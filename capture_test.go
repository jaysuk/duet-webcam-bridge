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

func TestPlanUSB_AVFoundation_LadderNoInputSize(t *testing.T) {
	cfg := defaultConfig()
	cfg.InputFormat = "avfoundation"
	cfg.Device = "0"
	cfg.Resolution = "1280x720"
	p, err := buildPlan(cfg, "ffmpeg", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.candidates) < 2 {
		t.Fatalf("avfoundation should offer several fallback variants, got %d", len(p.candidates))
	}
	// The first variant must lead with a real device pixel format (the actual
	// fix), never pin -video_size on the input, and scale on the output instead.
	v1 := p.candidates[0]
	if !argsContainSeq(v1, "-pixel_format", "uyvy422") {
		t.Errorf("variant 1 should lead with -pixel_format uyvy422: %v", v1)
	}
	if argsContainSeq(v1, "-video_size", "1280x720") {
		t.Errorf("variant 1 must NOT pin input -video_size: %v", v1)
	}
	if !argsContainSeq(v1, "-vf", "scale=1280:720") {
		t.Errorf("variant 1 should scale on output: %v", v1)
	}
	if !argsContainSeq(v1, "-framerate", "15") {
		t.Errorf("variant 1 should pin the configured input framerate: %v", v1)
	}
	// The ladder should also include an nv12 variant and a bare (no -framerate,
	// no -pixel_format) last resort.
	var hasNV12, hasBare bool
	for _, v := range p.candidates {
		if argsContains(v, "nv12") {
			hasNV12 = true
		}
		if !argsContains(v, "-framerate") && !argsContains(v, "-pixel_format") {
			hasBare = true
		}
	}
	if !hasNV12 {
		t.Error("ladder should include an nv12 variant")
	}
	if !hasBare {
		t.Error("ladder should include a bare last-resort variant")
	}
}

func TestNormalizeCrop(t *testing.T) {
	cases := []struct {
		in, out string
		ok      bool
	}{
		{"", "", true},
		{"800:600", "800:600", true},
		{"800x600", "800:600", true},
		{"640:480:10:20", "640:480:10:20", true},
		{"640,480,10,20", "640:480:10:20", true},
		{"0:480", "", false},
		{"640:480:10", "", false},
		{"abc", "", false},
	}
	for _, c := range cases {
		got, err := normalizeCrop(c.in)
		if (err == nil) != c.ok || got != c.out {
			t.Errorf("normalizeCrop(%q) = %q,%v want %q,ok=%v", c.in, got, err, c.out, c.ok)
		}
	}
}

func TestNormalizeScale(t *testing.T) {
	cases := []struct {
		in, out string
		ok      bool
	}{
		{"", "", true},
		{"640x480", "640:480", true},
		{"640x-1", "640:-1", true},
		{"-1x720", "-1:720", true},
		{"-1x-1", "", false},
		{"640", "", false},
		{"foo", "", false},
	}
	for _, c := range cases {
		got, err := normalizeScale(c.in)
		if (err == nil) != c.ok || got != c.out {
			t.Errorf("normalizeScale(%q) = %q,%v want %q,ok=%v", c.in, got, err, c.out, c.ok)
		}
	}
}

func TestVideoFilters_CropThenScale(t *testing.T) {
	cfg := defaultConfig()
	cfg.Crop = "800:800:240:0"
	cfg.Scale = "640x480"
	vf, err := videoFilters(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if vf != "crop=800:800:240:0,scale=640:480" {
		t.Errorf("filter chain = %q", vf)
	}
}

func TestVideoFilters_ResolutionScalesOnlyWhenEnabled(t *testing.T) {
	cfg := defaultConfig()
	cfg.Resolution = "1280x720"
	// dshow/v4l2 pin the input, so resolution must NOT scale on output:
	if vf, _ := videoFilters(cfg, false); vf != "" {
		t.Errorf("resolution should not add an output scale here, got %q", vf)
	}
	// avfoundation/network can't pin input, so resolution becomes the scale:
	if vf, _ := videoFilters(cfg, true); vf != "scale=1280:720" {
		t.Errorf("resolution should scale on output, got %q", vf)
	}
	// An explicit Scale always wins:
	cfg.Scale = "640x-1"
	if vf, _ := videoFilters(cfg, true); vf != "scale=640:-1" {
		t.Errorf("explicit scale should win, got %q", vf)
	}
}

func TestUSBInputVariants_ExplicitPixelFormatSingleTry(t *testing.T) {
	cfg := defaultConfig()
	cfg.PixelFormat = "yuyv422"
	vs := usbInputVariants(cfg, "avfoundation", "FaceTime HD Camera")
	if len(vs) != 1 {
		t.Fatalf("explicit pixelFormat should give a single variant, got %d", len(vs))
	}
	if !argsContainSeq(vs[0], "-pixel_format", "yuyv422") {
		t.Errorf("should honour the user pixel format: %v", vs[0])
	}
}

func TestPlanUSB_DShow_SingleVariant(t *testing.T) {
	cfg := defaultConfig()
	cfg.InputFormat = "dshow"
	cfg.Device = "Some Camera"
	cfg.Resolution = "1280x720"
	p, err := buildPlan(cfg, "ffmpeg", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.candidates) != 1 {
		t.Fatalf("dshow should have a single variant, got %d", len(p.candidates))
	}
	args := p.candidates[0]
	if !argsContainSeq(args, "-video_size", "1280x720") {
		t.Errorf("dshow should select hardware mode via input -video_size: %v", args)
	}
	iIdx := indexOf(args, "-i")
	for i := 0; i < iIdx; i++ {
		if args[i] == "-framerate" {
			t.Errorf("dshow must not pin input -framerate: %v", args)
		}
	}
	if !argsContains(args, "video=Some Camera") {
		t.Errorf("dshow device arg missing: %v", args)
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
	full := strings.Join(p.candidates[0], " ")
	logged := strings.Join(p.logCandidates[0], " ")
	if !strings.Contains(full, "topsecret") {
		t.Errorf("args should carry the real password for ffmpeg: %v", p.candidates[0])
	}
	if strings.Contains(logged, "topsecret") {
		t.Errorf("log args must redact the password: %v", p.logCandidates[0])
	}
	if !argsContainSeq(p.candidates[0], "-rtsp_transport", "tcp") {
		t.Errorf("rtsp should default to tcp transport: %v", p.candidates[0])
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
	args := p.candidates[0]
	if !argsContain(args, "--codec", "mjpeg") {
		t.Errorf("missing mjpeg codec: %v", args)
	}
	if !argsContainSeq(args, "--width", "1280") || !argsContainSeq(args, "--height", "720") {
		t.Errorf("missing width/height: %v", args)
	}
	if !argsContainSeq(args, "--framerate", "20") {
		t.Errorf("missing framerate: %v", args)
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
