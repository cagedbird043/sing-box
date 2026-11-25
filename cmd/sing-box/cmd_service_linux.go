//go:build !android

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const systemdArgumentsComment = "# sing-box argv: "

func managedServiceDefaults() (string, string, string, error) {
	path, err := managedExecutable()
	return path, "/etc/sing-box", "/var/lib/sing-box", err
}

func systemdUnitPath(name string) string {
	return filepath.Join("/etc/systemd/system", name+".service")
}

func managedServiceLog(s managedService) string {
	return "journalctl -u " + s.name + ".service"
}

func renderSystemdService(s managedService) []byte {
	argv := append([]string{s.executable}, s.args()...)
	encodedArgs, _ := json.Marshal(argv)
	quotedArgs := make([]string, len(argv))
	for i, arg := range argv {
		quotedArgs[i] = strconv.Quote(strings.ReplaceAll(arg, "%", "%%"))
	}
	return fmt.Appendf(nil, `%s%s
[Unit]
Description=sing-box managed service
Wants=network-online.target
After=network-online.target
StartLimitIntervalSec=0

[Service]
Type=notify
ExecStart=:%s
KillMode=mixed
TimeoutStartSec=155s
TimeoutStopSec=20s
Restart=on-failure
RestartSec=3s
LimitNOFILE=infinity

[Install]
WantedBy=multi-user.target
`, systemdArgumentsComment, encodedArgs, strings.Join(quotedArgs, " "))
}

func installManagedService(s managedService) error {
	ctx := context.Background()
	if err := writeManagedFile(systemdUnitPath(s.name), renderSystemdService(s), false); err != nil {
		return err
	}
	if _, err := runServiceTool(ctx, "/usr/bin/systemctl", "daemon-reload"); err != nil {
		return err
	}
	if _, err := runServiceTool(ctx, "/usr/bin/systemctl", "enable", s.name+".service"); err != nil {
		return err
	}
	_, err := runServiceTool(ctx, "/usr/bin/systemctl", "restart", s.name+".service")
	return err
}

func uninstallManagedService(name string) error {
	path := systemdUnitPath(name)
	definition, _, err := readSystemdManagedService(name, true)
	if err != nil || definition == nil {
		return err
	}
	ctx := context.Background()
	if _, err := runServiceTool(ctx, "/usr/bin/systemctl", "disable", "--now", name+".service"); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	_, err = runServiceTool(ctx, "/usr/bin/systemctl", "daemon-reload")
	return err
}

func readSystemdManagedService(name string, uninstall bool) (*managedService, bool, error) {
	// Refuse to mask vendor units, aliases, or jobs loaded from another path.
	path := systemdUnitPath(name)
	out, showErr := runServiceTool(context.Background(), "/usr/bin/systemctl", "show",
		"--property=LoadState,FragmentPath,ActiveState", name+".service")
	var loadState, fragment string
	var running bool
	for line := range strings.SplitSeq(string(out), "\n") {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "LoadState":
			loadState = value
		case "FragmentPath":
			fragment = value
		case "ActiveState":
			running = value == "active"
		}
	}
	if showErr != nil && loadState != "not-found" {
		return nil, false, showErr
	}
	if loadState != "not-found" && fragment != path {
		return nil, false, errors.New("systemd unit at this name is not a managed sing-box service")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && loadState == "not-found" {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, errors.New("systemd unit is not a regular managed sing-box definition")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	comment, _, _ := strings.Cut(string(data), "\n")
	encodedArgs, ok := strings.CutPrefix(comment, systemdArgumentsComment)
	if !ok {
		return nil, false, errors.New("systemd unit is not a managed sing-box service")
	}
	var argv []string
	if err = json.Unmarshal([]byte(encodedArgs), &argv); err != nil {
		return nil, false, errors.New("invalid managed systemd service arguments")
	}
	definition, err := managedServiceFromArgs(name, argv, uninstall)
	return definition, running, err
}

func lookupManagedService(name string) (*managedService, bool, error) {
	return readSystemdManagedService(name, false)
}

func runManagedService(ctx context.Context, s managedService) error {
	if err := s.checkPaths(false); err != nil {
		return err
	}
	return runUnixManagedService(ctx, s, os.Stderr, notifySystemdSupervisorStarted)
}

func notifySystemdSupervisorStarted() error {
	address := os.Getenv("NOTIFY_SOCKET")
	if address == "" {
		return nil
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: address, Net: "unixgram"})
	if err != nil {
		return fmt.Errorf("connect to systemd notification socket: %w", err)
	}
	defer conn.Close()
	if _, err = conn.Write([]byte("READY=1")); err != nil {
		return fmt.Errorf("notify systemd that the supervisor has started: %w", err)
	}
	return nil
}
