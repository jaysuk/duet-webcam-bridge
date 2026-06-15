package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// handler builds the HTTP routes. The endpoints are deliberately
// mjpg-streamer-compatible so existing Duet guides "just work":
//
//	/snapshot          single JPEG (use with a DWC update interval)
//	/stream            live MJPEG    (use with DWC update interval = 0)
//	/?action=snapshot  alias for /snapshot
//	/?action=stream    alias for /stream
//	/health            JSON status
//	/config            settings page (GET form, POST to save+apply)
//	/                  help page (or routes the ?action= aliases)
func (a *App) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/snapshot", a.handleSnapshot)
	mux.HandleFunc("/stream", a.handleStream)
	mux.HandleFunc("/health", a.handleHealth)
	mux.HandleFunc("/config", a.handleConfig)
	mux.HandleFunc("/", a.handleRoot)
	return mux
}

// handleRoot serves the help page, but also honours the mjpg-streamer
// ?action=snapshot / ?action=stream query style at the root path.
func (a *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	switch r.URL.Query().Get("action") {
	case "snapshot":
		a.handleSnapshot(w, r)
		return
	case "stream":
		a.handleStream(w, r)
		return
	}
	a.handleHelp(w, r)
}

func (a *App) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	cam := a.currentCam()
	if cam == nil {
		http.Error(w, "camera not running - see /config", http.StatusServiceUnavailable)
		return
	}
	frame, ok := cam.Snapshot()
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

