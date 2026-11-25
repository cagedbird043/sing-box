//go:build !ios

package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func homebrewLayout(path string) (prefix, formula string) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		candidate, rest, ok := strings.Cut(resolved, "/Cellar/")
		if ok && filepath.Base(filepath.Dir(resolved)) == "bin" {
			name, _, _ := strings.Cut(rest, "/")
			return candidate, name
		}
	}
	for _, prefix := range []string{"/opt/homebrew", "/usr/local"} {
		if _, err := os.Stat(filepath.Join(prefix, "bin", "brew")); err == nil {
			return prefix, ""
		}
	}
	return "/usr/local", ""
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
	return path, filepath.Join(prefix, "etc", "sing-box"), filepath.Join(prefix, "var", "lib", "sing-box"), nil
}

func launchdPath(name string) string {
	return filepath.Join("/Library/LaunchDaemons", name+".plist")
}

func managedServiceLog(s managedService) string {
	prefix, _ := homebrewLayout(s.executable)
	return filepath.Join(prefix, "var", "log", s.name+".log")
}

func preflightManagedService(s managedService) error {
	_, formula := homebrewLayout(s.executable)
	if formula == "" {
		return nil
	}
	uids, err := exec.Command("/bin/ps", "-axo", "uid=").Output()
	if err != nil {
		return fmt.Errorf("list user domains: %w", err)
	}
	domains := []string{"system"}
	seen := make(map[string]bool)
	for _, uid := range strings.Fields(string(uids)) {
		if uid != "0" && !seen[uid] {
			seen[uid] = true
			domains = append(domains, "gui/"+uid, "user/"+uid)
		}
	}
	for _, label := range []string{"sh.brew." + formula, "homebrew.mxcl." + formula} {
		for _, domain := range domains {
			if _, loaded := queryLaunchd(domain + "/" + label); loaded {
				if domain != "system" {
					return fmt.Errorf("Homebrew service %s is loaded; in its user session run brew services stop %s (system service: sudo brew services stop %s)", label, formula, formula)
				}
				return fmt.Errorf("Homebrew service %s is loaded; run sudo brew services stop %s first", label, formula)
			}
		}
	}
	return nil
}

func renderLaunchdService(s managedService, logfile string) ([]byte, error) {
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
	for _, key := range []string{"StandardOutPath", "StandardErrorPath"} {
		fmt.Fprintf(&output, "<key>%s</key><string>", key)
		if err := xml.EscapeText(&output, []byte(logfile)); err != nil {
			return nil, err
		}
		output.WriteString("</string>\n")
	}
	output.WriteString("</dict></plist>\n")
	return []byte(output.String()), nil
}

func installManagedService(s managedService) error {
	ctx := context.Background()
	logfile := managedServiceLog(s)
	if err := os.MkdirAll(filepath.Dir(logfile), 0o755); err != nil {
		return err
	}
	content, err := renderLaunchdService(s, logfile)
	if err != nil {
		return err
	}
	target := "system/" + s.name
	if _, loaded := queryLaunchd(target); loaded {
		if _, err = runServiceTool(ctx, "/bin/launchctl", "bootout", target); err != nil {
			return err
		}
	}
	path := launchdPath(s.name)
	if err = writeManagedDefinition(path, content); err != nil {
		return err
	}
	_, err = runServiceTool(ctx, "/bin/launchctl", "bootstrap", "system", path)
	return err
}

func uninstallManagedService(name string) error {
	definition, _, err := lookupManagedService(name)
	if err != nil || definition == nil {
		return err
	}
	target := "system/" + name
	if _, loaded := queryLaunchd(target); loaded {
		if _, err = runServiceTool(context.Background(), "/bin/launchctl", "bootout", target); err != nil {
			return err
		}
	}
	return os.Remove(launchdPath(name))
}

func queryLaunchd(target string) (running, loaded bool) {
	out, err := exec.Command("/bin/launchctl", "print", target).Output()
	if err != nil {
		return false, false
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.TrimSpace(line) == "state = running" {
			return true, true
		}
	}
	return false, true
}

func lookupManagedService(name string) (*managedService, bool, error) {
	path := launchdPath(name)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	out, err := runServiceTool(context.Background(), "/usr/bin/plutil", "-extract", "ProgramArguments", "json", "-o", "-", path)
	if err != nil {
		return nil, false, err
	}
	var argv []string
	if err = json.Unmarshal(out, &argv); err != nil {
		return nil, false, errors.New("invalid managed launchd service arguments")
	}
	definition, err := managedServiceFromArgs(name, argv)
	if err != nil {
		return nil, false, err
	}
	running, _ := queryLaunchd("system/" + name)
	return &definition, running, nil
}

func reloadManagedService(ctx context.Context, name string) error {
	_, err := runServiceTool(ctx, "/bin/launchctl", "kill", "HUP", "system/"+name)
	return err
}
