//go:build !windows

package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

func requireServiceAdmin() error {
	if os.Geteuid() != 0 {
		return errors.New("this command requires root; run with sudo")
	}
	return nil
}

func runPlatform() error {
	ctx := context.Background()
	if runSyncConfig {
		var cancel context.CancelFunc
		ctx, cancel = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer cancel()
	}
	return run(ctx, nil, nil)
}

func writeManagedDefinition(path string, content []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".sing-box-service-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(content); err == nil {
		err = file.Chmod(0o644)
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
