//go:build darwin

package darwindns

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	DefaultStatePath    = "/var/run/sing-box-darwin-dns.json"
	networkSetupPath    = "/usr/sbin/networksetup"
	defaultPollInterval = 2 * time.Second
	automaticDNSValue   = "Empty"
)

type CommandRunner interface {
	Output(name string, args ...string) ([]byte, error)
	Run(name string, args ...string) error
}

type systemRunner struct{}

func (systemRunner) Output(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

func (systemRunner) Run(name string, args ...string) error {
	command := exec.Command(name, args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

type persistedState struct {
	Services map[string][]string `json:"services"`
}

type Supervisor struct {
	runner       CommandRunner
	statePath    string
	pollInterval time.Duration
	dnsServers   []string

	access    sync.Mutex
	originals map[string][]string
}

func NewSupervisor(dnsServers []string, statePath string) (*Supervisor, error) {
	if len(dnsServers) == 0 {
		return nil, errors.New("missing TUN DNS server")
	}
	for _, server := range dnsServers {
		if strings.TrimSpace(server) == "" {
			return nil, errors.New("invalid empty TUN DNS server")
		}
	}
	if statePath == "" {
		statePath = DefaultStatePath
	}
	return &Supervisor{
		runner:       systemRunner{},
		statePath:    statePath,
		pollInterval: defaultPollInterval,
		dnsServers:   append([]string(nil), dnsServers...),
		originals:    make(map[string][]string),
	}, nil
}

// Run owns macOS DNS while the parent keeps parent open. EOF means the parent
// exited or released ownership. Original per-service DNS settings are restored
// before Run returns. A persisted snapshot also repairs an interrupted prior run.
func (s *Supervisor) Run(ctx context.Context, parent io.Reader, ready io.Writer) error {
	lock, err := acquireLock(s.statePath + ".lock")
	if err != nil {
		return err
	}
	defer releaseLock(lock)
	if err := s.restoreStaleState(); err != nil {
		return err
	}
	if err := s.reconcile(); err != nil {
		_ = s.restoreAll()
		return err
	}
	if ready != nil {
		if _, err := io.WriteString(ready, "ready\n"); err != nil {
			_ = s.restoreAll()
			return fmt.Errorf("report Darwin DNS readiness: %w", err)
		}
	}

	parentClosed := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, parent)
		close(parentClosed)
	}()

	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return s.restoreAll()
		case <-parentClosed:
			return s.restoreAll()
		case <-ticker.C:
			if err := s.reconcile(); err != nil {
				_ = s.restoreAll()
				return err
			}
		}
	}
}

func (s *Supervisor) reconcile() error {
	services, err := listNetworkServices(s.runner)
	if err != nil {
		return err
	}

	s.access.Lock()
	defer s.access.Unlock()
	for _, service := range services {
		current, err := getDNS(s.runner, service)
		if err != nil {
			return err
		}
		if _, managed := s.originals[service]; !managed {
			s.originals[service] = current
			if err := s.saveStateLocked(); err != nil {
				delete(s.originals, service)
				return err
			}
		}
		if !slices.Equal(current, s.dnsServers) {
			if err := setDNS(s.runner, service, s.dnsServers); err != nil {
				return err
			}
		}
	}
	return nil
}

func acquireLock(path string) (*os.File, error) {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open Darwin DNS lock: %w", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("another Darwin DNS supervisor is active: %w", err)
	}
	return lock, nil
}

func releaseLock(lock *os.File) {
	_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	_ = lock.Close()
}

func (s *Supervisor) restoreStaleState() error {
	state, err := readState(s.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := restoreAvailableServices(s.runner, state.Services); err != nil {
		return fmt.Errorf("restore stale Darwin DNS state: %w", err)
	}
	if err := os.Remove(s.statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale Darwin DNS state: %w", err)
	}
	return nil
}

func (s *Supervisor) restoreAll() error {
	s.access.Lock()
	defer s.access.Unlock()
	if len(s.originals) == 0 {
		return nil
	}
	if err := restoreAvailableServices(s.runner, s.originals); err != nil {
		return err
	}
	clear(s.originals)
	if err := os.Remove(s.statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove Darwin DNS state: %w", err)
	}
	return nil
}

func (s *Supervisor) saveStateLocked() error {
	content, err := json.Marshal(persistedState{Services: s.originals})
	if err != nil {
		return fmt.Errorf("encode Darwin DNS state: %w", err)
	}
	directory := filepath.Dir(s.statePath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create Darwin DNS state directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".sing-box-darwin-dns-*")
	if err != nil {
		return fmt.Errorf("create Darwin DNS state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure Darwin DNS state: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write Darwin DNS state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync Darwin DNS state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close Darwin DNS state: %w", err)
	}
	if err := os.Rename(temporaryPath, s.statePath); err != nil {
		return fmt.Errorf("publish Darwin DNS state: %w", err)
	}
	return nil
}

func readState(path string) (persistedState, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return persistedState{}, err
	}
	var state persistedState
	if err := json.Unmarshal(content, &state); err != nil {
		return persistedState{}, fmt.Errorf("decode Darwin DNS state: %w", err)
	}
	if state.Services == nil {
		return persistedState{}, errors.New("invalid empty Darwin DNS state")
	}
	return state, nil
}

func restoreAvailableServices(runner CommandRunner, services map[string][]string) error {
	available, err := listNetworkServices(runner)
	if err != nil {
		return err
	}
	availableSet := make(map[string]struct{}, len(available))
	for _, service := range available {
		availableSet[service] = struct{}{}
	}
	restorable := make(map[string][]string, len(services))
	for service, servers := range services {
		if _, loaded := availableSet[service]; loaded {
			restorable[service] = servers
		}
	}
	return restoreServices(runner, restorable)
}

func restoreServices(runner CommandRunner, services map[string][]string) error {
	names := make([]string, 0, len(services))
	for service := range services {
		names = append(names, service)
	}
	sort.Strings(names)
	var restoreError error
	for _, service := range names {
		if err := setDNS(runner, service, services[service]); err != nil {
			restoreError = errors.Join(restoreError, err)
		}
	}
	return restoreError
}

func listNetworkServices(runner CommandRunner) ([]string, error) {
	output, err := runner.Output(networkSetupPath, "-listallnetworkservices")
	if err != nil {
		return nil, fmt.Errorf("list macOS network services: %w", err)
	}
	var services []string
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		service := strings.TrimSpace(scanner.Text())
		if service == "" || strings.HasPrefix(service, "An asterisk") || strings.HasPrefix(service, "*") {
			continue
		}
		services = append(services, service)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parse macOS network services: %w", err)
	}
	return services, nil
}

func getDNS(runner CommandRunner, service string) ([]string, error) {
	output, err := runner.Output(networkSetupPath, "-getdnsservers", service)
	if err != nil {
		return nil, fmt.Errorf("get DNS for macOS network service %q: %w", service, err)
	}
	text := strings.TrimSpace(string(output))
	if text == "" || strings.HasPrefix(text, "There aren't any DNS Servers set on") {
		return nil, nil
	}
	servers := strings.Fields(text)
	return servers, nil
}

func setDNS(runner CommandRunner, service string, servers []string) error {
	arguments := []string{"-setdnsservers", service}
	if len(servers) == 0 {
		arguments = append(arguments, automaticDNSValue)
	} else {
		arguments = append(arguments, servers...)
	}
	if err := runner.Run(networkSetupPath, arguments...); err != nil {
		return fmt.Errorf("set DNS for macOS network service %q: %w", service, err)
	}
	return nil
}
