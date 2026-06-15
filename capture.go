package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Camera owns the capture subprocess (ffmpeg or rpicam-vid) or snapshot poller,
// keeps the most recent JPEG frame in memory, and fans new frames out to any
// connected MJPEG stream clients. It restarts the source automatically if it
// hiccups, so a transient glitch doesn't take the bridge down.
//
// For sources whose capture settings are fiddly (notably macOS avfoundation),
// the plan can carry several candidate argument-sets; if one runs without ever
// producing a frame, the next is tried on the following restart until one works.
type Camera struct {
	cfg  Config
	plan capturePlan

	frames atomic.Uint64 // total frames ever published (for "did this run work?")

	mu        sync.RWMutex
	latest    []byte
	haveFrame bool
	lastErr   string
	recentLog []string // last few stderr lines from ffmpeg/rpicam, for diagnostics
	variant   int      // index of the candidate args currently in use

	subsMu sync.Mutex
	subs   map[chan []byte]struct{}

	done chan struct{} // closed when Run returns (camera retired)
}

// capturePlan is the resolved "how to get frames" decision, built once up front.
type capturePlan struct {
	// poll: pull single JPEGs over HTTP instead of running a subprocess.
	poll         bool
	pollURL      string
	pollUser     string
	pollPass     string
	pollInterval time.Duration

	// subprocess capture (ffmpeg or rpicam-vid):
	cmdPath       string
	candidates    [][]string // argument-sets to try in order
	logCandidates [][]string // parallel to candidates, with secrets redacted
	rawMJPEG      bool       // true => parse raw concatenated JPEGs (rpicam) not mpjpeg
	description   string     // human label for the banner ("USB camera", etc.)
}

func NewCamera(cfg Config, ffmpegPath, rpicamPath string) (*Camera, error) {
	plan, err := buildPlan(cfg, ffmpegPath, rpicamPath)
	if err != nil {
		return nil, err
	}
	return &Camera{
		cfg:  cfg,
		plan: plan,
		subs: make(map[chan []byte]struct{}),
		done: make(chan struct{}),
	}, nil
}

// buildPlan turns the config into a concrete capture plan for the chosen source.
func buildPlan(cfg Config, ffmpegPath, rpicamPath string) (capturePlan, error) {
	switch strings.ToLower(cfg.Source) {
	case "", "usb", "auto":
		return planUSB(cfg, ffmpegPath)
	case "network", "ip":
		return planNetwork(cfg, ffmpegPath)
	case "csi", "libcamera", "rpicam":
		return planCSI(cfg, rpicamPath)
	default:
		return capturePlan{}, fmt.Errorf("unknown source %q (use usb, csi or network)", cfg.Source)
	}
}

// ffLogLevel is the ffmpeg -loglevel; configurable for diagnostics.
func ffLogLevel(cfg Config) string {
	if cfg.LogLevel != "" {
		return cfg.LogLevel
	}
	return "error"
}

// planUSB builds ffmpeg command(s) capturing a local USB/built-in camera.
func planUSB(cfg Config, ffmpegPath string) (capturePlan, error) {
	inputFmt := cfg.InputFormat
	if inputFmt == "" {
		inputFmt = defaultInputFormat()
	}
	dev, err := deviceArg(inputFmt, cfg.Device, ffmpegPath)
	if err != nil {
		return capturePlan{}, err
	}

	prefix := []string{"-hide_banner", "-loglevel", ffLogLevel(cfg), "-nostdin"}
	// avfoundation needs output-side scaling (we can't reliably pin input size);
	// dshow/v4l2 select the hardware mode on the input.
	out := encodeArgs(cfg, inputFmt == "avfoundation")

	inputVariants := usbInputVariants(cfg, inputFmt, dev)
	var cands [][]string
	for _, in := range inputVariants {
		full := append([]string{}, prefix...)
		full = append(full, in...)
		full = append(full, out...)
		cands = append(cands, full)
	}
	return capturePlan{
		cmdPath:       ffmpegPath,
		candidates:    cands,
		logCandidates: cands, // no secrets in a local capture
		description:   "USB / built-in camera",
	}, nil
}

