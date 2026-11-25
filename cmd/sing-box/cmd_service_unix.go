//go:build !windows

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func requireServiceAdmin() error {
	if os.Geteuid() != 0 {
		return errors.New("this command requires root; run with sudo")
	}
	return nil
}

func runUnixManagedService(ctx context.Context, s managedService, output *os.File, ready func() error) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return superviseManagedService(ctx, s, output, ready)
}

func startManagedChild(command *exec.Cmd) (func(), error) {
	return nil, command.Start()
}

func signalManagedChild(process *os.Process) error {
	return process.Signal(syscall.SIGTERM)
}

func serviceControlPath(s managedService) string {
	return filepath.Join(s.controlDir(), "control")
}

func listenServiceControl(s managedService) (net.Listener, error) {
	path := serviceControlPath(s)
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSocket == 0 {
		return nil, errors.New("service control path is not a Unix socket")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	directory, err := os.Open(s.controlDir())
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(directory.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = directory.Close()
		return nil, fmt.Errorf("lock service control directory: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = directory.Close()
		}
	}()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("%w (use a shorter -D path if the Unix socket path is too long)", err)
	}
	if err = os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	keep = true
	return &lockedServiceListener{Listener: listener, directory: directory}, nil
}

type lockedServiceListener struct {
	net.Listener
	directory *os.File
}

func (l *lockedServiceListener) Close() error {
	return errors.Join(l.Listener.Close(), l.directory.Close())
}

func dialServiceControl(ctx context.Context, s managedService) (net.Conn, error) {
	if err := checkServiceControlDirectory(s.controlDir()); err != nil {
		return nil, err
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", serviceControlPath(s))
}
