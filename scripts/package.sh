#!/usr/bin/env bash
# Build the Go binary for one target, bundle ffmpeg + launchers + docs, and
# produce a ready-to-ship archive in dist/. Run from the repo root.
#
# Usage: scripts/package.sh <target> <version>
#   target: windows-amd64 | linux-amd64 | linux-arm64 | linux-armhf | macos-amd64
set -euo pipefail

target="${1:?usage: package.sh <target> <version>}"
version="${2:?usage: package.sh <target> <version>}"

case "$target" in
  windows-amd64) goos=windows; goarch=amd64; goarm=""; ext=".exe"; fmt=zip ;;
  linux-amd64)   goos=linux;   goarch=amd64; goarm=""; ext="";     fmt=tar ;;
  linux-arm64)   goos=linux;   goarch=arm64; goarm=""; ext="";     fmt=tar ;;
  linux-armhf)   goos=linux;   goarch=arm;   goarm=7;  ext="";     fmt=tar ;;
  macos-amd64)   goos=darwin;  goarch=amd64; goarm=""; ext="";     fmt=tar ;;
  *) echo "unknown target: $target" >&2; exit 1 ;;
esac

root="$(cd "$(dirname "$0")/.." && pwd)"
stage="$root/dist/duet-webcam-bridge-$version-$target"
rm -rf "$stage"
mkdir -p "$stage"

echo "== Building binary for $goos/$goarch${goarm:+v$goarm} =="
( cd "$root" && GOOS="$goos" GOARCH="$goarch" GOARM="$goarm" CGO_ENABLED=0 \
    go build -trimpath -ldflags "-s -w -X main.version=$version" \
    -o "$stage/duet-webcam-bridge$ext" . )

echo "== Fetching ffmpeg =="
"$root/scripts/fetch-ffmpeg.sh" "$target" "$stage"

echo "== Fetching OpenCV.js (for the tool-alignment plugin, served at /opencv/) =="
# Best-effort: a failed CV fetch shouldn't sink a webcam-bridge release — the /opencv route just
# 404s without these files and camera streaming is unaffected.
bash "$root/scripts/fetch-opencv.sh" "$stage" || echo "warning: OpenCV.js fetch failed; release will ship without /opencv assets"

echo "== Adding launchers + docs =="
cp "$root/config.example.json" "$stage/config.json"
cp "$root/LICENSE" "$stage/" 2>/dev/null || true
cp "$root/scripts/QUICKSTART.txt" "$stage/QUICKSTART.txt"
case "$target" in
  windows-amd64)
    cp "$root/scripts/launchers/Start Webcam Bridge.bat" "$stage/"
    cp "$root/scripts/launchers/List Cameras.bat" "$stage/" ;;
  macos-amd64)
    cp "$root/scripts/launchers/start.command" "$stage/"; chmod +x "$stage/start.command" ;;
  *)
    cp "$root/scripts/launchers/start.sh" "$stage/"; chmod +x "$stage/start.sh" ;;
esac

echo "== Creating archive =="
mkdir -p "$root/dist"
base="duet-webcam-bridge-$version-$target"
( cd "$root/dist"
  if [ "$fmt" = "zip" ]; then
    rm -f "$base.zip"; zip -r -q "$base.zip" "$base"
    echo "$root/dist/$base.zip"
  else
    rm -f "$base.tar.gz"; tar -czf "$base.tar.gz" "$base"
    echo "$root/dist/$base.tar.gz"
  fi )
