package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// chunkedReader hands out the data in small pieces to exercise the parser's
// buffering across reads (mimicking a real pipe).
type chunkedReader struct {
	data []byte
	pos  int
	n    int
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	end := r.pos + r.n
	if end > len(r.data) {
		end = len(r.data)
	}
	c := copy(p, r.data[r.pos:end])
	r.pos += c
	return c, nil
}

func jpegFrame(payload byte) []byte {
	// SOI + a few payload bytes + EOI
	return []byte{0xFF, 0xD8, payload, payload, payload, 0xFF, 0xD9}
}

func TestParseRawMJPEG_SplitsFrames(t *testing.T) {
	f1, f2, f3 := jpegFrame(0x11), jpegFrame(0x22), jpegFrame(0x33)
	var buf bytes.Buffer
	buf.Write([]byte{0x00, 0x01, 0x02}) // leading junk before first SOI
	buf.Write(f1)
	buf.Write(f2)
	buf.Write([]byte{0xFF}) // stray byte between frames
	buf.Write(f3)

	cam := &Camera{subs: make(map[chan []byte]struct{})}
	err := cam.parseRawMJPEG(&chunkedReader{data: buf.Bytes(), n: 4})
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
	got, ok := cam.Snapshot()
	if !ok {
		t.Fatal("no frame captured")
	}
	if !bytes.Equal(got, f3) {
		t.Errorf("last frame = % x, want % x", got, f3)
	}
}

func argsContain(args []string, want ...string) bool {
	joined := " " + strings.Join(args, " ") + " "
	for _, w := range want {
		if !strings.Contains(joined, " "+w+" ") {
			return false
		}
	}
	return true
}

func argsContainSeq(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}

func TestSplitResolution(t *testing.T) {
	cases := []struct {
		in         string
		w, h       int
		ok         bool
	}{
		{"1280x720", 1280, 720, true},
		{"640X480", 640, 480, true},
		{"", 0, 0, false},
		{"nonsense", 0, 0, false},
		{"1280x", 0, 0, false},
	}
	for _, c := range cases {
		w, h, ok := splitResolution(c.in)
		if ok != c.ok || w != c.w || h != c.h {
			t.Errorf("splitResolution(%q) = %d,%d,%v want %d,%d,%v", c.in, w, h, ok, c.w, c.h, c.ok)
		}
	}
}

