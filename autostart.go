package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// runAutostart installs (or removes) an OS-native mechanism that launches the
// bridge automatically at boot/login. Each platform uses the approach that can
// actually access a camera:
//   - Windows: a Scheduled Task that runs at logon (NOT a Session-0 service,
//     where DirectShow USB capture is unreliable).
//   - Linux:   a systemd service (system unit if root, else a user unit).
//   - macOS:   a launchd LaunchAgent (per-user, so it can use the per-user
//     Camera privacy permission).
func runAutostart(install bool) {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot determine executable path: %v\n", err)
		os.Exit(1)
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}

	switch runtime.GOOS {
	case "windows":
		err = autostartWindows(install, exe)
	case "darwin":
		err = autostartDarwin(install, exe)
	default:
		err = autostartLinux(install, exe)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "autostart: %v\n", err)
		os.Exit(1)
	}
}

const launchdLabel = "com.jaysuk.duet-webcam-bridge"

// --- Windows ---

// On Windows we drop a small launcher into the per-user Startup folder. This
// runs at logon with no elevation (unlike a Scheduled Task, which can demand
// admin on locked-down accounts) and no extra dependencies.
func autostartWindows(install bool, exe string) error {
	startup := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	launcher := filepath.Join(startup, "Duet Webcam Bridge.cmd")

	if !install {
		if err := os.Remove(launcher); err != nil && !os.IsNotExist(err) {
			return err
		}
		_ = exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", "name=Duet Webcam Bridge").Run()
		fmt.Println("Autostart removed.")
		return nil
	}

	if err := os.MkdirAll(startup, 0o755); err != nil {
		return err
	}
	script := windowsLauncher(exe)
	if err := os.WriteFile(launcher, []byte(script), 0o644); err != nil {
		return fmt.Errorf("writing startup launcher: %w", err)
	}
	// Best-effort firewall rule (needs admin; ignored if it can't be added).
	_ = exec.Command("netsh", "advfirewall", "firewall", "add", "rule",
		"name=Duet Webcam Bridge", "dir=in", "action=allow",
		"program="+exe, "enable=yes").Run()
	fmt.Printf("Installed: the bridge will start when you log in.\n  %s\n", launcher)
	fmt.Println("(If Windows asks to allow network access the first time, click Allow.)")
	return nil
}

func windowsLauncher(exe string) string {
	return fmt.Sprintf("@echo off\r\ncd /d \"%s\"\r\nstart \"\" /min \"%s\"\r\n", filepath.Dir(exe), exe)
}

// --- macOS ---

func autostartDarwin(install bool, exe string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
	if !install {
		_ = exec.Command("launchctl", "unload", plistPath).Run()
		if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Println("Autostart removed.")
		return nil
	}
	logPath := filepath.Join(home, "Library", "Logs", "duet-webcam-bridge.log")
	plist := launchdPlist(launchdLabel, exe, filepath.Dir(exe), logPath)
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return err
	}
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	if out, err := exec.Command("launchctl", "load", "-w", plistPath).CombinedOutput(); err != nil {
		return fmt.Errorf("loading LaunchAgent: %v\n%s", err, out)
	}
	fmt.Printf("Installed LaunchAgent at %s\n", plistPath)
	fmt.Println("NOTE: the first time, macOS must prompt for Camera access. If the")
	fmt.Println("camera stays black, run the bridge once from Terminal to grant it,")
	fmt.Println("then it will work at login.")
	return nil
}

func launchdPlist(label, exe, workdir, logPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
	</array>
	<key>WorkingDirectory</key>
	<string>%s</string>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, label, exe, workdir, logPath, logPath)
}

// --- Linux ---

func autostartLinux(install bool, exe string) error {
	root := os.Geteuid() == 0
	var unitPath string
	var reload, enable, disable []string
	if root {
		unitPath = "/etc/systemd/system/duet-webcam-bridge.service"
		reload = []string{"systemctl", "daemon-reload"}
		enable = []string{"systemctl", "enable", "--now", "duet-webcam-bridge.service"}
		disable = []string{"systemctl", "disable", "--now", "duet-webcam-bridge.service"}
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		unitPath = filepath.Join(home, ".config", "systemd", "user", "duet-webcam-bridge.service")
		reload = []string{"systemctl", "--user", "daemon-reload"}
		enable = []string{"systemctl", "--user", "enable", "--now", "duet-webcam-bridge.service"}
		disable = []string{"systemctl", "--user", "disable", "--now", "duet-webcam-bridge.service"}
	}

	if !install {
		_ = exec.Command(disable[0], disable[1:]...).Run()
		if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		_ = exec.Command(reload[0], reload[1:]...).Run()
		fmt.Println("Autostart removed.")
		return nil
	}

	user := ""
	if root {
		user = os.Getenv("SUDO_USER") // run as the invoking user, not root, for camera/group access
	}
	unit := systemdUnit(exe, filepath.Dir(exe), user)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w (try with sudo)", unitPath, err)
	}
	_ = exec.Command(reload[0], reload[1:]...).Run()
	if out, err := exec.Command(enable[0], enable[1:]...).CombinedOutput(); err != nil {
		return fmt.Errorf("enabling service: %v\n%s", err, out)
	}
	fmt.Printf("Installed systemd unit at %s and started it.\n", unitPath)
	if !root {
		fmt.Println("For it to start at boot before you log in:  sudo loginctl enable-linger " + os.Getenv("USER"))
	}
	return nil
}

func systemdUnit(exe, workdir, user string) string {
	userLine := ""
	if user != "" {
		userLine = "User=" + user + "\nSupplementaryGroups=video render\n"
	}
	return fmt.Sprintf(`[Unit]
Description=Duet Webcam Bridge
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s
WorkingDirectory=%s
%sRestart=always
RestartSec=3

[Install]
WantedBy=%s
`, exe, workdir, userLine, wantedByTarget(user))
}

func wantedByTarget(user string) string {
	// User units install under default.target; system units under multi-user.
	if user == "" && os.Geteuid() != 0 {
		return "default.target"
	}
	return "multi-user.target"
}
