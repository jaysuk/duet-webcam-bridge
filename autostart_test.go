package main

import (
	"strings"
	"testing"
)

func TestSystemdUnit(t *testing.T) {
	unit := systemdUnit("/opt/dwb/duet-webcam-bridge", "/opt/dwb", "pi")
	for _, want := range []string{
		"ExecStart=/opt/dwb/duet-webcam-bridge",
		"WorkingDirectory=/opt/dwb",
		"User=pi",
		"SupplementaryGroups=video render",
		"Restart=always",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("systemd unit missing %q:\n%s", want, unit)
		}
	}
}

func TestSystemdUnit_NoUser(t *testing.T) {
	unit := systemdUnit("/x/dwb", "/x", "")
	if strings.Contains(unit, "User=") {
		t.Errorf("expected no User= line when user is empty:\n%s", unit)
	}
}

func TestWindowsLauncher(t *testing.T) {
	s := windowsLauncher(`C:\Apps\dwb\duet-webcam-bridge.exe`)
	if !strings.Contains(s, `cd /d "C:\Apps\dwb"`) {
		t.Errorf("launcher should cd to the exe dir:\n%s", s)
	}
	if !strings.Contains(s, `start "" /min "C:\Apps\dwb\duet-webcam-bridge.exe"`) {
		t.Errorf("launcher should start the exe minimized:\n%s", s)
	}
	if !strings.Contains(s, "\r\n") {
		t.Error("Windows .cmd should use CRLF line endings")
	}
}

func TestLaunchdPlist(t *testing.T) {
	plist := launchdPlist("com.test.bridge", "/Apps/dwb", "/Apps", "/tmp/dwb.log")
	for _, want := range []string{
		"<string>com.test.bridge</string>",
		"<string>/Apps/dwb</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"/tmp/dwb.log",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing %q:\n%s", want, plist)
		}
	}
}
