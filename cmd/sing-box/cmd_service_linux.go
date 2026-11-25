//go:build !android

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const systemdArgumentsComment = "# sing-box argv: "

func managedServiceDefaults() (string, string, string, error) {
	path, err := managedExecutable()
	return path, "/etc/sing-box", "/var/lib/sing-box", err
}

func preflightManagedService(s managedService) error { return nil }

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
ExecStart=:%s
ExecReload=/bin/kill -HUP $MAINPID
Restart=always
RestartSec=3s
LimitNOFILE=infinity

[Install]
WantedBy=multi-user.target
`, systemdArgumentsComment, encodedArgs, strings.Join(quotedArgs, " "))
}

func installManagedService(s managedService) error {
	ctx := context.Background()
	if err := writeManagedDefinition(systemdUnitPath(s.name), renderSystemdService(s)); err != nil {
		return err
	}
	if _, err := runServiceTool(ctx, "/usr/bin/systemctl", "daemon-reload"); err != nil {
		return err
	}
	if _, err := runServiceTool(ctx, "/usr/bin/systemctl", "enable", "--force", s.name+".service"); err != nil {
		return err
	}
	_, err := runServiceTool(ctx, "/usr/bin/systemctl", "restart", s.name+".service")
	return err
}

func uninstallManagedService(name string) error {
	path := systemdUnitPath(name)
	definition, err := readSystemdManagedService(name)
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

func readSystemdManagedService(name string) (*managedService, error) {
	data, err := os.ReadFile(systemdUnitPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	comment, _, _ := strings.Cut(string(data), "\n")
	encodedArgs, ok := strings.CutPrefix(comment, systemdArgumentsComment)
	if !ok {
		return nil, errors.New("systemd unit is not a managed sing-box service")
	}
	var argv []string
	if err = json.Unmarshal([]byte(encodedArgs), &argv); err != nil {
		return nil, errors.New("invalid managed systemd service arguments")
	}
	definition, err := managedServiceFromArgs(name, argv)
	return &definition, err
}

func lookupManagedService(name string) (*managedService, bool, error) {
	definition, err := readSystemdManagedService(name)
	if err != nil || definition == nil {
		return nil, false, err
	}
	_, err = runServiceTool(context.Background(), "/usr/bin/systemctl", "is-active", "--quiet", name+".service")
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || (exit.ExitCode() != 3 && exit.ExitCode() != 4) {
			return nil, false, err
		}
	}
	return definition, err == nil, nil
}

func reloadManagedService(ctx context.Context, name string) error {
	_, err := runServiceTool(ctx, "/usr/bin/systemctl", "reload", name+".service")
	return err
}