func (a *App) handleStream(w http.ResponseWriter, r *http.Request) {
	cam := a.currentCam()
	if cam == nil {
		http.Error(w, "camera not running - see /config", http.StatusServiceUnavailable)
		return
	}
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

	ch := cam.Subscribe()
	defer cam.Unsubscribe(ch)

	if frame, ok := cam.Snapshot(); ok {
		if err := writePart(w, boundary, frame); err != nil {
			return
		}
		flusher.Flush()
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-cam.Done(): // camera was reloaded/retired; end this stream
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

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	haveFrame, lastErr := false, "camera not running"
	var recent []string
	if cam := a.currentCam(); cam != nil {
		haveFrame, lastErr, recent = cam.Status()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"version":   version,
		"source":    cfg.Source,
		"device":    cfg.Device,
		"haveFrame": haveFrame,
		"lastError": lastErr,
		"log":       recent,
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
 .btn{display:inline-block;background:#1976d2;color:#fff;padding:.4rem .8rem;border-radius:.3rem;text-decoration:none}
</style>
</head>
<body>
<h1>Duet Webcam Bridge <small>v{{.Version}}</small></h1>
<p>This little server is publishing your camera so Duet Web Control can show it.</p>
<p><a class="btn" href="/config">Settings</a></p>
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

func (a *App) handleHelp(w http.ResponseWriter, r *http.Request) {
	host := r.Host
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

// handleConfig serves the settings form (GET) and applies it (POST).
func (a *App) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		a.handleConfigSave(w, r)
		return
	}
	a.renderConfig(w, a.config(), "", "")
}

func (a *App) handleConfigSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		a.renderConfig(w, a.config(), "", "Could not read the form: "+err.Error())
		return
	}
	newCfg := parseConfigForm(r, a.config())

	addrChanged, err := a.Apply(newCfg)
	if err != nil {
		// Show the (saved) values back with the error.
		a.renderConfig(w, newCfg, "", err.Error())
		return
	}

	if addrChanged {
		// Respond first, then move the listener so this response still goes out.
		newURL := fmt.Sprintf("http://%s:%d/config", firstHost(r), newCfg.Port)
		a.renderConfig(w, newCfg, "Saved. The port changed - this page is moving to "+newURL, "")
		go func() {
			time.Sleep(400 * time.Millisecond)
			a.triggerRebind()
		}()
		return
	}
	a.renderConfig(w, newCfg, "Saved and applied.", "")
}

// parseConfigForm builds a Config from the posted form, starting from current so
// unspecified fields are preserved. A blank password keeps the existing one (so
// the stored password is never echoed to the page).
func parseConfigForm(r *http.Request, current Config) Config {
	c := current
	c.Bind = strings.TrimSpace(formOr(r, "bind", c.Bind))
	c.Port = atoiOr(r.FormValue("port"), c.Port)
	c.Source = strings.TrimSpace(formOr(r, "source", c.Source))
	c.Device = strings.TrimSpace(r.FormValue("device"))
	c.Resolution = strings.TrimSpace(r.FormValue("resolution"))
	c.Framerate = atoiOr(r.FormValue("framerate"), c.Framerate)
	c.Quality = atoiOr(r.FormValue("quality"), c.Quality)
	c.PixelFormat = strings.TrimSpace(r.FormValue("pixelFormat"))
	c.URL = strings.TrimSpace(r.FormValue("url"))
	c.Username = strings.TrimSpace(r.FormValue("username"))
	if pw := r.FormValue("password"); pw != "" {
		c.Password = pw
	}
	c.RTSPTransport = strings.TrimSpace(formOr(r, "rtspTransport", c.RTSPTransport))
	c.NetworkMode = strings.TrimSpace(formOr(r, "networkMode", c.NetworkMode))
	c.SnapshotInterval = atoiOr(r.FormValue("snapshotInterval"), c.SnapshotInterval)
	c.LogLevel = strings.TrimSpace(r.FormValue("logLevel"))
	return c
}

func formOr(r *http.Request, key, fallback string) string {
	if v := r.FormValue(key); v != "" {
		return v
	}
	return fallback
}

func atoiOr(s string, fallback int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return fallback
}

func firstHost(r *http.Request) string {
	host := r.Host
	if i := strings.LastIndex(host, ":"); i != -1 {
		host = host[:i]
	}
	return host
}

func (a *App) renderConfig(w http.ResponseWriter, cfg Config, ok, errMsg string) {
	var haveFrame bool
	var camErr string
	var recent []string
	if cam := a.currentCam(); cam != nil {
		haveFrame, camErr, recent = cam.Status()
	} else {
		camErr = "camera not running"
	}
	data := struct {
		Version     string
		Cfg         Config
		HasPassword bool
		OK          string
		Err         string
		HaveFrame   bool
		CamErr      string
		Log         []string
	}{
		Version:     version,
		Cfg:         cfg,
		HasPassword: cfg.Password != "",
		OK:          ok,
		Err:         errMsg,
		HaveFrame:   haveFrame,
		CamErr:      camErr,
		Log:         recent,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := configTmpl.Execute(w, data); err != nil {
		log.Printf("rendering config page: %v", err)
	}
}

var configTmpl = template.Must(template.New("config").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Settings &middot; Duet Webcam Bridge</title>
<style>
 body{font-family:system-ui,Segoe UI,Roboto,sans-serif;max-width:42rem;margin:1.5rem auto;padding:0 1rem;line-height:1.5;color:#222}
 h1{font-size:1.3rem} h2{font-size:1rem;margin:1.4rem 0 .3rem;color:#555}
 label{display:block;margin:.6rem 0 .15rem;font-weight:600;font-size:.9rem}
 input,select{width:100%;padding:.4rem;border:1px solid #bbb;border-radius:.3rem;font-size:1rem;box-sizing:border-box}
 .hint{font-weight:400;color:#777;font-size:.8rem}
 .row{display:flex;gap:1rem} .row>div{flex:1}
 .btn{margin-top:1.2rem;background:#1976d2;color:#fff;padding:.55rem 1rem;border:0;border-radius:.3rem;font-size:1rem;cursor:pointer}
 .ok{background:#e6f4ea;border:1px solid #34a853;padding:.5rem .7rem;border-radius:.3rem;margin:.5rem 0}
 .err{background:#fce8e6;border:1px solid #ea4335;padding:.5rem .7rem;border-radius:.3rem;margin:.5rem 0}
 .diag{background:#fff8e1;border:1px solid #f9ab00;padding:.5rem .7rem;border-radius:.3rem;margin:.5rem 0}
 pre{background:#111;color:#ddd;padding:.6rem;border-radius:.3rem;overflow:auto;font-size:.78rem;max-height:14rem}
 a{color:#1976d2}
</style>
</head>
<body>
<h1>Settings <small>v{{.Version}}</small></h1>
<p><a href="/">&larr; back</a> &middot; <a href="/stream">preview</a></p>
{{if .OK}}<div class="ok">{{.OK}}</div>{{end}}
{{if .Err}}<div class="err">{{.Err}}</div>{{end}}
{{if .HaveFrame}}<div class="ok">Camera is running — frames are flowing.</div>
{{else}}<div class="diag"><strong>Camera not producing frames yet.</strong>{{if .CamErr}} Last error: {{.CamErr}}{{end}}
{{if .Log}}<br>Recent capture output (set Log level to <code>verbose</code> for more):<pre>{{range .Log}}{{.}}
{{end}}</pre>{{end}}</div>{{end}}
<form method="post" action="/config">
  <h2>Camera source</h2>
  <label>Source
    <select name="source">
      <option value="usb"{{if eq .Cfg.Source "usb"}} selected{{end}}>USB / built-in camera</option>
      <option value="network"{{if eq .Cfg.Source "network"}} selected{{end}}>Network / IP camera</option>
      <option value="csi"{{if eq .Cfg.Source "csi"}} selected{{end}}>Raspberry Pi CSI camera</option>
    </select>
  </label>

  <label>Device <span class="hint">USB camera name/index or Pi CSI index. Blank = first found.</span>
    <input name="device" value="{{.Cfg.Device}}"></label>

  <h2>Network / IP camera <span class="hint">(only used when source is Network)</span></h2>
  <label>URL <span class="hint">e.g. rtsp://192.168.1.20:554/stream</span>
    <input name="url" value="{{.Cfg.URL}}"></label>
  <div class="row">
    <div><label>Username<input name="username" value="{{.Cfg.Username}}"></label></div>
    <div><label>Password <span class="hint">{{if .HasPassword}}leave blank to keep{{end}}</span>
      <input name="password" type="password" placeholder="{{if .HasPassword}}••••••••{{end}}"></label></div>
  </div>
  <div class="row">
    <div><label>RTSP transport
      <select name="rtspTransport">
        <option value="tcp"{{if eq .Cfg.RTSPTransport "tcp"}} selected{{end}}>tcp (reliable)</option>
        <option value="udp"{{if eq .Cfg.RTSPTransport "udp"}} selected{{end}}>udp</option>
      </select></label></div>
    <div><label>Mode
      <select name="networkMode">
        <option value="stream"{{if eq .Cfg.NetworkMode "stream"}} selected{{end}}>stream (transcode)</option>
        <option value="snapshot"{{if eq .Cfg.NetworkMode "snapshot"}} selected{{end}}>snapshot (poll JPEG)</option>
      </select></label></div>
  </div>

  <h2>Picture</h2>
  <div class="row">
    <div><label>Resolution <span class="hint">e.g. 1280x720; blank = native</span>
      <input name="resolution" value="{{.Cfg.Resolution}}"></label></div>
    <div><label>Framerate<input name="framerate" value="{{.Cfg.Framerate}}"></label></div>
  </div>
  <div class="row">
    <div><label>Quality <span class="hint">2 best .. 31 worst</span>
      <input name="quality" value="{{.Cfg.Quality}}"></label></div>
    <div><label>Pixel format <span class="hint">advanced; blank = auto</span>
      <input name="pixelFormat" value="{{.Cfg.PixelFormat}}"></label></div>
  </div>

  <h2>Server</h2>
  <div class="row">
    <div><label>Port<input name="port" value="{{.Cfg.Port}}"></label></div>
    <div><label>Bind <span class="hint">0.0.0.0 = all</span>
      <input name="bind" value="{{.Cfg.Bind}}"></label></div>
  </div>
  <div class="row">
    <div><label>Snapshot poll interval (ms)<input name="snapshotInterval" value="{{.Cfg.SnapshotInterval}}"></label></div>
    <div><label>Log level <span class="hint">verbose = full diagnostics</span>
      <select name="logLevel">
        <option value=""{{if eq .Cfg.LogLevel ""}} selected{{end}}>normal (errors)</option>
        <option value="warning"{{if eq .Cfg.LogLevel "warning"}} selected{{end}}>warning</option>
        <option value="info"{{if eq .Cfg.LogLevel "info"}} selected{{end}}>info</option>
        <option value="verbose"{{if eq .Cfg.LogLevel "verbose"}} selected{{end}}>verbose</option>
      </select></label></div>
  </div>

  <button class="btn" type="submit">Save &amp; apply</button>
</form>
</body>
</html>`))
