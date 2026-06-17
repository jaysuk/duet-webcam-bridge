package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config holds every tunable. Values are resolved in this order (later wins):
//   1. built-in defaults below
//   2. config.json next to the executable (if present)
//   3. command-line flags that were explicitly set
//
// Keeping the JSON tags lowercase keeps the hand-edited config.json friendly.
type Config struct {
	// Bind is the network interface to listen on. "0.0.0.0" means "all
	// interfaces", which is what you want so DWC on another machine can reach it.
	Bind string `json:"bind"`
	// Port to serve on. 8081 matches the Duet SBC webcam default.
	Port int `json:"port"`

	// AllowOrigin sets the Access-Control-Allow-Origin header on the camera and
	// asset endpoints. Plain <img> display works cross-origin without it, but a
	// browser plugin that reads pixels off a <canvas> (e.g. the tool-alignment
	// CV) needs it, otherwise the canvas is "tainted" and getImageData throws.
	// Default "*" (any origin); set a specific origin like "http://duet.local"
	// to lock it down, or "" to disable the header entirely.
	AllowOrigin string `json:"allowOrigin"`

	// Source selects where the video comes from:
	//   "usb"     a USB / built-in camera on this machine (default)
	//   "csi"     a Raspberry Pi CSI ribbon-cable camera (via rpicam-vid)
	//   "network" an IP camera reached over the network (RTSP/HTTP, via ffmpeg)
	Source string `json:"source"`

	// --- usb / csi device selection ---
	// Device is the camera to capture from. Leave empty to auto-pick the first.
	// usb: Windows DirectShow name / macOS AVFoundation index / Linux /dev/videoN.
	// csi: the rpicam camera index ("0", "1", ...).
	Device string `json:"device"`

	// --- network (IP camera) settings ---
	// URL is the camera stream/snapshot URL, e.g. rtsp://host:554/stream or
	// http://host/snapshot.jpg. Credentials may be embedded here, or supplied
	// separately via Username/Password (kept out of the URL/logs).
	URL string `json:"url"`
	// Username / Password for the network camera (HTTP basic/digest or RTSP).
	Username string `json:"username"`
	Password string `json:"password"`
	// RTSPTransport is "tcp" (default, reliable) or "udp".
	RTSPTransport string `json:"rtspTransport"`
	// NetworkMode is "stream" (default: transcode a live stream to MJPEG) or
	// "snapshot" (poll a single-JPEG URL and re-serve it, no transcoding).
	NetworkMode string `json:"networkMode"`
	// SnapshotInterval is the poll period in ms for NetworkMode "snapshot".
	SnapshotInterval int `json:"snapshotInterval"`

	// --- common capture options ---
	// Resolution like "1280x720". Empty = native. This is the *capture* size: for
	// usb on Windows/Linux it selects the camera's hardware mode; on macOS and
	// for network it's the baseline the output is scaled from when Scale is unset.
	Resolution string `json:"resolution"`
	// Crop cuts a region out of the captured image before scaling. ffmpeg crop
	// syntax: "w:h" (centred) or "w:h:x:y" (x,y = top-left offset), in pixels.
	// Empty = no crop. (Not supported for csi.)
	Crop string `json:"crop"`
	// Scale resizes the (optionally cropped) image that DWC receives, e.g.
	// "640x480" or "1280x720". Use -1 for one axis to keep the aspect ratio,
	// e.g. "640x-1". Empty = no explicit scaling.
	Scale string `json:"scale"`
	// Framerate to emit. 0 = source default.
	Framerate int `json:"framerate"`
	// Quality is ffmpeg's -q:v (2 = best/large ... 31 = worst/small).
	Quality int `json:"quality"`
	// PixelFormat overrides the capture input pixel format (advanced; e.g.
	// "nv12"/"uyvy422" can help fussy macOS cameras). Empty = let ffmpeg decide.
	PixelFormat string `json:"pixelFormat"`

	// --- advanced / overrides ---
	// InputFormat overrides the auto-detected ffmpeg input demuxer for usb
	// (dshow / v4l2 / avfoundation). Leave empty unless you know you need it.
	InputFormat string `json:"inputFormat"`
	// FFmpegPath overrides the bundled ffmpeg. Empty = ffmpeg next to this
	// executable, falling back to PATH.
	FFmpegPath string `json:"ffmpegPath"`
	// RpicamPath overrides the rpicam-vid binary used for csi. Empty = PATH.
	RpicamPath string `json:"rpicamPath"`
	// LogLevel is ffmpeg's -loglevel. Default "error". Set "verbose" or "info"
	// to see the full device negotiation when diagnosing a camera that won't
	// start.
	LogLevel string `json:"logLevel"`

	// OpenCVDir is the directory whose contents are served under /opencv/ — used
	// to host the OpenCV.js runtime (opencv.js + opencv_js.wasm) for the browser
	// tool-alignment plugin, so the CV engine loads from this bridge instead of a
	// CDN or the Duet's SD card. Empty = "opencv" next to the executable. The
	// route simply 404s when the directory is absent, so it's a no-op (and
	// effectively opt-out) unless you ship the assets alongside the binary.
	OpenCVDir string `json:"openCVDir"`
}

func defaultConfig() Config {
	return Config{
		Bind:             "0.0.0.0",
		Port:             8081,
		AllowOrigin:      "*",
		Source:           "usb",
		RTSPTransport:    "tcp",
		NetworkMode:      "stream",
		SnapshotInterval: 1000,
		Framerate:        15,
		Quality:          5,
	}
}

// exeDir returns the directory containing the running executable, so we can
// find ffmpeg and config.json that were shipped alongside it.
func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		if wd, werr := os.Getwd(); werr == nil {
			return wd
		}
		return "."
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}

