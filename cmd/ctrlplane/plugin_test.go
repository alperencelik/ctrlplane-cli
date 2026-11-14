package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// plugin on / off / list call the site's Plugins endpoints of the current control plane, as the
// CLI (JSON), and say the site's reason when it refuses.
func TestPlugins(t *testing.T) {
	var got []string
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path+" "+r.Header.Get("Accept")+" "+r.Header.Get("Authorization"))
		switch {
		case r.Method == "GET":
			_, _ = io.WriteString(w, `{"plugins":[{"name":"crossplane-aws-s3","description":"S3","on":true}]}`)
		case r.Method == "DELETE":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"delete its resources first: buckets.s3.aws.m.upbound.io"}`)
		default:
			_, _ = io.WriteString(w, `{"name":"crossplane-aws-s3","on":true,"changed":true}`)
		}
	}))
	defer site.Close()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	c := &config{Server: site.URL, IDToken: "tok", Expiry: time.Now().Add(time.Hour), Current: "alice-dev"}
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := plugins(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := setPlugin(ctx, "", "crossplane-aws-s3", true); err != nil {
		t.Fatal(err)
	}
	if err := setPlugin(ctx, "", "crossplane-aws-s3", false); err == nil || !strings.Contains(err.Error(), "delete its resources first") {
		t.Errorf("off: %v, want the site's reason", err)
	}
	want := []string{
		"GET /tenants/alice-dev/plugins application/json Bearer tok",
		"POST /tenants/alice-dev/plugins/crossplane-aws-s3 application/json Bearer tok",
		"DELETE /tenants/alice-dev/plugins/crossplane-aws-s3 application/json Bearer tok",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("site got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
