package main

// Signing in: OIDC authorization code + PKCE against the platform's Dex, as the public client
// ctrlplane-cli (the one kubelogin uses), with a loopback redirect to one of the two ports Dex
// allows it. The ID token is what the site accepts as a bearer and tenant API servers accept
// as-is; the refresh token renews it. All of it lives in config.json, mode 0600.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

var errSignIn = errors.New("not signed in: run `ctrlplane login`")

type config struct {
	Server       string    `json:"server"`
	Issuer       string    `json:"issuer"`
	ClientID     string    `json:"clientID"`
	IDToken      string    `json:"idToken"`
	RefreshToken string    `json:"refreshToken"`
	Expiry       time.Time `json:"expiry"`
	Current      string    `json:"current,omitempty"` // the control plane `use` switched to
}

func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	return filepath.Join(dir, "ctrlplane", "config.json"), err
}

func loadConfig() (*config, error) {
	p, err := configPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errSignIn
	} else if err != nil {
		return nil, err
	}
	c := &config{}
	return c, json.Unmarshal(b, c)
}

func (c *config) save() error {
	p, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(p, b, 0o600)
}

func login(ctx context.Context, server string) error {
	c := &config{Server: strings.TrimRight(server, "/")}
	// The platform says where to sign in (GET /login, as JSON).
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.Server+"/login", nil)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	err = json.NewDecoder(resp.Body).Decode(c)
	_ = resp.Body.Close()
	if err != nil || c.Issuer == "" {
		return fmt.Errorf("%s doesn't look like a ctrlplane site (%v)", c.Server, err)
	}
	provider, err := oidc.NewProvider(ctx, c.Issuer)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "localhost:8000")
	if err != nil {
		ln, err = net.Listen("tcp", "localhost:18000") // the other redirect Dex allows (as kubelogin)
	}
	if err != nil {
		return fmt.Errorf("ports 8000 and 18000 are busy, free one to sign in: %w", err)
	}
	conf := oauth2.Config{ClientID: c.ClientID, Endpoint: provider.Endpoint(),
		RedirectURL: fmt.Sprintf("http://localhost:%d", ln.Addr().(*net.TCPAddr).Port),
		Scopes:      []string{oidc.ScopeOpenID, oidc.ScopeOfflineAccess, "profile", "email", "federated:id"}}
	state, verifier := oauth2.GenerateVerifier(), oauth2.GenerateVerifier()
	done := make(chan error, 1)
	var tok *oauth2.Token
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.NotFound(rw, r) // e.g. /favicon.ico
			return
		}
		var err error
		if e := q.Get("error"); e != "" {
			err = fmt.Errorf("sign-in failed: %s %s", e, q.Get("error_description"))
		} else {
			tok, err = conf.Exchange(r.Context(), q.Get("code"), oauth2.VerifierOption(verifier))
		}
		if err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
		} else {
			_, _ = io.WriteString(rw, "Signed in to ctrlplane. You can close this tab.\n")
		}
		select {
		case done <- err:
		default:
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	authURL := conf.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
	fmt.Fprintf(os.Stderr, "Opening your browser to sign in. If it doesn't open, visit:\n\n  %s\n\n", authURL)
	_ = openBrowser(authURL)
	select {
	case err := <-done:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := c.setToken(ctx, provider, tok); err != nil {
		return err
	}
	if err := c.save(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Signed in to %s.\n", c.Server)
	return nil
}

func logout() error {
	p, err := configPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Fprintln(os.Stderr, "Signed out.")
	return nil
}

func (c *config) setToken(ctx context.Context, p *oidc.Provider, tok *oauth2.Token) error {
	raw, _ := tok.Extra("id_token").(string)
	id, err := p.Verifier(&oidc.Config{ClientID: c.ClientID}).Verify(ctx, raw)
	if err != nil {
		return err
	}
	c.IDToken, c.Expiry = raw, id.Expiry
	if tok.RefreshToken != "" { // Dex rotates them
		c.RefreshToken = tok.RefreshToken
	}
	return nil
}

// idToken is a valid ID token, refreshed (and saved) when it's about to expire.
// ponytail: concurrent refreshes (parallel kubectl calls) race on the rotated refresh token;
// Dex's reuse interval absorbs it. Lock the config file if that ever bites.
func (c *config) idToken(ctx context.Context) (string, error) {
	if time.Until(c.Expiry) > time.Minute {
		return c.IDToken, nil
	}
	if c.RefreshToken == "" {
		return "", errSignIn
	}
	p, err := oidc.NewProvider(ctx, c.Issuer)
	if err != nil {
		return "", err
	}
	conf := oauth2.Config{ClientID: c.ClientID, Endpoint: p.Endpoint()}
	tok, err := conf.TokenSource(ctx, &oauth2.Token{RefreshToken: c.RefreshToken}).Token()
	if err != nil {
		return "", fmt.Errorf("session expired, run `ctrlplane login`: %w", err)
	}
	if err := c.setToken(ctx, p, tok); err != nil {
		return "", err
	}
	return c.IDToken, c.save()
}

// api calls the site as the signed-in user: form in, JSON (or *[]byte: the raw body) out.
func (c *config) api(ctx context.Context, method, path string, form url.Values, out any) error {
	tok, err := c.idToken(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Server+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return errSignIn
	}
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		if json.Unmarshal(b, &e) != nil || e.Error == "" {
			e.Error = strings.TrimSpace(string(b))
		}
		return errors.New(e.Error)
	}
	if raw, ok := out.(*[]byte); ok {
		*raw = b
		return nil
	}
	return json.Unmarshal(b, out)
}

func openBrowser(u string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	}
	return exec.Command("xdg-open", u).Start()
}
