package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// App ties the whole thing together: it owns the current configuration, the live
// capture (which it can reload in-process when settings change via /config), and
// an HTTP server it can rebind to a new address if the port/bind changes.
type App struct {
	ffmpegPath string
	rpicamPath string

	mu        sync.Mutex
	cfg       Config
	cam       *Camera
	camCancel context.CancelFunc
	srv       *http.Server

	upd updater // latest GitHub release-check result

	parentCtx context.Context
	rebind    chan struct{}
}

func NewApp(cfg Config, ffmpegPath, rpicamPath string) *App {
	return &App{
		cfg:        cfg,
		ffmpegPath: ffmpegPath,
		rpicamPath: rpicamPath,
		rebind:     make(chan struct{}, 1),
	}
}

// currentCam returns the live camera (may be nil if it failed to start).
func (a *App) currentCam() *Camera {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cam
}

func (a *App) config() Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

// startCamera builds a camera from the current config and swaps it in, retiring
// the previous one. A start failure (e.g. missing rpicam) is returned but does
// not tear down the server, so the user can still fix things at /config.
func (a *App) startCamera() error {
	a.mu.Lock()
	cfg := a.cfg
	parent := a.parentCtx
	a.mu.Unlock()

	cam, err := NewCamera(cfg, a.ffmpegPath, a.rpicamPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	go cam.Run(ctx)

	a.mu.Lock()
	oldCancel := a.camCancel
	a.cam = cam
	a.camCancel = cancel
	a.mu.Unlock()

	if oldCancel != nil {
		oldCancel() // retire the previous camera (kills its ffmpeg/rpicam)
	}
	return nil
}

// Apply validates and persists a new config, reloads the camera, and reports
// whether the listening address changed (so the caller can trigger a rebind
// after it has finished responding to the client).
func (a *App) Apply(newCfg Config) (addrChanged bool, err error) {
	if err := validateConfig(newCfg); err != nil {
		return false, err
	}
	if err := a.writeConfigFile(newCfg); err != nil {
		return false, err
	}
	a.mu.Lock()
	oldAddr := net.JoinHostPort(a.cfg.Bind, strconv.Itoa(a.cfg.Port))
	a.cfg = newCfg
	newAddr := net.JoinHostPort(newCfg.Bind, strconv.Itoa(newCfg.Port))
	a.mu.Unlock()

	camErr := a.startCamera()
	if camErr != nil {
		// Config is saved; surface the camera problem so the UI can show it.
		return oldAddr != newAddr, fmt.Errorf("settings saved, but the camera failed to start: %w", camErr)
	}
	return oldAddr != newAddr, nil
}

func (a *App) triggerRebind() {
	select {
	case a.rebind <- struct{}{}:
	default:
	}
}

// writeConfigFile writes config.json (pretty-printed) next to the executable.
func (a *App) writeConfigFile(cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := filepath.Join(exeDir(), "config.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// Run starts the camera and serves HTTP until ctx is cancelled, re-listening on
// a new address whenever a rebind is requested.
func (a *App) Run(ctx context.Context) {
	a.mu.Lock()
	a.parentCtx = ctx
	a.mu.Unlock()

	if err := a.startCamera(); err != nil {
		log.Printf("camera did not start: %v", err)
		log.Printf("the bridge is still running - open /config in a browser to fix the settings")
	}

	go a.runUpdateChecks(ctx)

	first := true
	for {
		addr := net.JoinHostPort(a.config().Bind, strconv.Itoa(a.config().Port))
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("cannot listen on %s: %v (retrying in 3s)", addr, err)
			if sleepCtx(ctx, 3*time.Second) {
				return
			}
			continue
		}

		srv := &http.Server{Handler: a.handler()}
		a.mu.Lock()
		a.srv = srv
		a.mu.Unlock()

		if first {
			a.printBanner()
			first = false
		} else {
			log.Printf("now serving on %s", addr)
		}

		errc := make(chan error, 1)
		go func() { errc <- srv.Serve(ln) }()

		select {
		case <-ctx.Done():
			a.shutdown(srv)
			return
		case <-a.rebind:
			a.shutdown(srv) // loop and re-listen on the (new) address
		case err := <-errc:
			if ctx.Err() != nil {
				return
			}
			log.Printf("http server error: %v (restarting listener in 3s)", err)
			if sleepCtx(ctx, 3*time.Second) {
				return
			}
		}
	}
}

func (a *App) shutdown(srv *http.Server) {
	sd, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sd)
}

// runUpdateChecks polls GitHub for a newer release on startup (after a short delay) and then daily,
// storing the result for /config and /health to surface. Stdlib-only and best-effort: failures are
// recorded in the status, never fatal. "dev" builds never check.
func (a *App) runUpdateChecks(ctx context.Context) {
	if version == "dev" {
		return
	}
	if sleepCtx(ctx, 5*time.Second) {
		return
	}
	for {
		if a.config().CheckUpdates {
			s := checkForUpdate(ctx, version)
			a.upd.set(s)
			if s.Available {
				log.Printf("update available: v%s (you have v%s) - %s", s.Latest, s.Current, s.URL)
			}
		}
		if sleepCtx(ctx, updateInterval) {
			return
		}
	}
}

// updateStatus returns the latest release-check result (zero value before the first check).
func (a *App) updateStatus() UpdateStatus {
	return a.upd.get()
}

// sleepCtx waits for d or ctx cancellation; returns true if ctx was cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return true
	case <-time.After(d):
		return false
	}
}

func (a *App) printBanner() {
	cfg := a.config()
	fmt.Println()
	fmt.Printf("  Duet Webcam Bridge %s\n", version)
	if a.ffmpegPath != "" {
		if v := probeFFmpegVersion(a.ffmpegPath); v != "" {
			fmt.Printf("  using %s\n", v)
		}
	}
	if cam := a.currentCam(); cam != nil {
		fmt.Printf("  source: %s\n", cam.Description())
	}
	fmt.Println()

	port := strconv.Itoa(cfg.Port)
	ips := lanIPs()
	if len(ips) == 0 {
		ips = []string{"localhost"}
	}
	fmt.Println("  Open this in a browser to check it works, then paste a URL into")
	fmt.Println("  DWC -> Settings -> Webcam:")
	fmt.Println()
	for _, ip := range ips {
		base := "http://" + net.JoinHostPort(ip, port)
		fmt.Printf("    stream (live):   %s/stream      (DWC update interval = 0)\n", base)
		fmt.Printf("    snapshot (poll): %s/snapshot\n", base)
		fmt.Printf("    settings page:   %s/config\n", base)
		fmt.Println()
	}
	fmt.Println("  Press Ctrl+C to stop.")
	fmt.Println()
}
