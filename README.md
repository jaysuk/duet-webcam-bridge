# Duet Webcam Bridge

Stream a camera into [Duet Web Control](https://github.com/Duet3D/DuetWebControl)
(DWC) from any machine on your network. Works with a **USB / built-in camera**, an
**IP camera** (RTSP/HTTP), or a **Raspberry Pi CSI camera**. Download, unzip,
run — **ffmpeg is bundled, nothing to install.**

DWC can only show a webcam that returns a JPEG over HTTP (it can't display an
RTSP/USB camera directly). This bridge captures your camera and serves it as
exactly that:

- **`/snapshot`** — a single JPEG (for DWC's polled mode)
- **`/stream`** — a live MJPEG stream (for DWC's live mode)

It's a single tiny static binary (written in Go) that drives a bundled `ffmpeg`
(or `rpicam-vid` for Pi CSI cameras), so the same tool works on **Windows, macOS,
Linux and Raspberry Pi**.

## Quick start

1. Grab the file for your computer from the [Releases](../../releases) page:

   | Your computer | File |
   | --- | --- |
   | Windows PC | `*-windows-amd64.zip` |
   | Mac (Intel or Apple Silicon) | `*-macos-amd64.tar.gz` |
   | Linux PC | `*-linux-amd64.tar.gz` |
   | Raspberry Pi (64-bit OS) | `*-linux-arm64.tar.gz` |

   (32-bit Pi OS isn't pre-built yet — see [building from source](#building-from-source).)

2. Unzip it and start the bridge:
   - **Windows:** double-click `Start Webcam Bridge.bat`
   - **macOS:** double-click `start.command` (first time: right-click → Open)
   - **Linux:** `./start.sh`

3. It prints the addresses to use, e.g.:

   ```
   stream (live):   http://192.168.1.50:8081/stream
   snapshot (poll): http://192.168.1.50:8081/snapshot
   ```

   Open the `/stream` address in a browser to confirm you see the camera.

4. In DWC: **Settings → Webcam**
   - **Live video (recommended):** set the URL to your `/stream` address and set
     **Update interval** to `0`.
   - **Polled image:** set the URL to your `/snapshot` address and leave an
     update interval (e.g. `1000` ms).

> Tip: leave DWC's **Settings → General → "Store settings in this browser"**
> **off** so the webcam URL is saved on the machine and works from every PC.

## Settings page (no config-file editing needed)

Open **`http://<this-pc>:8081/config`** in a browser (there's a **Settings** link
on the bridge's home page). It's a simple form for every option — camera source,
device, resolution, network camera URL/credentials, port, etc. **Save & apply**
writes `config.json` and reloads the camera **live**, no restart required; if you
change the port it even moves itself to the new address. Stored passwords are
never shown back on the page.

Editing `config.json` by hand still works too — the keys are below.

## Camera sources

Set the source on the settings page, or `"source"` in `config.json` (or
`--source`), to one of:

| `source` | Camera | Notes |
| --- | --- | --- |
| `usb` *(default)* | USB / built-in camera on this machine | Windows, macOS, Linux, Pi |
| `network` | IP camera (RTSP / HTTP) | transcoded to MJPEG by the bundled ffmpeg |
| `csi` | Raspberry Pi CSI ribbon-cable camera | **Raspberry Pi OS (Trixie) only**, needs `rpicam-apps` |

### USB cameras

Auto-picks the first camera, or set `"device"`:
- Windows: double-click `List Cameras.bat`
- macOS: `./start.command --list`
- Linux: `./start.sh --list`

Put the listed name/ID into `device`, save, and restart.

> **macOS note:** select the camera by its **name** (e.g. `"FaceTime HD Camera"`),
> not a number — macOS re-orders the device indices between runs. The bridge
> automatically tries the pixel formats Mac cameras actually use
> (`uyvy422`/`nv12`/…), so it should start on its own; if it doesn't, open
> `/config`, set **Log level → verbose**, and check the on-page diagnostics.

### Network / IP cameras

```json
{ "source": "network", "url": "rtsp://192.168.1.20:554/stream",
  "username": "admin", "password": "secret" }
```

The bridge holds the credentials and transcodes to MJPEG, so DWC gets a clean
credential-free LAN URL (and can show cameras whose native RTSP/H.264 it couldn't
otherwise display). Credentials are **redacted** from the console/log. If your
camera already serves an authenticated still-JPEG URL, set
`"networkMode": "snapshot"` to just poll and re-serve it (no transcoding — much
lighter, especially on a Pi).

### Raspberry Pi CSI cameras (Trixie)

```json
{ "source": "csi", "resolution": "1280x720", "framerate": 15 }
```

Uses `rpicam-vid` (ships with Raspberry Pi OS). `./start.sh --list` shows the CSI
cameras. *(USB cameras on a Pi just use `source: usb`.)*

## Configuration reference

Settings live in `config.json` next to the program (edit with any text editor).
Anything here can also be passed as a flag (e.g. `--port 8082`); flags win.

| Key | Meaning | Default |
| --- | --- | --- |
| `source` | `usb`, `network` or `csi`. | `usb` |
| `device` | Which camera (usb name/index, or csi index). Empty = first. | `""` |
| `resolution` | **Capture** size, e.g. `"1280x720"`. Empty = native. | `""` |
| `crop` | Cut a region: `"w:h"` (centred) or `"w:h:x:y"`, in pixels. | `""` |
| `scale` | **Output** size sent to DWC, `"WxH"`. Use `-1` on one axis to keep aspect (`"640x-1"`). | `""` |
| `framerate` | Frames per second. | `15` |
| `quality` | ffmpeg `-q:v`: `2` best/larger … `31` worst/smaller. | `5` |
| `port` / `bind` | Port and interface (`0.0.0.0` = all). | `8081` / `0.0.0.0` |
| `url` | Network camera URL (rtsp/http). | `""` |
| `username` / `password` | Network camera credentials. | `""` |
| `rtspTransport` | `tcp` (reliable) or `udp`. | `tcp` |
| `networkMode` | `stream` (transcode) or `snapshot` (poll a JPEG URL). | `stream` |
| `snapshotInterval` | Poll period (ms) for `snapshot` mode. | `1000` |
| `pixelFormat` | Advanced input pixel-format override. | `""` |

**Cropping & scaling.** `crop` cuts a region out of the captured image and `scale`
resizes what DWC receives (crop happens first). For example, to show just the
print bed: capture at full res and crop to it, then shrink for the dashboard —
`"resolution": "1920x1080", "crop": "1000:1000:460:40", "scale": "640x-1"`. Leave
both blank to send the camera image as-is. (`crop` isn't available for Pi CSI
cameras; use `scale`/`resolution` there.)

## Start automatically at boot / login

```
duet-webcam-bridge --install-autostart      # set it up
duet-webcam-bridge --uninstall-autostart     # remove it
```

This installs the right native mechanism for your OS — a **Scheduled Task** at
logon on Windows, a **systemd** service on Linux/Pi (`sudo` for a system-wide one,
otherwise a per-user one), or a **launchd LaunchAgent** on macOS. On macOS you
must run it once interactively first so it can be granted Camera permission.

## Update notifications

On startup (and once a day) the bridge checks GitHub for a newer release and, if one is found, flags
it on the **Settings** page (with a download link for your platform) and in `/health`
(`"update": { "available": true, "latest": "…", "url": "…", "assetUrl": "…" }`). Nothing is
downloaded or replaced automatically — to update, download the linked archive and unzip it over your
current install. The check is on by default; turn it off with the **"Check GitHub for new versions"**
box on the Settings page or `--check-updates=false`. Local `dev` builds never check.

### URLs

The endpoints are deliberately mjpg-streamer-compatible, so existing Duet guides
work too:

| URL | What it does |
| --- | --- |
| `/stream` or `/?action=stream` | live MJPEG stream |
| `/snapshot` or `/?action=snapshot` | single JPEG |
| `/health` | JSON status (handy for debugging) |
| `/opencv/…` | OpenCV.js runtime, when the assets are present (see below) |
| `/` | help page with a live preview |

### Browser CV support (CORS + OpenCV.js)

The camera/asset endpoints send an `Access-Control-Allow-Origin` header (default `*`, set with
`--allow-origin` or `allowOrigin` in config.json, `""` to disable). Plain `<img>` display never needed
this, but a browser plugin that reads camera pixels off a `<canvas>` — such as the automated
tool-alignment plugin — does, otherwise the canvas is "tainted" and `getImageData` throws.

If the OpenCV.js runtime (`opencv.js` [+ `opencv_js.wasm`]) is present in an `opencv/` directory next
to the executable (override with `--opencv-dir`), it's served at `/opencv/` so that plugin can load
the CV engine from this bridge instead of a CDN or the Duet's SD card. Release archives bundle it via
`scripts/fetch-opencv.sh`; the route simply 404s when the assets are absent (camera streaming is
unaffected).

## Building from source

Requires [Go](https://go.dev/dl/) 1.23+.

```bash
go build .                         # builds for your machine; needs ffmpeg on PATH
./duet-webcam-bridge --list        # list cameras
```

To produce a full release-style archive (binary + bundled ffmpeg + launchers) for
a target, on Linux/macOS/WSL:

```bash
scripts/package.sh windows-amd64 0.0.0-local
# targets: windows-amd64 | linux-amd64 | linux-arm64 | linux-armhf | macos-amd64
```

## Releasing

Releases are built automatically by GitHub Actions on a version tag:

```bash
git tag v1.0.0
git push origin v1.0.0
```

The workflow cross-compiles every platform, bundles the matching static ffmpeg,
and publishes a GitHub Release with all the archives attached. See
[CONTRIBUTING.md](CONTRIBUTING.md).

## License

Project code: [MIT](LICENSE). Bundled ffmpeg is separately licensed — see
[NOTICE](NOTICE).
