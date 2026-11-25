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
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sagernet/sing-box/experimental/deprecated"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service"
)

const (
	defaultConfigSyncInterval = 30 * time.Minute
	configURLFilename         = "config-url"
	serviceConfigFilename     = "config.json"
)

type configSync struct {
	target   string
	interval time.Duration
}

func (s configSync) urlFile() string {
	return filepath.Join(filepath.Dir(s.target), configURLFilename)
}

func parseConfigURL(value string) (*url.URL, error) {
	remote, err := url.Parse(strings.TrimSpace(value))
	if err != nil || remote.Hostname() == "" || remote.Scheme != "https" && remote.Scheme != "http" {
		return nil, errors.New("configuration URL file must contain an HTTP or HTTPS URL")
	}
	return remote, nil
}

func (s configSync) pull(ctx context.Context) (changed bool, revision [sha256.Size]byte, err error) {
	file, err := readPrivateURLFile(s.urlFile())
	if err != nil {
		return false, revision, fmt.Errorf("read configuration URL file: %w", err)
	}
	remote, err := parseConfigURL(string(file))
	if err != nil {
		return false, revision, err
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
		return false, revision, fmt.Errorf("create configuration request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		var urlErr *url.Error
		for errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return false, revision, fmt.Errorf("download configuration: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false, revision, fmt.Errorf("download configuration: HTTP %d %s", response.StatusCode, http.StatusText(response.StatusCode))
	}
	temp, err := os.CreateTemp(filepath.Dir(s.target), ".config-pull-*")
	if err != nil {
		return false, revision, fmt.Errorf("create temporary configuration: %w", err)
	}
	defer os.Remove(temp.Name())
	_, copyErr := io.Copy(temp, response.Body)
	closeErr := temp.Close()
	if copyErr != nil {
		return false, revision, fmt.Errorf("download configuration body: %w", copyErr)
	}
	if closeErr != nil {
		return false, revision, fmt.Errorf("close temporary configuration: %w", closeErr)
	}
	content, err := os.ReadFile(temp.Name())
	if err != nil {
		return false, revision, err
	}
	if _, err = checkedSyncedConfig(content, s.target); err != nil {
		return false, revision, fmt.Errorf("downloaded configuration failed check: %w", err)
	}
	revision = sha256.Sum256(content)
	current, err := os.ReadFile(s.target)
	if err == nil && bytes.Equal(current, content) {
		return false, revision, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, revision, fmt.Errorf("read current configuration: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return false, revision, err
	}
	if err = os.Rename(temp.Name(), s.target); err != nil {
		return false, revision, fmt.Errorf("replace configuration: %w", err)
	}
	return true, revision, nil
}

func checkedSyncedConfig(content []byte, path string) (*OptionsEntry, error) {
	ctx := service.ExtendContext(globalCtx)
	service.MustRegister[deprecated.Manager](ctx, deprecated.NewStderrManager(log.NewNOPFactory().Logger()))
	entry, err := decodeConfigAt(ctx, content, path)
	if err != nil {
		return nil, err
	}
	options := entry.options
	var quietLog option.LogOptions
	if options.Log != nil {
		quietLog = *options.Log
	}
	quietLog.Output = os.DevNull
	options.Log = &quietLog
	return entry, checkOptions(ctx, options)
}

func redactConfigError(err error, remotes ...*url.URL) error {
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
	message := strings.NewReplacer(pairs...).Replace(err.Error())
	if message == err.Error() {
		return err
	}
	return errors.New(message)
}

func (s configSync) watch(ctx context.Context, requests <-chan struct{}, reload chan<- struct{}) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		var requested bool
		select {
		case <-ctx.Done():
			return
		case <-requests:
			requested = true
		case <-ticker.C:
		}
		changed, _, err := s.pull(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Error("sync configuration: ", err)
		}
		if changed || requested {
			requestConfigReload(reload)
		}
	}
}

func requestConfigReload(reload chan<- struct{}) {
	select {
	case reload <- struct{}{}:
	default:
	}
}

func writePrivateURLFile(path string, value string) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".config-url-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = protectURLFile(file); err == nil {
		_, err = file.WriteString(value + "\n")
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
