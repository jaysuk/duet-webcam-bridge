package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
)

// Server exposes the camera over HTTP. The endpoints are deliberately
// mjpg-streamer-compatible so existing Duet guides "just work":
//
//	/snapshot          single JPEG (use with a DWC update interval)
//	/stream            live MJPEG    (use with DWC update interval = 0)
//	/?action=snapshot  alias for /snapshot
//	/?action=stream    alias for /stream
//	/health            JSON status
//	/                  help page (or routes the ?action= aliases)
type Server struct {
	cam *Camera
	cfg Config
}

func NewServer(cam *Camera, cfg Config) *Server {
	return &Server{cam: cam, cfg: cfg}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/snapshot", s.handleSnapshot)
	mux.HandleFunc("/stream", s.handleStream)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/", s.handleRoot)
	return mux
}

// handleRoot serves the help page, but also honours the mjpg-streamer
// ?action=snapshot / ?action=stream query style at the root path.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	switch r.URL.Query().Get("action") {
	case "snapshot":
		s.handleSnapshot(w, r)
		return
	case "stream":
		s.handleStream(w, r)
		return
	}
	s.handleHelp(w, r)
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	frame, ok := s.cam.Snapshot()
	if !ok {
		http.Error(w, "no frame captured yet", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Content-Length", fmt.Sprint(len(frame)))
	_, _ = w.Write(frame)
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	const boundary = "frame"
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+boundary)
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Connection", "close")

	ch := s.cam.Subscribe()
	defer s.cam.Unsubscribe(ch)

	// Send the current frame immediately so the viewer isn't blank until the
	// next capture tick.
	if frame, ok := s.cam.Snapshot(); ok {
		if err := writePart(w, boundary, frame); err != nil {
			return
		}
		flusher.Flush()
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case frame := <-ch:
			if err := writePart(w, boundary, frame); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writePart(w http.ResponseWriter, boundary string, frame []byte) error {
	if _, err := fmt.Fprintf(w, "--%s\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", boundary, len(frame)); err != nil {
		return err
	}
	if _, err := w.Write(frame); err != nil {
		return err
	}
	_, err := w.Write([]byte("\r\n"))
	return err
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	haveFrame, lastErr := s.cam.Status()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"version":   version,
		"device":    s.cfg.Device,
		"haveFrame": haveFrame,
		"lastError": lastErr,
	})
}

var helpTmpl = template.Must(template.New("help").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Duet Webcam Bridge</title>
<style>
 body{font-family:system-ui,Segoe UI,Roboto,sans-serif;max-width:46rem;margin:2rem auto;padding:0 1rem;line-height:1.5;color:#222}
 h1{font-size:1.4rem} code{background:#f2f2f2;padding:.1rem .35rem;border-radius:.25rem}
 .urls a{display:inline-block;margin:.15rem 0}
 img{max-width:100%;border:1px solid #ddd;border-radius:.4rem;margin-top:1rem}
 table{border-collapse:collapse;margin-top:.5rem} td{padding:.15rem .6rem .15rem 0}
</style>
</head>
<body>
<h1>Duet Webcam Bridge <small>v{{.Version}}</small></h1>
<p>This little server is publishing your USB camera so Duet Web Control can show it.</p>
<h2>Add it to DWC</h2>
<p>In DWC open <strong>Settings &rarr; Webcam</strong> and use one of:</p>
<table>
<tr><td><strong>Live video (recommended)</strong></td><td><code>{{.StreamURL}}</code> &nbsp; and set <em>Update interval</em> to <code>0</code></td></tr>
<tr><td><strong>Polled image</strong></td><td><code>{{.SnapshotURL}}</code> &nbsp; with an update interval (e.g. 1000&nbsp;ms)</td></tr>
</table>
<p>If DWC and this PC are the same machine you can use <code>localhost</code> instead of the IP.</p>
<h2>Preview</h2>
<div class="urls">
<a href="/stream">/stream</a> &middot; <a href="/snapshot">/snapshot</a> &middot; <a href="/health">/health</a>
</div>
<img src="/stream" alt="camera preview">
</body>
</html>`))

func (s *Server) handleHelp(w http.ResponseWriter, r *http.Request) {
	host := r.Host // includes the port the user actually reached us on
	data := map[string]string{
		"Version":     version,
		"StreamURL":   "http://" + host + "/stream",
		"SnapshotURL": "http://" + host + "/snapshot",
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := helpTmpl.Execute(w, data); err != nil {
		log.Printf("rendering help page: %v", err)
	}
}
