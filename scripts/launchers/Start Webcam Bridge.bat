@echo off
rem Double-click this to start the Duet Webcam Bridge.
rem Edit config.json (next to this file) to pick a camera, resolution, etc.
cd /d "%~dp0"
duet-webcam-bridge.exe
echo.
echo The webcam bridge has stopped. Press any key to close this window.
pause >nul
