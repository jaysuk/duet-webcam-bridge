@echo off
rem Double-click to see the camera names connected to this PC.
rem Copy the name you want into the "device" field of config.json.
cd /d "%~dp0"
duet-webcam-bridge.exe --list
echo.
pause >nul