// usbInputVariants returns the ffmpeg input-argument set(s) to try (everything
// up to and including "-i <device>"). For dshow/v4l2 there's a single, reliable
// set. For macOS avfoundation we return a ladder of combinations, because which
// one a given camera accepts varies wildly - some need an exact size+fps mode,
// others a specific pixel format. The capture loop advances through them until
// one actually produces frames.
func usbInputVariants(cfg Config, inputFmt, dev string) [][]string {
	if inputFmt != "avfoundation" {
		in := []string{}
		if cfg.Resolution != "" {
			in = append(in, "-video_size", cfg.Resolution)
		}
		if cfg.PixelFormat != "" {
			in = append(in, "-pixel_format", cfg.PixelFormat)
		}
		in = append(in, "-f", inputFmt, "-i", dev)
		return [][]string{in}
	}

	fr := ""
	if cfg.Framerate > 0 {
		fr = strconv.Itoa(cfg.Framerate)
	}
	tail := []string{"-f", "avfoundation", "-i", dev}
	frArgs := func(f string) []string {
		if f == "" {
			return nil
		}
		return []string{"-framerate", f}
	}

	var variants [][]string
	add := func(parts ...string) { variants = append(variants, append(parts, tail...)) }

	// If the user explicitly set a pixel format, honour it exactly (single try).
	if cfg.PixelFormat != "" {
		add(append(frArgs(fr), "-pixel_format", cfg.PixelFormat)...)
		return variants
	}

	// Mac cameras typically DON'T offer ffmpeg's default yuv420p - they offer
	// uyvy422 / nv12 / yuyv422. Specifying the wrong (or no) pixel format makes
	// ffmpeg bail with "Selected pixel format ... is not supported" (and the
	// adjacent "framerate not supported" error is often the same root cause). So
	// we lead with the real device pixel formats, then vary the framerate, and
	// only fall back to letting ffmpeg choose as a last resort.
	pixfmts := []string{"uyvy422", "nv12", "yuyv422"}
	for _, pf := range pixfmts { // configured framerate + supported pixfmt
		add(append(frArgs(fr), "-pixel_format", pf)...)
	}
	if fr != "30" { // some cameras only do exactly 15/30
		add("-framerate", "30", "-pixel_format", "uyvy422")
		add("-framerate", "30", "-pixel_format", "nv12")
	}
	add("-pixel_format", "uyvy422") // no framerate at all
	add("-pixel_format", "nv12")
	add(frArgs(fr)...) // no pixel format (camera that does support the default)
	add()              // bare
	return variants
}

// planNetwork builds an ffmpeg command (or snapshot poller) for an IP camera.
func planNetwork(cfg Config, ffmpegPath string) (capturePlan, error) {
	if cfg.URL == "" {
		return capturePlan{}, fmt.Errorf("source \"network\" needs a \"url\" (e.g. rtsp://camera/stream) in config.json")
	}
	fullURL, redactedURL, err := buildNetworkURL(cfg.URL, cfg.Username, cfg.Password)
	if err != nil {
		return capturePlan{}, err
	}

	if strings.EqualFold(cfg.NetworkMode, "snapshot") {
		interval := time.Duration(cfg.SnapshotInterval) * time.Millisecond
		if interval <= 0 {
			interval = time.Second
		}
		return capturePlan{
			poll:         true,
			pollURL:      cfg.URL,
			pollUser:     cfg.Username,
			pollPass:     cfg.Password,
			pollInterval: interval,
			description:  "network camera (snapshot poll: " + redactedURL + ")",
		}, nil
	}

	args := []string{"-hide_banner", "-loglevel", ffLogLevel(cfg), "-nostdin"}
	logArgs := append([]string(nil), args...)
	if strings.HasPrefix(strings.ToLower(cfg.URL), "rtsp") && cfg.RTSPTransport != "" {
		args = append(args, "-rtsp_transport", cfg.RTSPTransport)
		logArgs = append(logArgs, "-rtsp_transport", cfg.RTSPTransport)
	}
	args = append(args, "-i", fullURL)
	logArgs = append(logArgs, "-i", redactedURL)

	enc := encodeArgs(cfg, true)
	args = append(args, enc...)
	logArgs = append(logArgs, enc...)

	return capturePlan{
		cmdPath:       ffmpegPath,
		candidates:    [][]string{args},
		logCandidates: [][]string{logArgs},
		description:   "network camera (" + redactedURL + ")",
	}, nil
}

