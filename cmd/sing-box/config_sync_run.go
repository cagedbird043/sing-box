package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	serviceStartupWindow = 2 * time.Second
	serviceStopTimeout   = 12 * time.Second
	serviceUpdateTimeout = 5 * time.Minute
)

var errConfigRollback = errors.New("could not restore the previous configuration")

type syncResult struct {
	Changed bool
	Error   string
}

type managedSupervisor struct {
	managedService
	output *os.File
	log    *log.Logger
	child  *managedChild
}

type managedChild struct {
	process *os.Process
	done    chan struct{}
	err     error
}

func superviseManagedService(ctx context.Context, definition managedService, output *os.File, ready func() error) (returnErr error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	listener, err := listenServiceControl(definition)
	if err != nil {
		return fmt.Errorf("listen for configuration update requests: %w", err)
	}
	defer listener.Close()
	s := managedSupervisor{managedService: definition, output: output, log: log.New(output, "", log.LstdFlags)}
	defer func() { returnErr = errors.Join(returnErr, s.stop()) }()
	_, err = s.sync(ctx)
	if ctx.Err() != nil {
		return nil
	}
	if errors.Is(err, errConfigRollback) {
		return err
	}
	if err != nil {
		s.log.Print("initial configuration sync: ", err)
	}
	if s.child == nil {
		if pathErr := checkServiceFile(s.configPath()); pathErr != nil {
			return errors.Join(err, pathErr)
		}
		if startErr := s.start(); startErr != nil {
			return errors.Join(err, startErr)
		}
	}
	if ready != nil {
		if err := ready(); err != nil {
			return err
		}
	}
	type accepted struct {
		conn net.Conn
		err  error
	}
	requests := make(chan accepted)
	go func() {
		for {
			conn, err := listener.Accept()
			select {
			case requests <- accepted{conn, err}:
			case <-ctx.Done():
				if conn != nil {
					_ = conn.Close()
				}
				return
			}
			if err != nil {
				return
			}
		}
	}()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.child.done:
			return s.child.exitError("running sing-box process exited")
		case <-ticker.C:
			if _, err := s.sync(ctx); err != nil && ctx.Err() == nil {
				s.log.Print("scheduled configuration sync: ", err)
			}
		case update := <-requests:
			if update.err != nil {
				return fmt.Errorf("accept configuration update request: %w", update.err)
			}
			changed, err := s.sync(ctx)
			result := syncResult{Changed: changed}
			if err != nil {
				result.Error = err.Error()
				if ctx.Err() == nil {
					s.log.Print("requested configuration sync: ", err)
				}
			}
			_ = update.conn.SetWriteDeadline(time.Now().Add(time.Second))
			_ = json.NewEncoder(update.conn).Encode(result)
			_ = update.conn.Close()
		}
		if s.child == nil {
			if ctx.Err() != nil {
				return nil
			}
			return errors.New("managed sing-box process could not be restored")
		}
	}
}

