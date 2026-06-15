package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
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

	// Device is the camera to capture from. Leave empty to auto-pick the first
	// one found. On Windows this is the DirectShow device *name* (run with
	// --list to see them); on Linux a /dev/videoN path; on macOS an AVFoundation
	// device index ("0", "1", ...) or name.
	Device string `json:"device"`

	// Resolution like "1280x720". Empty = let the camera/ffmpeg decide.
	Resolution string `json:"resolution"`
	// Framerate to request from the camera and emit. 0 = ffmpeg default.
	Framerate int `json:"framerate"`
	// Quality is ffmpeg's -q:v (2 = best/large ... 31 = worst/small). 5 is a
	// good middle ground for a print webcam.
	Quality int `json:"quality"`

	// InputFormat overrides the auto-detected ffmpeg input demuxer
	// (dshow / v4l2 / avfoundation). Leave empty unless you know you need it.
	InputFormat string `json:"inputFormat"`
	// FFmpegPath overrides the bundled ffmpeg. Empty = use the ffmpeg sitting
	// next to this executable, falling back to one on PATH.
	FFmpegPath string `json:"ffmpegPath"`
}

func defaultConfig() Config {
	return Config{
		Bind:      "0.0.0.0",
		Port:      8081,
		Framerate: 15,
		Quality:   5,
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
	fs.StringVar(&cfg.Device, "device", cfg.Device, "camera device (empty = auto; see --list)")
	fs.StringVar(&cfg.Resolution, "resolution", cfg.Resolution, "capture resolution e.g. 1280x720")
	fs.IntVar(&cfg.Framerate, "framerate", cfg.Framerate, "frames per second")
	fs.IntVar(&cfg.Quality, "quality", cfg.Quality, "JPEG quality, ffmpeg -q:v (2 best .. 31 worst)")
	fs.StringVar(&cfg.InputFormat, "input-format", cfg.InputFormat, "override ffmpeg input format")
	fs.StringVar(&cfg.FFmpegPath, "ffmpeg", cfg.FFmpegPath, "path to ffmpeg (empty = bundled/PATH)")
	fs.BoolVar(&f.list, "list", false, "list available cameras and exit")
	fs.BoolVar(&f.version, "version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		return cfg, nil, err
	}
	return cfg, f, nil
}

// flags are one-shot command-line actions that aren't part of Config.
type flags struct {
	list    bool
	version bool
}
