#!/bin/bash
# Double-click this (macOS) to start the Duet Webcam Bridge.
# Edit config.json (next to this file) to pick a camera, resolution, etc.
cd "$(dirname "$0")" || exit 1

# Because this was downloaded, macOS quarantines the bundled binaries. Clear it
# so Gatekeeper doesn't refuse to run them. (Harmless if already cleared.)
xattr -dr com.apple.quarantine . 2>/dev/null || true

./duet-webcam-bridge "$@"