func (s *managedSupervisor) sync(ctx context.Context) (bool, error) {
	candidate, hasPrevious, err := s.pull(ctx)
	if err != nil || candidate == "" {
		return false, err
	}
	defer os.Remove(candidate)
	if err = ctx.Err(); err != nil {
		return false, err
	}
	path := s.configPath()
	backup := ""
	if hasPrevious {
		backup = candidate + ".previous"
		// Retain the old inode before interrupting the child, so rollback
		// does not depend on being able to write the old bytes again.
		if err = os.Link(path, backup); err != nil {
			return false, fmt.Errorf("save previous configuration: %w", err)
		}
		defer func() {
			if backup != "" {
				_ = os.Remove(backup)
			}
		}()
	}
	hadChild := s.child != nil
	if err = s.stop(); err != nil {
		if s.child == nil && hadChild && ctx.Err() == nil {
			err = errors.Join(err, s.start())
		}
		return false, err
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	if err = os.Rename(candidate, path); err != nil {
		err = fmt.Errorf("replace configuration: %w", err)
		if hadChild {
			err = errors.Join(err, s.start())
		}
		return false, err
	}
	if err = s.start(); err == nil {
		return true, nil
	}
	if backup == "" {
		return false, errors.Join(err, os.Remove(path))
	}
	if restoreErr := os.Rename(backup, path); restoreErr != nil {
		preserved := backup
		backup = ""
		return false, errors.Join(err, fmt.Errorf("%w (saved at %s): %v", errConfigRollback, preserved, restoreErr))
	}
	if !hadChild || ctx.Err() != nil {
		return false, fmt.Errorf("%w; restored previous configuration", err)
	}
	if restoreErr := s.start(); restoreErr != nil {
		return false, errors.Join(err, fmt.Errorf("restart previous configuration: %w", restoreErr))
	}
	return false, fmt.Errorf("%w; restored previous configuration", err)
}

func (s *managedSupervisor) start() error {
	command := exec.Command(s.executable, s.childArgs("run", s.configPath())...)
	command.Dir = s.dataDir
	command.Stdout, command.Stderr = s.output, s.output
	var filtered *childLog
	if value, err := readPrivateURLFile(s.urlPath()); err == nil {
		if remote, err := parseConfigURL(string(value)); err == nil {
			filtered = &childLog{output: s.output, redact: configURLRedactor(remote)}
			command.Stdout, command.Stderr = filtered, filtered
			command.WaitDelay = 2 * time.Second
		}
	}
	cleanup, err := startManagedChild(command)
	if err != nil {
		return fmt.Errorf("launch sing-box: %w", err)
	}
	child := &managedChild{process: command.Process, done: make(chan struct{})}
	go func() {
		child.err = command.Wait()
		if filtered != nil && len(filtered.pending) != 0 {
			_, err := filtered.redact.WriteString(filtered.output, string(filtered.pending))
			child.err = errors.Join(child.err, err)
		}
		if cleanup != nil {
			cleanup()
		}
		close(child.done)
	}()
	// Only detect early startup exits, not readiness or ongoing liveness.
	timer := time.NewTimer(serviceStartupWindow)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-child.done:
		return child.exitError("sing-box exited during startup")
	}
	select {
	case <-child.done:
		return child.exitError("sing-box exited during startup")
	default:
	}
	s.child = child
	s.log.Printf("started sing-box process %d", child.process.Pid)
	return nil
}

type childLog struct {
	output  io.Writer
	redact  *strings.Replacer
	pending []byte
}

func (l *childLog) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) != 0 {
		end := bytes.IndexByte(p, '\n') + 1
		if end == 0 {
			l.pending = append(l.pending, p...)
			break
		}
		line := p[:end]
		if len(l.pending) != 0 {
			line = append(l.pending, line...)
			l.pending = l.pending[:0]
		}
		if _, err := l.redact.WriteString(l.output, string(line)); err != nil {
			return total - len(p), err
		}
		p = p[end:]
	}
	return total, nil
}

func (c *managedChild) exitError(message string) error {
	if c.err != nil {
		return fmt.Errorf("%s: %w", message, c.err)
	}
	return errors.New(message)
}

func (s *managedSupervisor) stop() error {
	child := s.child
	if child == nil {
		return nil
	}
	select {
	case <-child.done:
		s.child = nil
		return nil
	default:
	}
	err := signalManagedChild(child.process)
	if err == nil || errors.Is(err, os.ErrProcessDone) {
		timer := time.NewTimer(serviceStopTimeout)
		defer timer.Stop()
		select {
		case <-child.done:
			s.child = nil
			if child.err != nil {
				return child.exitError("sing-box failed to shut down cleanly")
			}
			return nil
		case <-timer.C:
			err = errors.New("timed out waiting for sing-box to shut down")
		}
	} else {
		err = fmt.Errorf("signal sing-box to shut down: %w", err)
	}
	if killErr := child.process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
		return errors.Join(err, fmt.Errorf("force-stop sing-box: %w", killErr))
	}
	<-child.done
	s.child = nil
	return err
}

func requestServiceSync(ctx context.Context, definition managedService) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, serviceUpdateTimeout)
	defer cancel()
	conn, err := dialServiceControl(ctx, definition)
	if err != nil {
		return false, fmt.Errorf("connect to managed service: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	var result syncResult
	if err = json.NewDecoder(io.LimitReader(conn, 256*1024)).Decode(&result); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return false, fmt.Errorf("receive configuration sync result: %w", err)
	}
	if result.Error != "" {
		return false, errors.New(result.Error)
	}
	return result.Changed, nil
}

func (s managedService) logPath() string {
	return filepath.Join(s.controlDir(), "service.log")
}

func (s managedService) openLog() (*os.File, error) {
	path := s.logPath()
	if err := checkServiceFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if err = protectURLFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
