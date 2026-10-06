package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeployForm(t *testing.T) {
	f, err := deployForm("guestbook", []string{"--leader-elect", "--zap-devel"},
		deployOpts{image: "ghcr.io/you/guestbook:v1", env: []string{"A=1", "B=x=y"}, cpu: "250m", webhookPort: 9443,
			metricsPort: "8443", metricsHTTPS: true, registryUser: "you", passwordStdin: true}, strings.NewReader("s3cret\n"))
	if err != nil {
		t.Fatal(err)
	}
	// As the site's form sends them: args and env one per line, the password without its newline.
	for k, want := range map[string]string{"name": "guestbook", "image": "ghcr.io/you/guestbook:v1", "args": "--leader-elect\n--zap-devel",
		"env": "A=1\nB=x=y", "cpu": "250m", "webhookPort": "9443", "metricsPort": "8443", "metricsHTTPS": "on", "registryUser": "you", "registryPassword": "s3cret"} {
		if got := f.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if f.Has("memory") || f.Has("source") || f.Has("metricsPath") {
		t.Errorf("unset fields sent: %v", f)
	}

	dir := t.TempDir()
	yaml := filepath.Join(dir, "manager.yaml")
	if err := os.WriteFile(yaml, []byte("kind: Deployment"), 0o600); err != nil {
		t.Fatal(err)
	}
	if f, err := deployForm("", nil, deployOpts{file: yaml}, nil); err != nil || f.Get("source") != "yaml" || f.Get("yaml") != "kind: Deployment" || f.Has("name") {
		t.Errorf("-f file: %v, %v", f, err)
	}
	if f, err := deployForm("", nil, deployOpts{file: "https://example.com/install.yaml"}, nil); err != nil || f.Get("url") != "https://example.com/install.yaml" {
		t.Errorf("-f URL: %v, %v", f, err)
	}
	for name, o := range map[string]deployOpts{
		"no image or file":  {},
		"password, no user": {image: "x", passwordStdin: true},
		"stdin used twice":  {file: "-", passwordStdin: true, registryUser: "you"},
	} {
		if _, err := deployForm("c", nil, o, strings.NewReader("")); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, err := deployForm("", nil, deployOpts{image: "x"}, nil); err == nil {
		t.Error("--image without a name: want an error")
	}
}
