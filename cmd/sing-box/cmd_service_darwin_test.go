//go:build !ios

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLaunchdManagedService(t *testing.T) {
	definition := managedService{
		name: "isolated", executable: "/opt/homebrew/opt/sing-box-cagedbird/bin/sing-box", configDir: "/etc/with & spaces", dataDir: "/var/lib/with spaces", interval: 5 * time.Minute,
	}
	const logfile = "/opt/homebrew/var/log/isolated.log"
	plist, err := renderLaunchdService(definition, logfile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(plist)
	for _, entry := range []string{
		"<string>" + definition.executable + "</string>", "<string>/etc/with &amp; spaces/config.json</string>",
		"<string>/var/lib/with spaces</string>", "<string>--sync-config</string>", "<string>5m0s</string>",
		"<key>RunAtLoad</key><true/>", "<key>KeepAlive</key><true/>",
		"<key>StandardErrorPath</key><string>" + logfile + "</string>",
	} {
		if !strings.Contains(text, entry) {
			t.Errorf("missing entry %q in:\n%s", entry, plist)
		}
	}
	path := filepath.Join(t.TempDir(), "isolated.plist")
	if err = os.WriteFile(path, plist, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/usr/bin/plutil", "-lint", path).CombinedOutput(); err != nil {
		t.Fatalf("plutil rejected definition: %v: %s", err, out)
	}
}