// loadConfig resolves defaults -> config.json -> explicit flags. It also returns
// the parsed flag set so callers can react to one-shot flags like --list.
func loadConfig(args []string) (Config, *flags, error) {
	cfg := defaultConfig()

	// Layer config.json (if present) over the defaults.
	cfgPath := filepath.Join(exeDir(), "config.json")
	if data, err := os.ReadFile(cfgPath); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return cfg, nil, fmt.Errorf("parsing %s: %w", cfgPath, err)
		}
	} else if !os.IsNotExist(err) {
		return cfg, nil, fmt.Errorf("reading %s: %w", cfgPath, err)
	}

	// Now define flags using the (possibly file-overridden) values as defaults,
	// so an explicitly-passed flag always wins over the file.
	fs := flag.NewFlagSet("duet-webcam-bridge", flag.ContinueOnError)
	f := &flags{}
	fs.StringVar(&cfg.Bind, "bind", cfg.Bind, "interface to listen on (0.0.0.0 = all)")
	fs.IntVar(&cfg.Port, "port", cfg.Port, "port to serve on")
	fs.StringVar(&cfg.AllowOrigin, "allow-origin", cfg.AllowOrigin, "Access-Control-Allow-Origin for camera/asset endpoints (* = any, empty = off)")
	fs.StringVar(&cfg.Source, "source", cfg.Source, "video source: usb | csi | network")
	fs.StringVar(&cfg.Device, "device", cfg.Device, "camera device (empty = auto; see --list)")
	fs.StringVar(&cfg.URL, "url", cfg.URL, "network camera URL (rtsp/http)")
	fs.StringVar(&cfg.Username, "username", cfg.Username, "network camera username")
	fs.StringVar(&cfg.Password, "password", cfg.Password, "network camera password")
	fs.StringVar(&cfg.NetworkMode, "network-mode", cfg.NetworkMode, "network camera mode: stream | snapshot")
	fs.StringVar(&cfg.Resolution, "resolution", cfg.Resolution, "capture resolution e.g. 1280x720")
	fs.StringVar(&cfg.Crop, "crop", cfg.Crop, "crop region w:h or w:h:x:y (pixels)")
	fs.StringVar(&cfg.Scale, "scale", cfg.Scale, "output size WxH (use -1 to keep aspect, e.g. 640x-1)")
	fs.IntVar(&cfg.Framerate, "framerate", cfg.Framerate, "frames per second")
	fs.IntVar(&cfg.Quality, "quality", cfg.Quality, "JPEG quality, ffmpeg -q:v (2 best .. 31 worst)")
	fs.StringVar(&cfg.PixelFormat, "pixel-format", cfg.PixelFormat, "input pixel format override")
	fs.StringVar(&cfg.InputFormat, "input-format", cfg.InputFormat, "override ffmpeg input format")
	fs.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "ffmpeg -loglevel (e.g. verbose) for diagnostics")
	fs.StringVar(&cfg.OpenCVDir, "opencv-dir", cfg.OpenCVDir, "directory served at /opencv/ (empty = ./opencv next to the exe)")
	fs.StringVar(&cfg.FFmpegPath, "ffmpeg", cfg.FFmpegPath, "path to ffmpeg (empty = bundled/PATH)")
	fs.BoolVar(&f.list, "list", false, "list available cameras and exit")
	fs.BoolVar(&f.version, "version", false, "print version and exit")
	fs.BoolVar(&f.installAutostart, "install-autostart", false, "start automatically at boot/login and exit")
	fs.BoolVar(&f.uninstallAutostart, "uninstall-autostart", false, "remove autostart and exit")

	if err := fs.Parse(args); err != nil {
		return cfg, nil, err
	}
	return cfg, f, nil
}

// validateConfig checks a config before it is persisted/applied, returning a
// friendly error for the /config page.
func validateConfig(cfg Config) error {
	if cfg.Port < 1 || cfg.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	switch strings.ToLower(cfg.Source) {
	case "", "usb", "auto", "csi", "libcamera", "rpicam":
		// ok
	case "network", "ip":
		if strings.TrimSpace(cfg.URL) == "" {
			return fmt.Errorf("a network camera needs a URL (e.g. rtsp://camera/stream)")
		}
		if _, _, err := buildNetworkURL(cfg.URL, cfg.Username, cfg.Password); err != nil {
			return err
		}
		if m := strings.ToLower(cfg.NetworkMode); m != "" && m != "stream" && m != "snapshot" {
			return fmt.Errorf("networkMode must be \"stream\" or \"snapshot\"")
		}
	default:
		return fmt.Errorf("source must be usb, network or csi")
	}
	if cfg.Resolution != "" {
		if _, _, ok := splitResolution(cfg.Resolution); !ok {
			return fmt.Errorf("resolution must look like 1280x720")
		}
	}
	if _, err := normalizeCrop(cfg.Crop); err != nil {
		return err
	}
	if _, err := normalizeScale(cfg.Scale); err != nil {
		return err
	}
	if cfg.Framerate < 0 || cfg.Framerate > 240 {
		return fmt.Errorf("framerate must be between 0 and 240")
	}
	if cfg.Quality != 0 && (cfg.Quality < 1 || cfg.Quality > 31) {
		return fmt.Errorf("quality must be between 1 (best) and 31 (worst)")
	}
	if cfg.SnapshotInterval < 0 {
		return fmt.Errorf("snapshotInterval cannot be negative")
	}
	return nil
}

// flags are one-shot command-line actions that aren't part of Config.
type flags struct {
	list               bool
	version            bool
	installAutostart   bool
	uninstallAutostart bool
}
