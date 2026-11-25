package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"

	"github.com/sagernet/sing-box/log"
)

type runConfigSync struct {
	configSync
	revision  [sha256.Size]byte
	readyPath string
	reload    chan struct{}
}

func prepareRunConfigSync(ctx context.Context) (*runConfigSync, error) {
	if !runSyncConfig {
		return nil, nil
	}
	if len(configPaths) != 1 || len(configDirectories) != 0 || configPaths[0] == "stdin" {
		return nil, errors.New("configuration sync requires a single configuration file")
	}
	if runSyncInterval <= 0 {
		return nil, errors.New("configuration sync interval must be positive")
	}
	if err := validateServiceName(runServiceName); err != nil {
		return nil, err
	}
	s := &runConfigSync{
		configSync: configSync{target: configPaths[0], interval: runSyncInterval},
		readyPath:  configReadyPath("", runServiceName),
	}
	if err := os.Remove(s.readyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("clear configuration revision: %w", err)
	}
	if _, _, err := s.pull(ctx); err != nil {
		if _, statErr := os.Stat(s.target); statErr != nil {
			return nil, fmt.Errorf("initial configuration sync: %w", err)
		}
		log.Warn("initial configuration sync: ", err)
	}
	return s, nil
}

func (s *runConfigSync) startWatch(ctx context.Context, pulls <-chan struct{}, content []byte) func() {
	s.revision = sha256.Sum256(content)
	s.reload = make(chan struct{}, 1)
	watchCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.watch(watchCtx, pulls, s.reload)
	}()
	return func() {
		cancel()
		<-done
		_ = os.Remove(s.readyPath)
	}
}

func (s *runConfigSync) next() (*OptionsEntry, error) {
	content, err := os.ReadFile(s.target)
	if err != nil {
		return nil, err
	}
	revision := sha256.Sum256(content)
	if revision == s.revision {
		return nil, nil
	}
	entry, err := checkedSyncedConfig(content, s.target)
	if err != nil {
		return nil, err
	}
	s.revision = revision
	return entry, nil
}

func (s *runConfigSync) publish() {
	if err := writeConfigRevision(s.readyPath, s.revision); err != nil {
		log.Error("publish configuration revision: ", err)
	}
}

func (s *runConfigSync) redact(err error) error {
	if s == nil || err == nil {
		return err
	}
	data, readErr := readPrivateURLFile(s.urlFile())
	if readErr != nil {
		return err
	}
	remote, parseErr := parseConfigURL(string(data))
	if parseErr != nil {
		return err
	}
	return redactConfigError(err, remote)
}