// planCSI builds an rpicam-vid command for a Raspberry Pi CSI camera.
func planCSI(cfg Config, rpicamPath string) (capturePlan, error) {
	if rpicamPath == "" {
		return capturePlan{}, fmt.Errorf("source \"csi\" needs rpicam-vid (install rpicam-apps on Raspberry Pi OS)")
	}
	args := []string{"-t", "0", "--codec", "mjpeg", "--nopreview", "--flush", "-o", "-"}
	if cfg.Device != "" {
		args = append(args, "--camera", cfg.Device)
	}
	if cfg.Framerate > 0 {
		args = append(args, "--framerate", strconv.Itoa(cfg.Framerate))
	}
	if w, h, ok := splitResolution(cfg.Resolution); ok {
		args = append(args, "--width", strconv.Itoa(w), "--height", strconv.Itoa(h))
	}
	return capturePlan{
		cmdPath:       rpicamPath,
		candidates:    [][]string{args},
		logCandidates: [][]string{args},
		rawMJPEG:      true,
		description:   "Raspberry Pi CSI camera",
	}, nil
}

// encodeArgs builds the shared ffmpeg output args (MJPEG over mpjpeg on stdout).
func encodeArgs(cfg Config, scaleOutput bool) []string {
	args := []string{"-an", "-c:v", "mjpeg", "-pix_fmt", "yuvj420p"}
	if cfg.Quality > 0 {
		args = append(args, "-q:v", strconv.Itoa(cfg.Quality))
	}
	if cfg.Framerate > 0 {
		args = append(args, "-r", strconv.Itoa(cfg.Framerate))
	}
	if scaleOutput {
		if w, h, ok := splitResolution(cfg.Resolution); ok {
			args = append(args, "-vf", fmt.Sprintf("scale=%d:%d", w, h))
		}
	}
	args = append(args, "-f", "mpjpeg", "pipe:1")
	return args
}

// Run keeps the capture source alive until ctx is cancelled, restarting on
// failure with a backoff and rotating through candidate arg-sets that don't
// produce frames. Done() is closed when it returns.
func (c *Camera) Run(ctx context.Context) {
	defer close(c.done)
	if c.plan.poll {
		c.runPoller(ctx)
		return
	}

	n := len(c.plan.candidates)
	idx := 0
	delay := time.Second
	frameless := 0
	for ctx.Err() == nil {
		c.setVariant(idx)
		before := c.frames.Load()
		err := c.runOnce(ctx, c.plan.candidates[idx], c.plan.logCandidates[idx])
		if ctx.Err() != nil {
			return
		}
		c.setError(err)

		if c.frames.Load() > before {
			// Healthy: keep this variant and reset the probing state.
			frameless = 0
			delay = time.Second
		} else {
			frameless++
			if n > 1 {
				idx = (idx + 1) % n
				log.Printf("no frames with these camera settings; trying alternative %d/%d", idx+1, n)
			}
			// Probe candidates quickly; only start backing off once we've cycled
			// through them all without success, to avoid hammering a dead source.
			if frameless >= 2*n && delay < 15*time.Second {
				delay *= 2
			}
		}
		if sleepCtx(ctx, delay) {
			return
		}
	}
}

func (c *Camera) runOnce(ctx context.Context, args, logArgs []string) error {
	name := baseName(c.plan.cmdPath)
	log.Printf("starting: %s %s", name, strings.Join(logArgs, " "))
	cmd := exec.CommandContext(ctx, c.plan.cmdPath, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start %s: %w", name, err)
	}
	go c.relayStderr(stderr, name)

	var parseErr error
	if c.plan.rawMJPEG {
		parseErr = c.parseRawMJPEG(stdout)
	} else {
		parseErr = c.parseMPJPEG(stdout)
	}
	waitErr := cmd.Wait()
	if parseErr != nil && parseErr != io.EOF {
		return parseErr
	}
	return waitErr
}

