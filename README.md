# Duet Webcam Bridge

Stream a **USB camera plugged into your PC** into [Duet Web Control](https://github.com/Duet3D/DuetWebControl)
(DWC), from any machine on your network. Download, unzip, run — **ffmpeg is
bundled, nothing to install.**

DWC can only show a webcam that returns a JPEG over HTTP (it can't display an
RTSP/USB camera directly). This little bridge captures your USB camera and serves
it as exactly that:

- **`/snapshot`** — a single JPEG (for DWC's polled mode)
- **`/stream`** — a live MJPEG stream (for DWC's live mode)

It's a single tiny static binary (written in Go) that drives a bundled `ffmpeg`
for capture, so the same tool works on **Windows, macOS, Linux and Raspberry Pi**.

## Quick start

1. Grab the file for your computer from the [Releases](../../releases) page:

   | Your computer | File |
   | --- | --- |
   | Windows PC | `*-windows-amd64.zip` |
   | Mac (Intel or Apple Silicon) | `*-macos-amd64.tar.gz` |
   | Linux PC | `*-linux-amd64.tar.gz` |
   | Raspberry Pi (64-bit OS) | `*-linux-arm64.tar.gz` |
   | Raspberry Pi (32-bit OS) | `*-linux-armhf.tar.gz` |

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

## Configuration

Settings live in `config.json` next to the program (edit with any text editor):

| Key | Meaning | Default |
| --- | --- | --- |
| `device` | Which camera (see below). Empty = first one found. | `""` |
| `resolution` | e.g. `"1280x720"`. Empty = camera default. | `""` |
| `framerate` | Frames per second. | `15` |
| `quality` | ffmpeg `-q:v`: `2` best/larger … `31` worst/smaller. | `5` |
| `port` | Port to serve on. | `8081` |
| `bind` | Interface to listen on (`0.0.0.0` = all). | `0.0.0.0` |

**Finding a camera name/ID:**
- Windows: double-click `List Cameras.bat`
- macOS: `./start.command --list`
- Linux: `./start.sh --list`

Put the listed name/ID into `device`, save, and restart. Anything in `config.json`
can also be passed as a flag (e.g. `--port 8082 --device "HD Pro Webcam C920"`);
flags win over the file.

### URLs

The endpoints are deliberately mjpg-streamer-compatible, so existing Duet guides
work too:

| URL | What it does |
| --- | --- |
| `/stream` or `/?action=stream` | live MJPEG stream |
| `/snapshot` or `/?action=snapshot` | single JPEG |
| `/health` | JSON status (handy for debugging) |
| `/` | help page with a live preview |

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
