package main

import (
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestCORSHeaders(t *testing.T) {
	app := NewApp(defaultConfig(), "", "") // default AllowOrigin "*"

	// A simple GET to /health (camera nil) still carries the ACAO header so the
	// browser plugin can read the response cross-origin.
	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()
	app.handler().ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("expected Access-Control-Allow-Origin *, got %q", got)
	}

	// An OPTIONS preflight is answered with 204 and the allowed methods.
	pre := httptest.NewRequest("OPTIONS", "/snapshot", nil)
	preRec := httptest.NewRecorder()
	app.handler().ServeHTTP(preRec, pre)
	if preRec.Code != 204 {
		t.Errorf("expected 204 for OPTIONS preflight, got %d", preRec.Code)
	}
	if !strings.Contains(preRec.Header().Get("Access-Control-Allow-Methods"), "GET") {
		t.Errorf("preflight should allow GET, got %q", preRec.Header().Get("Access-Control-Allow-Methods"))
	}

	// With AllowOrigin disabled no header is emitted.
	cfg := defaultConfig()
	cfg.AllowOrigin = ""
	off := NewApp(cfg, "", "")
	offRec := httptest.NewRecorder()
	off.handler().ServeHTTP(offRec, httptest.NewRequest("GET", "/health", nil))
	if got := offRec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("expected no ACAO header when disabled, got %q", got)
	}
}

func TestOpenCVRouteMissingDir404(t *testing.T) {
	cfg := defaultConfig()
	cfg.OpenCVDir = filepath.Join(t.TempDir(), "does-not-exist")
	app := NewApp(cfg, "", "")
	rec := httptest.NewRecorder()
	app.handler().ServeHTTP(rec, httptest.NewRequest("GET", "/opencv/opencv.js", nil))
	if rec.Code != 404 {
		t.Errorf("expected 404 for missing OpenCV asset, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("OpenCV route should still set CORS, got %q", got)
	}
}

func TestValidateConfig(t *testing.T) {
	base := defaultConfig()

	if err := validateConfig(base); err != nil {
		t.Errorf("default config should be valid: %v", err)
	}

	bad := base
	bad.Port = 0
	if err := validateConfig(bad); err == nil {
		t.Error("port 0 should be invalid")
	}

	net := base
	net.Source = "network"
	net.URL = ""
	if err := validateConfig(net); err == nil {
		t.Error("network source without URL should be invalid")
	}
	net.URL = "rtsp://cam/stream"
	if err := validateConfig(net); err != nil {
		t.Errorf("network source with URL should be valid: %v", err)
	}

	res := base
	res.Resolution = "huge"
	if err := validateConfig(res); err == nil {
		t.Error("bad resolution should be invalid")
	}

	q := base
	q.Quality = 50
	if err := validateConfig(q); err == nil {
		t.Error("quality 50 should be invalid")
	}
}

func TestParseConfigForm_KeepsPasswordWhenBlank(t *testing.T) {
	current := defaultConfig()
	current.Password = "existing"

	req := httptest.NewRequest("POST", "/config", strings.NewReader(url.Values{
		"source":   {"network"},
		"url":      {"rtsp://cam/stream"},
		"username": {"admin"},
		"password": {""}, // blank => keep existing
		"port":     {"8081"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}

	got := parseConfigForm(req, current)
	if got.Password != "existing" {
		t.Errorf("blank password should keep existing, got %q", got.Password)
	}
	if got.Username != "admin" || got.Source != "network" {
		t.Errorf("form fields not applied: %+v", got)
	}
}

func TestParseConfigForm_UpdatesPasswordAndNumbers(t *testing.T) {
	current := defaultConfig()
	current.Password = "old"

	req := httptest.NewRequest("POST", "/config", strings.NewReader(url.Values{
		"password":  {"new-secret"},
		"port":      {"9000"},
		"framerate": {"24"},
		"quality":   {"8"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = req.ParseForm()

	got := parseConfigForm(req, current)
	if got.Password != "new-secret" {
		t.Errorf("password = %q", got.Password)
	}
	if got.Port != 9000 || got.Framerate != 24 || got.Quality != 8 {
		t.Errorf("numbers not parsed: %+v", got)
	}
}