// runPoller fetches a JPEG from the snapshot URL on an interval (network
// snapshot mode), with optional HTTP basic auth.
func (c *Camera) runPoller(ctx context.Context) {
	client := &http.Client{Timeout: 10 * time.Second}
	ticker := time.NewTicker(c.plan.pollInterval)
	defer ticker.Stop()
	fetch := func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.plan.pollURL, nil)
		if err != nil {
			c.setError(err)
			return
		}
		if c.plan.pollUser != "" || c.plan.pollPass != "" {
			req.SetBasicAuth(c.plan.pollUser, c.plan.pollPass)
		}
		resp, err := client.Do(req)
		if err != nil {
			c.setError(err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			c.setError(fmt.Errorf("snapshot HTTP %d", resp.StatusCode))
			return
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		if err != nil {
			c.setError(err)
			return
		}
		c.publish(body)
	}
	fetch()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fetch()
		}
	}
}

func (c *Camera) relayStderr(r io.Reader, name string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		log.Printf("%s: %s", name, line)
		c.mu.Lock()
		c.recentLog = append(c.recentLog, line)
		if len(c.recentLog) > 30 {
			c.recentLog = c.recentLog[len(c.recentLog)-30:]
		}
		c.mu.Unlock()
	}
}

// parseMPJPEG reads ffmpeg's mpjpeg output: a repeating sequence of a boundary
// line, headers (including Content-Length), a blank line, then exactly that many
// JPEG bytes. We rely on Content-Length, so there's no fragile marker scanning.
func (c *Camera) parseMPJPEG(r io.Reader) error {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		contentLen := -1
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
			continue
		}
		frame := make([]byte, contentLen)
		if _, err := io.ReadFull(br, frame); err != nil {
			return err
		}
		c.publish(frame)
	}
}

// JPEG start/end-of-image markers.
var (
	jpegSOI = []byte{0xFF, 0xD8}
	jpegEOI = []byte{0xFF, 0xD9}
)

// parseRawMJPEG reads a stream of concatenated JPEGs (rpicam-vid --codec mjpeg),
// splitting on the SOI/EOI markers. Good enough for camera MJPEG, which has no
// embedded thumbnails.
func (c *Camera) parseRawMJPEG(r io.Reader) error {
	br := bufio.NewReaderSize(r, 1<<20)
	var buf bytes.Buffer
	started := false
	tmp := make([]byte, 32*1024)
	for {
		n, err := br.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
			for {
				data := buf.Bytes()
				if !started {
					i := bytes.Index(data, jpegSOI)
					if i < 0 {
						if buf.Len() > 1 {
							buf.Next(buf.Len() - 1)
						}
						break
					}
					buf.Next(i)
					started = true
					data = buf.Bytes()
				}
				j := bytes.Index(data[2:], jpegEOI)
				if j < 0 {
					break
				}
				end := 2 + j + 2
				frame := make([]byte, end)
				copy(frame, data[:end])
				c.publish(frame)
				buf.Next(end)
				started = false
			}
		}
		if err != nil {
			return err
		}
	}
}

// publish stores the latest frame and notifies stream subscribers. Slow
// subscribers simply miss frames rather than blocking capture.
func (c *Camera) publish(frame []byte) {
	c.frames.Add(1)
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

func (c *Camera) setVariant(i int) {
	c.mu.Lock()
	c.variant = i
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

// Status reports capture health for /health and /config diagnostics.
func (c *Camera) Status() (haveFrame bool, lastErr string, recentLog []string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.haveFrame, c.lastErr, append([]string(nil), c.recentLog...)
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

// Description is a human-readable label for the active capture source.
func (c *Camera) Description() string { return c.plan.description }

// Done is closed once the camera has been retired (its context cancelled), so
// stream handlers can stop instead of blocking on a camera that's been replaced.
func (c *Camera) Done() <-chan struct{} { return c.done }

// printCameras lists discovered cameras to stdout for the --list flag.
func printCameras(cfg Config, ffmpegPath, rpicamPath string) {
	if strings.EqualFold(cfg.Source, "csi") {
		listCSICameras(rpicamPath)
		return
	}
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
			fmt.Printf("  - device %q  (%s)\n", cam.ID, cam.Name)
		}
	}
}
