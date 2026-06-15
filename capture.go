package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Camera owns the ffmpeg subprocess, keeps the most recent JPEG frame in memory,
// and fans new frames out to any connected MJPEG stream clients. It restarts
// ffmpeg automatically if the camera hiccups, so a transient USB glitch doesn't
// take the bridge down.
type Camera struct {
	cfg        Config
	ffmpegPath string
	inputFmt   string
	deviceArg  string

	mu        sync.RWMutex
	latest    []byte
	haveFrame bool
	lastErr   string

	subsMu sync.Mutex
	subs   map[chan []byte]struct{}
}

func NewCamera(cfg Config, ffmpegPath string) (*Camera, error) {
	inputFmt := cfg.InputFormat
	if inputFmt == "" {
		inputFmt = defaultInputFormat()
	}
	dev, err := deviceArg(inputFmt, cfg.Device, ffmpegPath)
	if err != nil {
		return nil, err
	}
	return &Camera{
		cfg:        cfg,
		ffmpegPath: ffmpegPath,
		inputFmt:   inputFmt,
		deviceArg:  dev,
		subs:       make(map[chan []byte]struct{}),
	}, nil
}

// ffmpegArgs builds the capture command. We ask ffmpeg for an mpjpeg
// (multipart-JPEG) stream on stdout, which is trivial and unambiguous to parse.
func (c *Camera) ffmpegArgs() []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}

	// Input options must come *before* -i.
	//
	// Framerate is platform-specific. macOS avfoundation defaults to ~29.97 fps
	// and outright *rejects* capture unless the requested input framerate is one
	// the device advertises (typically exactly 15 or 30), so we must pin it on
	// the input there. Windows dshow is the opposite - pinning the input
	// framerate makes it bail with "could not set video options" - so for dshow
	// (and by default v4l2) we leave the input alone and shape the rate on the
	// output with -r instead.
	if c.inputFmt == "avfoundation" && c.cfg.Framerate > 0 {
		args = append(args, "-framerate", strconv.Itoa(c.cfg.Framerate))
	}
	// Only set a resolution when the user asked for one: forcing a mode the
	// camera doesn't expose makes the capture backends bail out. It must be a
	// mode the camera actually supports.
	if c.cfg.Resolution != "" {
		args = append(args, "-video_size", c.cfg.Resolution)
	}
	args = append(args, "-f", c.inputFmt, "-i", c.deviceArg)

	// Output: motion-JPEG, video only. Pin the MJPEG encoder and a pixel format
	// the mpjpeg muxer accepts (the auto-picked yuvj422p gets rejected on some
	// cameras), so this works regardless of the camera's native pixel format.
	args = append(args, "-an", "-c:v", "mjpeg", "-pix_fmt", "yuvj420p")
	if c.cfg.Quality > 0 {
		args = append(args, "-q:v", strconv.Itoa(c.cfg.Quality))
	}
	if c.cfg.Framerate > 0 {
		args = append(args, "-r", strconv.Itoa(c.cfg.Framerate))
	}
	args = append(args, "-f", "mpjpeg", "pipe:1")
	return args
}

// Run keeps ffmpeg alive until ctx is cancelled, restarting on failure.
func (c *Camera) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		c.setError(err)
		log.Printf("ffmpeg stopped (%v); restarting in %s", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
}

func (c *Camera) runOnce(ctx context.Context) error {
	args := c.ffmpegArgs()
	log.Printf("starting: ffmpeg %s", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, c.ffmpegPath, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// Surface ffmpeg's own diagnostics (it only logs errors at our loglevel).
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start ffmpeg: %w", err)
	}
	go relayStderr(stderr)

	parseErr := c.parseMPJPEG(stdout)
	waitErr := cmd.Wait()
	if parseErr != nil && parseErr != io.EOF {
		return parseErr
	}
	return waitErr
}

func relayStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			log.Printf("ffmpeg: %s", line)
		}
	}
}

// parseMPJPEG reads ffmpeg's mpjpeg output: a repeating sequence of a boundary
// line, headers (including Content-Length), a blank line, then exactly that many
// JPEG bytes. We rely on Content-Length, so there's no fragile marker scanning.
func (c *Camera) parseMPJPEG(r io.Reader) error {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		contentLen := -1
		// Read headers until a blank line.
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return err
			}
			trimmed := strings.TrimRight(line, "\r\n")
			if trimmed == "" {
				break
			}
			if cl, ok := strings.CutPrefix(strings.ToLower(trimmed), "content-length:"); ok {
				if n, err := strconv.Atoi(strings.TrimSpace(cl)); err == nil {
					contentLen = n
				}
			}
		}
		if contentLen <= 0 {
			// Boundary line with no length yet, or stray line; keep reading.
			continue
		}
		frame := make([]byte, contentLen)
		if _, err := io.ReadFull(br, frame); err != nil {
			return err
		}
		c.publish(frame)
	}
}

// publish stores the latest frame and notifies stream subscribers. Slow
// subscribers simply miss frames rather than blocking capture.
func (c *Camera) publish(frame []byte) {
	c.mu.Lock()
	c.latest = frame
	c.haveFrame = true
	c.lastErr = ""
	c.mu.Unlock()

	c.subsMu.Lock()
	for ch := range c.subs {
		select {
		case ch <- frame:
		default:
		}
	}
	c.subsMu.Unlock()
}

func (c *Camera) setError(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	c.lastErr = err.Error()
	c.mu.Unlock()
}

// Snapshot returns the most recent JPEG frame, or false if none yet.
func (c *Camera) Snapshot() ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.haveFrame {
		return nil, false
	}
	return c.latest, true
}

// Status reports whether a frame has been seen and the last error, for /health.
func (c *Camera) Status() (haveFrame bool, lastErr string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.haveFrame, c.lastErr
}

// Subscribe registers a channel that receives every new frame until unsubscribed.
func (c *Camera) Subscribe() chan []byte {
	ch := make(chan []byte, 1)
	c.subsMu.Lock()
	c.subs[ch] = struct{}{}
	c.subsMu.Unlock()
	return ch
}

func (c *Camera) Unsubscribe(ch chan []byte) {
	c.subsMu.Lock()
	delete(c.subs, ch)
	c.subsMu.Unlock()
}

// printCameras lists discovered cameras to stdout for the --list flag.
func printCameras(ffmpegPath string) {
	cams, err := listCameras(ffmpegPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not list cameras: %v\n", err)
		return
	}
	if len(cams) == 0 {
		fmt.Println("No cameras found.")
		return
	}
	fmt.Println("Available cameras (set the ID as \"device\" in config.json):")
	for _, cam := range cams {
		if cam.ID == cam.Name {
			fmt.Printf("  - %s\n", cam.ID)
		} else {
			fmt.Printf("  - %s  (%s)\n", cam.ID, cam.Name)
		}
	}
}
