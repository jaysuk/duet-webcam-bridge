# Contributing

## Project layout

| File | Purpose |
| --- | --- |
| `main.go` | Entry point: config, wiring, startup banner, signal handling |
| `config.go` | Config struct + resolution (defaults → `config.json` → flags) |
| `ffmpeg.go` | Locating ffmpeg, per-OS input format, camera listing |
| `capture.go` | Drives ffmpeg, parses MJPEG frames, keeps latest + fans out |
| `server.go` | HTTP endpoints (`/snapshot`, `/stream`, `/health`, help page) |
| `scripts/` | ffmpeg fetch, packaging, and the user-facing launcher scripts |
| `.github/workflows/` | `ci.yml` (vet/build/test), `release.yml` (tagged releases) |

It's pure Go standard library — no third-party modules, no cgo. Capture itself is
done by a bundled `ffmpeg`, which is what makes it cross-platform.

## Local development

```bash
go vet ./...
go build .
```

For an end-to-end test you need an `ffmpeg` (on PATH, or next to the binary, or
`--ffmpeg /path`). You can grab a static one with:

```bash
scripts/fetch-ffmpeg.sh linux-amd64 .     # drops ./ffmpeg here (gitignored)
```

## Releasing

Releases are fully automated. Bump nothing in code (the version comes from the
tag); just:

```bash
git tag v1.2.3
git push origin v1.2.3
```

`release.yml` then, for each target
(`windows-amd64`, `linux-amd64`, `linux-arm64`, `linux-armhf`, `macos-amd64`):

1. cross-compiles the binary with `-X main.version=<tag>`,
2. downloads the matching static ffmpeg (`scripts/fetch-ffmpeg.sh`),
3. bundles binary + ffmpeg + launcher + `config.json` + `QUICKSTART.txt`
   (`scripts/package.sh`),
4. and the `release` job publishes one GitHub Release with every archive attached
   and auto-generated notes.

You can also trigger it manually (Actions → Release → Run workflow) to build the
archives as artifacts without publishing.

### Adding a platform

Add a case to both `scripts/fetch-ffmpeg.sh` (ffmpeg source) and
`scripts/package.sh` (GOOS/GOARCH + archive format), then add the target to the
matrix in `.github/workflows/release.yml`.
