package main

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Update checking: poll the GitHub Releases API for a newer version and flag it to the user (on the
// /config page and in /health). Stdlib only, no self-replacement — we surface the news + a download
// link for the user's platform; replacing a running binary + bundled ffmpeg/opencv across platforms
// is deliberately out of scope.

const (
	updateRepo     = "jaysuk/duet-webcam-bridge"
	updateAPI      = "https://api.github.com/repos/" + updateRepo + "/releases/latest"
	updateInterval = 24 * time.Hour
)

// UpdateStatus is the latest known result of a release check (serialised in /health and the config page).
type UpdateStatus struct {
	Available bool      `json:"available"`
	Current   string    `json:"current"`
	Latest    string    `json:"latest"`
	URL       string    `json:"url"`                // release page
	AssetURL  string    `json:"assetUrl,omitempty"` // direct archive download for this platform, if matched
	AssetName string    `json:"assetName,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
	Error     string    `json:"error,omitempty"`
}

// updater holds the most recent check result behind a mutex.
type updater struct {
	mu     sync.Mutex
	status UpdateStatus
}

func (u *updater) get() UpdateStatus {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.status
}

func (u *updater) set(s UpdateStatus) {
	u.mu.Lock()
	u.status = s
	u.mu.Unlock()
}

// platformTarget maps the running OS/arch to the release archive's target suffix (matching package.sh).
// macOS uses the x86_64 archive on both Intel and Apple Silicon (it runs via Rosetta). Empty = no
// prebuilt archive for this platform (e.g. 32-bit Pi), so we still flag the version but offer no asset.
func platformTarget() string {
	switch runtime.GOOS {
	case "windows":
		if runtime.GOARCH == "amd64" {
			return "windows-amd64"
		}
	case "darwin":
		return "macos-amd64"
	case "linux":
		switch runtime.GOARCH {
		case "amd64":
			return "linux-amd64"
		case "arm64":
			return "linux-arm64"
		}
	}
	return ""
}

// compareVersions returns -1, 0, 1 comparing dotted numeric versions (a<b, a==b, a>b). A leading "v"
// and any prerelease suffix ("-rc1", "+build") are ignored, so 1.2.3-rc == 1.2.3 for flag purposes.
func compareVersions(a, b string) int {
	pa, pb := splitVersion(a), splitVersion(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func splitVersion(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	for _, sep := range []string{"-", "+"} {
		if i := strings.Index(v, sep); i >= 0 {
			v = v[:i]
		}
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, _ := strconv.Atoi(strings.TrimSpace(p))
		out = append(out, n)
	}
	return out
}

// checkForUpdate queries GitHub's latest-release API. Never panics; failures are recorded in .Error.
func checkForUpdate(ctx context.Context, current string) UpdateStatus {
	return checkRelease(ctx, &http.Client{Timeout: 15 * time.Second}, updateAPI, current)
}

// checkRelease is the testable core: it takes the client + API URL so tests can point at httptest.
func checkRelease(ctx context.Context, client *http.Client, apiURL, current string) UpdateStatus {
	s := UpdateStatus{Current: current, CheckedAt: time.Now()}
	if current == "" || current == "dev" {
		s.Error = "dev build (update checks skipped)"
		return s
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		s.Error = err.Error()
		return s
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "duet-webcam-bridge/"+current)

	resp, err := client.Do(req)
	if err != nil {
		s.Error = err.Error()
		return s
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		s.Error = "github API HTTP " + strconv.Itoa(resp.StatusCode)
		return s
	}

	var rel struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		s.Error = "parsing github response: " + err.Error()
		return s
	}

	s.Latest = strings.TrimPrefix(rel.TagName, "v")
	s.URL = rel.HTMLURL
	if s.Latest == "" {
		s.Error = "no tag_name in latest release"
		return s
	}
	if compareVersions(s.Latest, current) > 0 {
		s.Available = true
		if target := platformTarget(); target != "" {
			for _, a := range rel.Assets {
				if strings.Contains(a.Name, target) {
					s.AssetURL = a.URL
					s.AssetName = a.Name
					break
				}
			}
		}
	}
	return s
}
