#!/usr/bin/env bash
# Download a prebuilt *static* ffmpeg for a target platform and drop the single
# binary into <destdir>. Used by the release workflow to bundle ffmpeg into each
# release archive, and handy locally for testing.
#
# Usage: scripts/fetch-ffmpeg.sh <target> <destdir>
#   target: windows-amd64 | linux-amd64 | linux-arm64 | linux-armhf | macos-amd64
#
# These are well-known, stable static-build sources (no apt/install needed):
#   - Windows: gyan.dev
#   - Linux:   BtbN/FFmpeg-Builds on GitHub (amd64 + arm64; reliable from CI).
#              armhf still uses johnvansickle.com (not pre-built in the release matrix).
#   - macOS:   evermeet.cx (x86_64; runs on Apple Silicon via Rosetta)
set -euo pipefail

target="${1:?usage: fetch-ffmpeg.sh <target> <destdir>}"
dest="${2:?usage: fetch-ffmpeg.sh <target> <destdir>}"
mkdir -p "$dest"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

case "$target" in
  windows-amd64)
    url="https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip"
    archive="$tmp/ff.zip"; binname="ffmpeg.exe" ;;
  linux-amd64)
    # BtbN's GitHub-hosted static builds (johnvansickle.com intermittently 415s GitHub runners).
    url="https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-linux64-gpl.tar.xz"
    archive="$tmp/ff.tar.xz"; binname="ffmpeg" ;;
  linux-arm64)
    url="https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-linuxarm64-gpl.tar.xz"
    archive="$tmp/ff.tar.xz"; binname="ffmpeg" ;;
  linux-armhf)
    url="https://johnvansickle.com/ffmpeg/releases/ffmpeg-release-armhf-static.tar.xz"
    archive="$tmp/ff.tar.xz"; binname="ffmpeg" ;;
  macos-amd64)
    url="https://evermeet.cx/ffmpeg/getrelease/ffmpeg/zip"
    archive="$tmp/ff.zip"; binname="ffmpeg" ;;
  *)
    echo "unknown target: $target" >&2; exit 1 ;;
esac

echo "Downloading ffmpeg for $target"
echo "  $url"
# Explicit Accept/identity headers avoid an Apache content-negotiation quirk that
# can return 415 on some johnvansickle variants; retry-all-errors rides out the
# occasional transient hiccup.
curl -fsSL --retry 5 --retry-all-errors --retry-delay 2 \
  -H 'Accept: */*' -H 'Accept-Encoding: identity' \
  -o "$archive" "$url"

echo "Extracting"
case "$archive" in
  *.zip)    unzip -q "$archive" -d "$tmp/x" ;;
  *.tar.xz) mkdir -p "$tmp/x" && tar -xJf "$archive" -C "$tmp/x" ;;
  *.tar.gz) mkdir -p "$tmp/x" && tar -xzf "$archive" -C "$tmp/x" ;;
esac

# Find the ffmpeg binary anywhere in the extracted tree and copy it out.
found="$(find "$tmp/x" -type f -name "$binname" | head -n1)"
if [ -z "$found" ]; then
  echo "could not find $binname in the downloaded archive" >&2
  exit 1
fi
cp "$found" "$dest/$binname"
chmod +x "$dest/$binname" || true
echo "ffmpeg -> $dest/$binname"
