package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.5.1", "0.5.1", 0},
		{"0.5.2", "0.5.1", 1},
		{"0.5.1", "0.5.2", -1},
		{"v0.6.0", "0.5.9", 1}, // leading v ignored
		{"1.0.0", "0.9.9", 1},
		{"0.5.1-rc1", "0.5.1", 0}, // prerelease suffix ignored
		{"0.10.0", "0.9.0", 1},    // numeric, not lexical
		{"1.2", "1.2.0", 0},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCheckRelease_DevBuildSkips(t *testing.T) {
	s := checkRelease(context.Background(), http.DefaultClient, "http://unused", "dev")
	if s.Available || s.Error == "" {
		t.Errorf("dev build should skip with an error note, got %+v", s)
	}
}

func TestCheckRelease_NewerAvailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"tag_name": "v9.9.9",
			"html_url": "https://example.com/releases/v9.9.9",
			"assets": [
				{"name": "duet-webcam-bridge-9.9.9-windows-amd64.zip", "browser_download_url": "https://example.com/win.zip"},
				{"name": "duet-webcam-bridge-9.9.9-linux-amd64.tar.gz", "browser_download_url": "https://example.com/linux.tgz"}
			]
		}`))
	}))
	defer srv.Close()

	s := checkRelease(context.Background(), srv.Client(), srv.URL, "0.5.1")
	if !s.Available {
		t.Fatalf("expected update available, got %+v", s)
	}
	if s.Latest != "9.9.9" || s.URL == "" {
		t.Errorf("unexpected latest/url: %+v", s)
	}
	// When a platform archive exists in the assets it should be matched.
	if platformTarget() != "" && s.AssetURL == "" {
		t.Errorf("expected an asset match for target %q, got none", platformTarget())
	}
}

func TestCheckRelease_UpToDate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.5.1","html_url":"https://example.com"}`))
	}))
	defer srv.Close()

	s := checkRelease(context.Background(), srv.Client(), srv.URL, "0.5.1")
	if s.Available {
		t.Errorf("same version should not be flagged: %+v", s)
	}
}

func TestCheckRelease_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // e.g. rate limited
	}))
	defer srv.Close()

	s := checkRelease(context.Background(), srv.Client(), srv.URL, "0.5.1")
	if s.Available || s.Error == "" {
		t.Errorf("HTTP error should be recorded, not fatal: %+v", s)
	}
}
