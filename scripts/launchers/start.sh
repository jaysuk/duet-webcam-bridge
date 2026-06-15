#!/bin/bash
# Start the Duet Webcam Bridge on Linux (incl. Raspberry Pi).
# Edit config.json (next to this file) to pick a camera, resolution, etc.
#   ./start.sh            start the bridge
#   ./start.sh --list     list available cameras
cd "$(dirname "$0")" || exit 1
./duet-webcam-bridge "$@"
