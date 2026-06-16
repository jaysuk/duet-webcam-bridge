package main

import (
	"bufio"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// findFFmpeg returns the ffmpeg to use: an explicit override, otherwise the
// copy bundled next to this executable, otherwise whatever is on PATH.
func findFFmpeg(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	name := "ffmpeg"
	if runtime.GOOS == "windows" {
		name = "ffmpeg.exe"
	}
	bundled := filepath.Join(exeDir(), name)
	if fileExists(bundled) {
		return bundled, nil
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("ffmpeg not found next to the program or on PATH")
}

// findRpicam locates rpicam-vid (or the older libcamera-vid) for CSI capture.
// Returns "" if neither is present; only meaningful on Linux/Raspberry Pi.
func findRpicam(override string) string {
	if override != "" {
		return override
	}
	for _, name := range []string{"rpicam-vid", "libcamera-vid"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// buildNetworkURL applies optional credentials to an IP-camera URL and returns
// both the full URL (for ffmpeg) and a redacted one (for logs/banner) with the
// password stripped, so secrets never reach the console or log file.
func buildNetworkURL(rawURL, user, pass string) (full, redacted string, err error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", "", fmt.Errorf("invalid url %q: %w", rawURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", "", fmt.Errorf("url %q must include a scheme and host (e.g. rtsp://host/stream)", rawURL)
	}
	if user != "" {
		u.User = url.UserPassword(user, pass)
	}
	full = u.String()

	r := *u
	if r.User != nil {
		r.User = url.User(r.User.Username()) // keep username, drop password
	}
	redacted = r.String()
	return full, redacted, nil
}

// splitResolution parses "1280x720" into its parts. Returns ok=false if empty
// or malformed.
func splitResolution(res string) (w, h int, ok bool) {
	res = strings.TrimSpace(res)
	if res == "" {
		return 0, 0, false
	}
	parts := strings.FieldsFunc(res, func(r rune) bool { return r == 'x' || r == 'X' })
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

// baseName is filepath.Base, used for tidy log prefixes ("ffmpeg", "rpicam-vid").
func baseName(p string) string {
	return strings.TrimSuffix(filepath.Base(p), ".exe")
}

func splitInts(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == 'x' || r == 'X' || r == ':' || r == ',' || r == ' '
	})
	return fields
}

// normalizeCrop validates a crop spec and returns the ffmpeg crop arguments
// ("w:h" centred, or "w:h:x:y"). Empty input returns "".
func normalizeCrop(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	parts := splitInts(s)
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return "", fmt.Errorf("crop must be numbers like w:h or w:h:x:y (got %q)", s)
		}
		nums = append(nums, n)
	}
	switch len(nums) {
	case 2:
		if nums[0] <= 0 || nums[1] <= 0 {
			return "", fmt.Errorf("crop width and height must be positive")
		}
		return fmt.Sprintf("%d:%d", nums[0], nums[1]), nil
	case 4:
		if nums[0] <= 0 || nums[1] <= 0 || nums[2] < 0 || nums[3] < 0 {
			return "", fmt.Errorf("crop w:h must be positive and x:y non-negative")
		}
		return fmt.Sprintf("%d:%d:%d:%d", nums[0], nums[1], nums[2], nums[3]), nil
	default:
		return "", fmt.Errorf("crop must be w:h or w:h:x:y (got %q)", s)
	}
}

// normalizeScale validates a scale spec and returns ffmpeg scale arguments
// ("w:h"). One axis may be -1 to preserve the aspect ratio. Empty returns "".
func normalizeScale(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	parts := splitInts(s)
	if len(parts) != 2 {
		return "", fmt.Errorf("scale must look like 640x480 (or 640x-1) (got %q)", s)
	}
	w, err1 := strconv.Atoi(parts[0])
	h, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return "", fmt.Errorf("scale must be numbers like 640x480 (got %q)", s)
	}
	valid := func(v int) bool { return v == -1 || v > 0 }
	if !valid(w) || !valid(h) || (w == -1 && h == -1) {
		return "", fmt.Errorf("scale values must be positive (or -1 on one axis to keep aspect)")
	}
	return fmt.Sprintf("%d:%d", w, h), nil
}

// listCSICameras prints the Pi CSI cameras rpicam-vid can see.
func listCSICameras(rpicamPath string) {
	if rpicamPath == "" {
		fmt.Fprintln(os.Stderr, "rpicam-vid not found; install rpicam-apps (Raspberry Pi OS).")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, rpicamPath, "--list-cameras").CombinedOutput()
	fmt.Print(string(out))
	fmt.Println("\nSet the camera index as \"device\" in config.json (e.g. \"device\": \"0\").")
}