func TestBuildNetworkURL_InjectsAndRedacts(t *testing.T) {
	full, redacted, err := buildNetworkURL("rtsp://cam.local:554/h264", "admin", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(full, "admin:s3cret@") {
		t.Errorf("full URL should embed credentials, got %q", full)
	}
	if strings.Contains(redacted, "s3cret") {
		t.Errorf("redacted URL must not contain the password, got %q", redacted)
	}
	if !strings.Contains(redacted, "admin@") {
		t.Errorf("redacted URL should keep the username, got %q", redacted)
	}
}

func TestBuildNetworkURL_StripsEmbeddedPasswordInRedaction(t *testing.T) {
	_, redacted, err := buildNetworkURL("rtsp://admin:hunter2@cam/stream", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(redacted, "hunter2") {
		t.Errorf("redacted URL leaked embedded password: %q", redacted)
	}
}

func TestBuildNetworkURL_Invalid(t *testing.T) {
	if _, _, err := buildNetworkURL("not-a-url", "", ""); err == nil {
		t.Error("expected error for URL without scheme/host")
	}
}

func TestPlanUSB_AVFoundation_NoInputSize(t *testing.T) {
	cfg := defaultConfig()
	cfg.InputFormat = "avfoundation"
	cfg.Device = "0"
	cfg.Resolution = "1280x720"
	p, err := buildPlan(cfg, "ffmpeg", "")
	if err != nil {
		t.Fatal(err)
	}
	if argsContainSeq(p.cmdArgs, "-video_size", "1280x720") {
		t.Errorf("avfoundation must NOT pin input -video_size (causes negotiation failure): %v", p.cmdArgs)
	}
	if !argsContainSeq(p.cmdArgs, "-vf", "scale=1280:720") {
		t.Errorf("avfoundation should scale on output instead: %v", p.cmdArgs)
	}
	if !argsContainSeq(p.cmdArgs, "-framerate", "15") {
		t.Errorf("avfoundation should pin input framerate: %v", p.cmdArgs)
	}
}

func TestPlanUSB_DShow_PinsSizeNotFramerateInput(t *testing.T) {
	cfg := defaultConfig()
	cfg.InputFormat = "dshow"
	cfg.Device = "Some Camera"
	cfg.Resolution = "1280x720"
	p, err := buildPlan(cfg, "ffmpeg", "")
	if err != nil {
		t.Fatal(err)
	}
	if !argsContainSeq(p.cmdArgs, "-video_size", "1280x720") {
		t.Errorf("dshow should select hardware mode via input -video_size: %v", p.cmdArgs)
	}
	// -framerate must NOT appear as an input option (before -i) for dshow.
	iIdx := indexOf(p.cmdArgs, "-i")
	for i := 0; i < iIdx; i++ {
		if p.cmdArgs[i] == "-framerate" {
			t.Errorf("dshow must not pin input -framerate: %v", p.cmdArgs)
		}
	}
	if !argsContains(p.cmdArgs, "video=Some Camera") {
		t.Errorf("dshow device arg missing: %v", p.cmdArgs)
	}
}

func TestPlanNetwork_RedactsLogArgs(t *testing.T) {
	cfg := defaultConfig()
	cfg.Source = "network"
	cfg.URL = "rtsp://cam/stream"
	cfg.Username = "admin"
	cfg.Password = "topsecret"
	p, err := buildPlan(cfg, "ffmpeg", "")
	if err != nil {
		t.Fatal(err)
	}
	full := strings.Join(p.cmdArgs, " ")
	log := strings.Join(p.logArgs, " ")
	if !strings.Contains(full, "topsecret") {
		t.Errorf("cmdArgs should carry the real password for ffmpeg: %v", p.cmdArgs)
	}
	if strings.Contains(log, "topsecret") {
		t.Errorf("logArgs must redact the password: %v", p.logArgs)
	}
	if !argsContainSeq(p.cmdArgs, "-rtsp_transport", "tcp") {
		t.Errorf("rtsp should default to tcp transport: %v", p.cmdArgs)
	}
}

func TestPlanNetwork_SnapshotPoll(t *testing.T) {
	cfg := defaultConfig()
	cfg.Source = "network"
	cfg.URL = "http://cam/snapshot.jpg"
	cfg.NetworkMode = "snapshot"
	p, err := buildPlan(cfg, "ffmpeg", "")
	if err != nil {
		t.Fatal(err)
	}
	if !p.poll {
		t.Fatal("snapshot mode should use the poller")
	}
	if p.pollURL != "http://cam/snapshot.jpg" {
		t.Errorf("poll URL = %q", p.pollURL)
	}
}

func TestPlanCSI_RpicamArgs(t *testing.T) {
	cfg := defaultConfig()
	cfg.Source = "csi"
	cfg.Resolution = "1280x720"
	cfg.Framerate = 20
	p, err := buildPlan(cfg, "", "rpicam-vid")
	if err != nil {
		t.Fatal(err)
	}
	if !p.rawMJPEG {
		t.Error("CSI capture should use the raw-MJPEG parser")
	}
	if !argsContain(p.cmdArgs, "--codec", "mjpeg") {
		t.Errorf("missing mjpeg codec: %v", p.cmdArgs)
	}
	if !argsContainSeq(p.cmdArgs, "--width", "1280") || !argsContainSeq(p.cmdArgs, "--height", "720") {
		t.Errorf("missing width/height: %v", p.cmdArgs)
	}
	if !argsContainSeq(p.cmdArgs, "--framerate", "20") {
		t.Errorf("missing framerate: %v", p.cmdArgs)
	}
}

func TestPlanCSI_NoRpicam(t *testing.T) {
	cfg := defaultConfig()
	cfg.Source = "csi"
	if _, err := buildPlan(cfg, "", ""); err == nil {
		t.Error("expected error when rpicam-vid is absent")
	}
}

func TestBuildPlan_UnknownSource(t *testing.T) {
	cfg := defaultConfig()
	cfg.Source = "telepathy"
	if _, err := buildPlan(cfg, "ffmpeg", ""); err == nil {
		t.Error("expected error for unknown source")
	}
}

// helpers
func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return len(s)
}

func argsContains(args []string, v string) bool {
	for _, a := range args {
		if a == v {
			return true
		}
	}
	return false
}
