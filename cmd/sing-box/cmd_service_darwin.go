//go:build !ios

package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func homebrewLayout(path string) (prefix, formula string) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		candidate, rest, ok := strings.Cut(resolved, "/Cellar/")
		if ok && candidate != "" && filepath.Base(filepath.Dir(resolved)) == "bin" {
			name, _, _ := strings.Cut(rest, "/")
			return candidate, name
		}
	}
	return "", ""
}

func managedServiceDefaults() (string, string, string, error) {
	path, err := managedExecutable()
	if err != nil {
		return "", "", "", err
	}
	prefix, formula := homebrewLayout(path)
	if formula != "" {
		optPath := filepath.Join(prefix, "opt", formula, "bin", filepath.Base(path))
		binPath := filepath.Join(prefix, "bin", filepath.Base(path))
		if _, err = os.Stat(optPath); err == nil {
			path = optPath
		} else if _, err = os.Stat(binPath); err == nil {
			path = binPath
		} else {
			return "", "", "", errors.New("cannot find a stable Homebrew executable link")
		}
	}
	if prefix != "" {
		return path, filepath.Join(prefix, "etc", "sing-box"), filepath.Join(prefix, "var", "lib", "sing-box"), nil
	}
	return path, "/etc/sing-box", "/Library/Application Support/sing-box", nil
}

func launchdPath(name string) string {
	return filepath.Join("/Library/LaunchDaemons", name+".plist")
}

func managedServiceLog(s managedService) string {
	return s.logPath()
}

func renderLaunchdService(s managedService) ([]byte, error) {
	var output strings.Builder
	output.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>`)
	if err := xml.EscapeText(&output, []byte(s.name)); err != nil {
		return nil, err
	}
	output.WriteString("</string>\n<key>ProgramArguments</key><array>\n")
	for _, arg := range append([]string{s.executable}, s.args()...) {
		output.WriteString("<string>")
		if err := xml.EscapeText(&output, []byte(arg)); err != nil {
			return nil, err
		}
		output.WriteString("</string>\n")
	}
	output.WriteString("</array>\n<key>RunAtLoad</key><true/>\n<key>KeepAlive</key><true/>\n")
	output.WriteString("</dict></plist>\n")
	return []byte(output.String()), nil
}

func installManagedService(s managedService) error {
	ctx := context.Background()
	content, err := renderLaunchdService(s)
	if err != nil {
		return err
	}
	target := "system/" + s.name
	path := launchdPath(s.name)
	_, loaded, err := queryLaunchd(s.name)
	if err != nil {
		return err
	}
	if loaded {
		if _, err = runServiceTool(ctx, "/bin/launchctl", "bootout", target); err != nil {
			return err
		}
	}
	if err = writeManagedFile(path, content, false); err != nil {
		return err
	}
	_, err = runServiceTool(ctx, "/bin/launchctl", "bootstrap", "system", path)
	return err
}

func uninstallManagedService(name string) error {
	definition, _, err := readLaunchdManagedService(name, true)
	if err != nil || definition == nil {
		return err
	}
	_, loaded, err := queryLaunchd(name)
	if err != nil {
		return err
	}
	if loaded {
		if _, err = runServiceTool(context.Background(), "/bin/launchctl", "bootout", "system/"+name); err != nil {
			return err
		}
	}
	return os.Remove(launchdPath(name))
}

func queryLaunchd(name string) (running, loaded bool, err error) {
	out, err := runServiceTool(context.Background(), "/bin/launchctl", "print", "system/"+name)
	if err != nil {
		if strings.Contains(string(out), "Could not find service") {
			return false, false, nil
		}
		return false, false, err
	}
	var path string
	for line := range strings.SplitSeq(string(out), "\n") {
		switch line = strings.TrimSpace(line); {
		case strings.HasPrefix(line, "path = ") && path == "":
			path = strings.TrimPrefix(line, "path = ")
		case line == "state = running":
			running = true
		}
	}
	if path != launchdPath(name) {
		return false, false, errors.New("launchd job at this name is not a managed sing-box service")
	}
	return running, true, nil
}

func readLaunchdManagedService(name string, uninstall bool) (*managedService, bool, error) {
	running, loaded, err := queryLaunchd(name)
	if err != nil {
		return nil, false, err
	}
	path := launchdPath(name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && !loaded {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, errors.New("launchd file is not a regular managed sing-box definition")
	}
	out, err := runServiceTool(context.Background(), "/usr/bin/plutil", "-extract", "ProgramArguments", "json", "-o", "-", path)
	if err != nil {
		return nil, false, err
	}
	var argv []string
	if err = json.Unmarshal(out, &argv); err != nil {
		return nil, false, errors.New("invalid managed launchd service arguments")
	}
	definition, err := managedServiceFromArgs(name, argv, uninstall)
	return definition, running, err
}

func lookupManagedService(name string) (*managedService, bool, error) {
	return readLaunchdManagedService(name, false)
}

func runManagedService(ctx context.Context, s managedService) error {
	if err := s.checkPaths(false); err != nil {
		return err
	}
	output, err := s.openLog()
	if err != nil {
		return err
	}
	defer output.Close()
	err = runUnixManagedService(ctx, s, output, nil)
	if err != nil {
		_, _ = fmt.Fprintln(output, err)
	}
	return err
}
