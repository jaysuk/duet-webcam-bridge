package main

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

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
