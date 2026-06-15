// Command duet-webcam-bridge publishes a camera over HTTP as both a single JPEG
// (/snapshot) and a live MJPEG stream (/stream), so it can be shown in Duet Web
// Control's webcam panel from any machine on the network.
//
// The camera can be a local USB/built-in camera (default), a Raspberry Pi CSI
// camera (via rpicam-vid), or a network/IP camera (RTSP/HTTP, transcoded by the
// bundled ffmpeg). USB/network capture uses a bundled ffmpeg, which keeps the
// program a single tiny static binary that works on Windows, Linux and macOS.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func main() {
	log.SetFlags(log.Ltime)

	cfg, f, err := loadConfig(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	if f.version {
		fmt.Printf("duet-webcam-bridge %s\n", version)
		return
	}

	if f.installAutostart || f.uninstallAutostart {
		runAutostart(f.installAutostart)
		return
	}

	// Resolve both capture tools best-effort so the source can be switched live
	// from /config without a restart. A missing tool only matters once a source
	// that needs it is actually selected (surfaced via the camera's error).
	ffmpegPath, _ := findFFmpeg(cfg.FFmpegPath)
	rpicamPath := findRpicam(cfg.RpicamPath)

	if f.list {
		if ffmpegPath == "" && !strings.EqualFold(cfg.Source, "csi") {
			fmt.Fprintln(os.Stderr, "ffmpeg not found next to the program or on PATH.")
			os.Exit(1)
		}
		printCameras(cfg, ffmpegPath, rpicamPath)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app := NewApp(cfg, ffmpegPath, rpicamPath)
	app.Run(ctx)
	log.Println("shutting down...")
}

// lanIPs returns the machine's non-loopback IPv4 addresses, so the banner can
// show the user exactly what to type into DWC on another PC.
func lanIPs() []string {
	var ips []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				ips = append(ips, ip4.String())
			}
		}
	}
	return ips
}

// fileExists reports whether path exists and is a regular file (helper shared
// with ffmpeg.go).
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
