package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
)

// The site's kubeconfig (as cmd/platform's kubeconfig handler writes it) lands in KUBECONFIG as
// ctrlplane-<name>, signing in through this CLI, next to what was there.
func TestMergeKubeconfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: Config
clusters: [{name: kind, cluster: {server: "https://127.0.0.1:6443"}}]
users: [{name: kind, user: {token: x}}]
contexts: [{name: kind, context: {cluster: kind, user: kind}}]
current-context: kind
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
	site := []byte(`apiVersion: v1
kind: Config
clusters: [{name: alice-dev, cluster: {server: "https://abc.api.ctrlplane.run", certificate-authority-data: Q0E=}}]
users: [{name: oidc, user: {exec: {apiVersion: client.authentication.k8s.io/v1, command: kubectl, args: [oidc-login], interactiveMode: IfAvailable}}}]
contexts: [{name: alice-dev, context: {cluster: alice-dev, user: oidc, namespace: alice-dev}}]
current-context: alice-dev
`)
	key, err := mergeKubeconfig("alice-dev", site, "/usr/local/bin/ctrlplane", true)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := cfg.Contexts[key]
	if key != "ctrlplane-alice-dev" || cfg.CurrentContext != key || ctx == nil || ctx.Namespace != "alice-dev" {
		t.Fatalf("context %s: %+v, current %s", key, ctx, cfg.CurrentContext)
	}
	if c := cfg.Clusters[ctx.Cluster]; c.Server != "https://abc.api.ctrlplane.run" || string(c.CertificateAuthorityData) != "CA" {
		t.Errorf("cluster: %+v", c)
	}
	if e := cfg.AuthInfos[ctx.AuthInfo].Exec; e == nil || e.Command != "/usr/local/bin/ctrlplane" || !slices.Equal(e.Args, []string{"token"}) {
		t.Errorf("user: %+v", e)
	}
	if cfg.Contexts["kind"] == nil || cfg.AuthInfos["kind"].Token != "x" {
		t.Error("the existing context is gone")
	}

	// Again, without switching (--cp): still there, current unchanged.
	cfg.CurrentContext = "kind"
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		t.Fatal(err)
	}
	if _, err := mergeKubeconfig("alice-dev", site, "/usr/local/bin/ctrlplane", false); err != nil {
		t.Fatal(err)
	}
	if cfg, _ = clientcmd.LoadFromFile(path); cfg.CurrentContext != "kind" || cfg.Contexts[key] == nil {
		t.Errorf("without switching: current %s", cfg.CurrentContext)
	}
}
