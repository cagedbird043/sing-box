package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	defaultConfigSyncInterval = 30 * time.Minute
	configURLFilename         = "config-url"
	serviceConfigFilename     = "config.json"
)

func parseConfigURL(value string) (*url.URL, error) {
	remote, err := url.Parse(strings.TrimSpace(value))
	if err != nil || remote.Hostname() == "" || remote.Scheme != "https" && remote.Scheme != "http" {
		return nil, errors.New("configuration URL file must contain an HTTP or HTTPS URL")
	}
	return remote, nil
}

// pull stages a changed configuration without touching the active file.
func (s managedService) pull(ctx context.Context) (path string, hasPrevious bool, err error) {
	file, err := readPrivateURLFile(s.urlPath())
	if err != nil {
		return "", false, fmt.Errorf("read configuration URL file: %w", err)
	}
	remote, err := parseConfigURL(string(file))
	if err != nil {
		return "", false, err
	}
	remotes := []*url.URL{remote}
	defer func() {
		if err != nil {
			err = redactConfigError(err, remotes...)
		}
	}()
	client := http.Client{
		Timeout: 2 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			remotes = append(remotes, req.URL)
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return nil
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, remote.String(), nil)
	if err != nil {
		return "", false, fmt.Errorf("create configuration request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		var urlErr *url.Error
		for errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return "", false, fmt.Errorf("download configuration: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", false, fmt.Errorf("download configuration: HTTP %d %s", response.StatusCode, http.StatusText(response.StatusCode))
	}
	temp, err := os.CreateTemp(s.configDir, ".config-pull-*")
	if err != nil {
		return "", false, fmt.Errorf("create temporary configuration: %w", err)
	}
	defer func() {
		if path == "" {
			_ = os.Remove(temp.Name())
		}
	}()
	digest := sha256.New()
	if err = protectURLFile(temp); err == nil {
		_, err = io.Copy(io.MultiWriter(temp, digest), response.Body)
	}
	closeErr := temp.Close()
	if err != nil {
		return "", false, fmt.Errorf("stage configuration: %w", err)
	}
	if closeErr != nil {
		return "", false, fmt.Errorf("close temporary configuration: %w", closeErr)
	}
	if err = checkServiceFile(s.configPath()); err == nil {
		file, readErr := os.Open(s.configPath())
		if readErr != nil {
			return "", false, readErr
		}
		var downloaded, current [sha256.Size]byte
		digest.Sum(downloaded[:0])
		digest.Reset()
		_, readErr = io.Copy(digest, file)
		closeErr = file.Close()
		if readErr != nil || closeErr != nil {
			return "", false, fmt.Errorf("read current configuration: %w", errors.Join(readErr, closeErr))
		}
		if bytes.Equal(digest.Sum(current[:0]), downloaded[:]) {
			return "", false, nil
		}
		hasPrevious = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, fmt.Errorf("read current configuration: %w", err)
	}
	if err = s.checkConfig(ctx, temp.Name()); err != nil {
		return "", false, fmt.Errorf("downloaded configuration failed check: %w", err)
	}
	return temp.Name(), hasPrevious, nil
}

func (s managedService) childArgs(command, path string) []string {
	return []string{"--disable-color", "-D", s.dataDir, "-c", path, command}
}

func (s managedService) checkConfig(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, s.executable, s.childArgs("check", path)...)
	command.Dir = s.dataDir
	var output checkOutput
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		diagnostic := strings.TrimSpace(output.String())
		if diagnostic != "" {
			return fmt.Errorf("%w: %s", err, diagnostic)
		}
		return err
	}
	return nil
}

type checkOutput struct {
	bytes.Buffer
}

func (o *checkOutput) Write(p []byte) (int, error) {
	remaining := 32*1024 - o.Len()
	if remaining > 0 {
		_, _ = o.Buffer.Write(p[:min(remaining, len(p))])
	}
	return len(p), nil
}

func redactConfigError(err error, remotes ...*url.URL) error {
	original := err.Error()
	message := configURLRedactor(remotes...).Replace(original)
	if message == original {
		return err
	}
	return errors.New(message)
}

func configURLRedactor(remotes ...*url.URL) *strings.Replacer {
	var secrets []string
	for _, remote := range remotes {
		secrets = append(secrets, remote.String(), remote.Host, remote.Hostname(), strings.ToLower(remote.Hostname()), remote.Fragment)
		if remote.RawQuery != "" {
			secrets = append(secrets, "?"+remote.RawQuery)
			if len(remote.RawQuery) >= 4 {
				secrets = append(secrets, remote.RawQuery)
			}
			for _, values := range remote.Query() {
				for _, value := range values {
					if len(value) >= 4 {
						secrets = append(secrets, value, url.QueryEscape(value))
					}
				}
			}
		}
		if remote.Path != "/" {
			secrets = append(secrets, remote.Path, remote.EscapedPath())
		}
		if remote.User != nil {
			secrets = append(secrets, remote.User.String())
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	pairs := make([]string, 0, len(secrets)*2)
	for _, secret := range secrets {
		if secret != "" {
			pairs = append(pairs, secret, "[redacted]")
		}
	}
	return strings.NewReplacer(pairs...)
}

func writeManagedFile(path string, content []byte, private bool) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".sing-box-service-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if private {
		err = protectURLFile(file)
	} else {
		err = file.Chmod(0o644)
	}
	if err == nil {
		_, err = file.Write(content)
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}