// defaultInputFormat is the ffmpeg capture demuxer for the current OS.
func defaultInputFormat() string {
	switch runtime.GOOS {
	case "windows":
		return "dshow"
	case "darwin":
		return "avfoundation"
	default:
		return "v4l2"
	}
}

// deviceArg turns a configured device into the argument ffmpeg's -i expects for
// the given input format. An empty device is resolved to the first camera.
func deviceArg(inputFormat, device string, ffmpegPath string) (string, error) {
	if device == "" {
		cams, err := listCameras(ffmpegPath)
		if err != nil || len(cams) == 0 {
			// Fall back to sensible per-platform defaults if listing failed.
			switch inputFormat {
			case "dshow":
				return "", fmt.Errorf("no camera found; run with --list and set \"device\" in config.json")
			case "avfoundation":
				return "0", nil
			default:
				return "/dev/video0", nil
			}
		}
		device = cams[0].ID
	}

	switch inputFormat {
	case "dshow":
		return "video=" + device, nil
	case "avfoundation":
		// Video device only (index or name). A "<video>:<audio>" form is passed
		// through if the user supplied one; otherwise we capture video only and
		// drop audio with -an on the output. (Appending ":none" makes some
		// ffmpeg builds try to open an audio device called "none" and fail.)
		return device, nil
	default: // v4l2
		return device, nil
	}
}

// Camera describes a discovered capture device.
type CameraInfo struct {
	ID   string `json:"id"`   // what you put in config.json "device"
	Name string `json:"name"` // human-friendly label
}

// listCameras enumerates capture devices using ffmpeg (or /dev on Linux).
func listCameras(ffmpegPath string) ([]CameraInfo, error) {
	switch defaultInputFormat() {
	case "dshow":
		return listDShow(ffmpegPath)
	case "avfoundation":
		return listAVFoundation(ffmpegPath)
	default:
		return listV4L2()
	}
}

// runFFmpegStderr runs ffmpeg with the given args and returns its stderr text.
// Device-listing commands intentionally "fail" (exit non-zero) so we ignore the
// error and just read what they printed.
func runFFmpegStderr(ffmpegPath string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	var sb strings.Builder
	cmd.Stderr = &sb
	_ = cmd.Run()
	return sb.String()
}

var dshowLine = regexp.MustCompile(`"([^"]+)"`)

func listDShow(ffmpegPath string) ([]CameraInfo, error) {
	out := runFFmpegStderr(ffmpegPath, "-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy")
	var cams []CameraInfo
	inVideo := false
	for _, line := range strings.Split(out, "\n") {
		low := strings.ToLower(line)
		if strings.Contains(low, "directshow video devices") {
			inVideo = true
			continue
		}
		if strings.Contains(low, "directshow audio devices") {
			inVideo = false
			continue
		}
		// Newer ffmpeg tags each line with (video)/(audio) regardless of header.
		if strings.Contains(low, "(audio)") {
			continue
		}
		if !inVideo && !strings.Contains(low, "(video)") {
			continue
		}
		if m := dshowLine.FindStringSubmatch(line); m != nil {
			name := m[1]
			// Skip the "Alternative name" device-path lines.
			if strings.HasPrefix(name, "@device") {
				continue
			}
			cams = append(cams, CameraInfo{ID: name, Name: name})
		}
	}
	return cams, nil
}

var avLine = regexp.MustCompile(`\[(\d+)\]\s+(.+?)\s*$`)

func listAVFoundation(ffmpegPath string) ([]CameraInfo, error) {
	out := runFFmpegStderr(ffmpegPath, "-hide_banner", "-f", "avfoundation", "-list_devices", "true", "-i", "")
	var cams []CameraInfo
	inVideo := false
	for _, line := range strings.Split(out, "\n") {
		low := strings.ToLower(line)
		if strings.Contains(low, "avfoundation video devices") {
			inVideo = true
			continue
		}
		if strings.Contains(low, "avfoundation audio devices") {
			inVideo = false
			continue
		}
		if !inVideo {
			continue
		}
		if m := avLine.FindStringSubmatch(line); m != nil {
			// Use the device NAME as the ID to put in config: avfoundation
			// re-orders the numeric indices between runs, but matches the name
			// reliably. ffmpeg accepts the name directly as the -i value.
			cams = append(cams, CameraInfo{ID: m[2], Name: m[2]})
		}
	}
	return cams, nil
}

func listV4L2() ([]CameraInfo, error) {
	matches, err := filepath.Glob("/dev/video*")
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	var cams []CameraInfo
	for _, m := range matches {
		cams = append(cams, CameraInfo{ID: m, Name: m})
	}
	return cams, nil
}

// probeFFmpegVersion returns the first line of `ffmpeg -version`, for banners.
func probeFFmpegVersion(ffmpegPath string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpegPath, "-hide_banner", "-version")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	if sc.Scan() {
		return strings.TrimSpace(sc.Text())
	}
	return ""
}
