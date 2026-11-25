//go:build !android

package main

import (
	"strings"
	"testing"
	"time"
)

func TestSystemdManagedService(t *testing.T) {
	definition := managedService{
		name: "isolated", executable: "/usr/bin/sing-box", configDir: "/etc/with % spaces", dataDir: "/var/lib/with $spaces", interval: 5 * time.Minute,
	}
	unit := string(renderSystemdService(definition))
	for _, line := range []string{
		`ExecStart=:"/usr/bin/sing-box" "--disable-color" "-D" "/var/lib/with $spaces" "-c" "/etc/with %% spaces/config.json" "run" "--sync-config" "--sync-interval" "5m0s" "--service-name" "isolated"`,
		`ExecReload=/bin/kill -HUP $MAINPID`, `Wants=network-online.target`, `After=network-online.target`,
		`StartLimitIntervalSec=0`, `Restart=always`, `RestartSec=3s`,
	} {
		if !strings.Contains(unit, "\n"+line+"\n") {
			t.Errorf("missing line %q in:\n%s", line, unit)
		}
	}
}
