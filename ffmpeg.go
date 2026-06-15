package main

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
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
				return "0:none", nil
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
		// "<video>:<audio>"; we never want audio for a print cam.
		if strings.Contains(device, ":") {
			return device, nil
		}
		return device + ":none", nil
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
			cams = append(cams, CameraInfo{ID: m[1], Name: m[2]})
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
