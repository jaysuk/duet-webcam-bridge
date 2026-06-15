// Command duet-webcam-bridge publishes a locally-connected USB camera over HTTP
// as both a single JPEG (/snapshot) and a live MJPEG stream (/stream), so it can
// be shown in Duet Web Control's webcam panel from any machine on the network.
//
// It drives a bundled ffmpeg for the actual capture, which keeps the program a
// single tiny static binary while supporting USB cameras on Windows, Linux and
// macOS.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(log.Ltime)

	cfg, f, err := loadConfig(os.Args[1:])
	if err != nil {
		// flag.ContinueOnError already printed usage for parse errors.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	if f.version {
		fmt.Printf("duet-webcam-bridge %s\n", version)
		return
	}

	ffmpegPath, err := findFFmpeg(cfg.FFmpegPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fmt.Fprintln(os.Stderr, "Make sure ffmpeg(.exe) sits next to this program (it ships in the release) or is on your PATH.")
		os.Exit(1)
	}

	if f.list {
		printCameras(ffmpegPath)
		return
	}

	cam, err := NewCamera(cfg, ffmpegPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go cam.Run(ctx)

	addr := net.JoinHostPort(cfg.Bind, strconv.Itoa(cfg.Port))
	srv := &http.Server{
		Addr:    addr,
		Handler: NewServer(cam, cfg).Handler(),
	}

	printBanner(cfg, ffmpegPath)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func printBanner(cfg Config, ffmpegPath string) {
	fmt.Println()
	fmt.Printf("  Duet Webcam Bridge %s\n", version)
	if v := probeFFmpegVersion(ffmpegPath); v != "" {
		fmt.Printf("  using %s\n", v)
	}
	dev := cfg.Device
	if dev == "" {
		dev = "(auto)"
	}
	fmt.Printf("  camera: %s\n", dev)
	fmt.Println()

	port := strconv.Itoa(cfg.Port)
	ips := lanIPs()
	fmt.Println("  Open this in a browser to check it works, then paste a URL into")
	fmt.Println("  DWC -> Settings -> Webcam:")
	fmt.Println()
	if len(ips) == 0 {
		ips = []string{"localhost"}
	}
	for _, ip := range ips {
		base := "http://" + net.JoinHostPort(ip, port)
		fmt.Printf("    stream (live):   %s/stream      (DWC update interval = 0)\n", base)
		fmt.Printf("    snapshot (poll): %s/snapshot\n", base)
		fmt.Println()
	}
	fmt.Println("  Press Ctrl+C to stop.")
	fmt.Println()
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
