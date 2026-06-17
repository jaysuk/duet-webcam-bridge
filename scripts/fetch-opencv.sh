#!/usr/bin/env bash
# Download the prebuilt OpenCV.js runtime (opencv.js + opencv_js.wasm) into
# <destdir>/opencv, so the bridge can serve it at /opencv/ for the browser
# tool-alignment plugin. OpenCV.js is platform-independent (WASM + JS loader),
# so a single fetch covers every release target.
#
# Usage: scripts/fetch-opencv.sh <destdir> [version]
#   destdir: where to create the opencv/ directory (e.g. the package staging dir)
#   version: OpenCV release tag (default below)
#
# The official prebuilt opencv.js is published per release on docs.opencv.org.
# Some builds inline the wasm; others fetch opencv_js.wasm alongside — we copy
# whichever files are present so the plugin's Module.locateFile can point at
# this directory either way.
set -euo pipefail

dest="${1:?usage: fetch-opencv.sh <destdir> [version]}"
version="${2:-4.9.0}"
outdir="$dest/opencv"
mkdir -p "$outdir"

base="https://docs.opencv.org/${version}"

echo "Downloading OpenCV.js ${version}"
echo "  $base/opencv.js"
curl -fsSL --retry 5 --retry-all-errors --retry-delay 2 \
  -H 'Accept: */*' -H 'Accept-Encoding: identity' \
  -o "$outdir/opencv.js" "$base/opencv.js"

# opencv_js.wasm is optional (only present for split wasm builds). Don't fail
# the whole fetch if this particular release inlines it.
if curl -fsSL --retry 3 --retry-all-errors --retry-delay 2 \
     -H 'Accept: */*' -H 'Accept-Encoding: identity' \
     -o "$outdir/opencv_js.wasm" "$base/opencv_js.wasm"; then
  echo "opencv.js + opencv_js.wasm -> $outdir"
else
  rm -f "$outdir/opencv_js.wasm"
  echo "opencv.js (wasm inlined) -> $outdir"
fi
