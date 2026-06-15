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
	"time"
)

// Camera owns the capture subprocess (ffmpeg or rpicam-vid) or snapshot poller,
// keeps the most recent JPEG frame in memory, and fans new frames out to any
// connected MJPEG stream clients. It restarts the source automatically if it
// hiccups, so a transient glitch doesn't take the bridge down.
type Camera struct {
	cfg  Config
	plan capturePlan

	mu        sync.RWMutex
	latest    []byte
	haveFrame bool
	lastErr   string

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
	cmdPath      string
	cmdArgs      []string
	logArgs      []string // == cmdArgs with secrets redacted, for logging
	rawMJPEG     bool     // true => parse raw concatenated JPEGs (rpicam) not mpjpeg
	description  string   // human label for the banner ("USB camera", etc.)
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

// planUSB builds an ffmpeg command capturing a local USB/built-in camera.
func planUSB(cfg Config, ffmpegPath string) (capturePlan, error) {
	inputFmt := cfg.InputFormat
	if inputFmt == "" {
		inputFmt = defaultInputFormat()
	}
	dev, err := deviceArg(inputFmt, cfg.Device, ffmpegPath)
	if err != nil {
		return capturePlan{}, err
	}

	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}

	// Input framerate / size are platform-specific minefields:
	//   - macOS avfoundation: pinning -video_size triggers a format-negotiation
	//     failure ("Selected framerate is not supported by the device") even for
	//     listed modes, so we DON'T constrain the input size there and scale on
	//     the output instead. We still pass -framerate (a listed value) because
	//     avfoundation otherwise defaults to 29.97 and rejects that.
	//   - Windows dshow: pinning -framerate makes it bail ("could not set video
	//     options"), so we leave the input rate alone and shape it with -r.
	//   - Linux v4l2: pinning -video_size selects the hardware mode; fine.
	switch inputFmt {
	case "avfoundation":
		if cfg.Framerate > 0 {
			args = append(args, "-framerate", strconv.Itoa(cfg.Framerate))
		}
	default:
		if cfg.Resolution != "" {
			args = append(args, "-video_size", cfg.Resolution)
		}
	}
	if cfg.PixelFormat != "" {
		args = append(args, "-pixel_format", cfg.PixelFormat)
	}
	args = append(args, "-f", inputFmt, "-i", dev)

	// Output: motion-JPEG. On avfoundation we couldn't pin the size on the input,
	// so apply the requested resolution as an output scale (always safe).
	args = append(args, encodeArgs(cfg, inputFmt == "avfoundation")...)

	return capturePlan{
		cmdPath:     ffmpegPath,
		cmdArgs:     args,
		logArgs:     args, // no secrets in a local capture
		description: "USB / built-in camera",
	}, nil
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

	// Snapshot mode: just poll a single-JPEG endpoint and re-serve it. No
	// transcoding, so it's cheap - ideal when the camera already offers an
	// (authenticated) still-image URL.
	if strings.EqualFold(cfg.NetworkMode, "snapshot") {
		interval := time.Duration(cfg.SnapshotInterval) * time.Millisecond
		if interval <= 0 {
			interval = time.Second
		}
		return capturePlan{
			poll:         true,
			pollURL:      cfg.URL, // creds passed separately to avoid logging them
			pollUser:     cfg.Username,
			pollPass:     cfg.Password,
			pollInterval: interval,
			description:  "network camera (snapshot poll: " + redactedURL + ")",
		}, nil
	}

	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	logArgs := append([]string(nil), args...)
	if strings.HasPrefix(strings.ToLower(cfg.URL), "rtsp") && cfg.RTSPTransport != "" {
		args = append(args, "-rtsp_transport", cfg.RTSPTransport)
		logArgs = append(logArgs, "-rtsp_transport", cfg.RTSPTransport)
	}
	args = append(args, "-i", fullURL)
	logArgs = append(logArgs, "-i", redactedURL)

	enc := encodeArgs(cfg, true) // scale on output for network sources
	args = append(args, enc...)
	logArgs = append(logArgs, enc...)

	return capturePlan{
		cmdPath:     ffmpegPath,
		cmdArgs:     args,
		logArgs:     logArgs,
		description: "network camera (" + redactedURL + ")",
	}, nil
}

// planCSI builds an rpicam-vid command for a Raspberry Pi CSI camera.
func planCSI(cfg Config, rpicamPath string) (capturePlan, error) {
	if rpicamPath == "" {
		return capturePlan{}, fmt.Errorf("source \"csi\" needs rpicam-vid (install rpicam-apps on Raspberry Pi OS)")
	}
	// -t 0 = run forever; mjpeg codec to stdout; no preview window.
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
		cmdPath:     rpicamPath,
		cmdArgs:     args,
		logArgs:     args,
		rawMJPEG:    true,
		description: "Raspberry Pi CSI camera",
	}, nil
}

// encodeArgs builds the shared ffmpeg output args (MJPEG over mpjpeg on stdout).
// If scaleOutput is true and a resolution is configured, it's applied as an
// output scale filter rather than an input constraint.
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
// failure with a backoff. Done() is closed when it returns.
func (c *Camera) Run(ctx context.Context) {
	defer close(c.done)
	if c.plan.poll {
		c.runPoller(ctx)
		return
	}
	backoff := time.Second
	for ctx.Err() == nil {
		err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		c.setError(err)
		log.Printf("capture stopped (%v); restarting in %s", err, backoff)
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
	log.Printf("starting: %s %s", baseName(c.plan.cmdPath), strings.Join(c.plan.logArgs, " "))
	cmd := exec.CommandContext(ctx, c.plan.cmdPath, c.plan.cmdArgs...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start %s: %w", baseName(c.plan.cmdPath), err)
	}
	go relayStderr(stderr, baseName(c.plan.cmdPath))

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
	fetch() // prime immediately
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fetch()
		}
	}
}

func relayStderr(r io.Reader, name string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			log.Printf("%s: %s", name, line)
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
						if buf.Len() > 1 { // keep only a possible trailing 0xFF
							buf.Next(buf.Len() - 1)
						}
						break
					}
					buf.Next(i) // drop everything before SOI
					started = true
					data = buf.Bytes()
				}
				// look for EOI after the SOI
				j := bytes.Index(data[2:], jpegEOI)
				if j < 0 {
					break
				}
				end := 2 + j + 2 // include the EOI marker
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
