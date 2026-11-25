package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/tailscale/go-winio"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

var (
	consoleLibrary = windows.NewLazySystemDLL("kernel32.dll")
	allocConsole   = consoleLibrary.NewProc("AllocConsole")
	freeConsole    = consoleLibrary.NewProc("FreeConsole")
)

func requireServiceAdmin() error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("this command requires an elevated Administrator session")
	}
	return nil
}

func managedServiceDefaults() (string, string, string, error) {
	path, err := managedExecutable()
	if err != nil {
		return "", "", "", err
	}
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return "", "", "", err
	}
	return path, filepath.Join(programData, "sing-box"), filepath.Join(programData, "sing-box", "data"), nil
}

func managedServiceLog(s managedService) string {
	return s.logPath()
}

func windowsManagedServiceConfig(s managedService) mgr.Config {
	return mgr.Config{
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:        mgr.StartAutomatic,
		ErrorControl:     mgr.ErrorNormal,
		BinaryPathName:   windows.ComposeCommandLine(append([]string{s.executable}, s.args()...)),
		ServiceStartName: "LocalSystem",
		DisplayName:      s.name,
		Description:      "sing-box managed service",
		DelayedAutoStart: true,
	}
}

func installManagedService(s managedService) error {
	manager, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	config := windowsManagedServiceConfig(s)
	systemService, err := manager.OpenService(s.name)
	created := errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST)
	if created {
		systemService, err = manager.CreateService(s.name, s.executable, config, s.args()...)
	}
	if err != nil {
		return err
	}
	defer systemService.Close()
	if !created {
		if _, err = windowsManagedServiceDefinition(systemService, false); err != nil {
			return err
		}
		if err = stopWindowsManagedService(systemService); err != nil {
			return err
		}
		if err = systemService.UpdateConfig(config); err != nil {
			return err
		}
	}
	if err = systemService.SetRecoveryActions([]mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: 3 * time.Second}}, 0); err == nil {
		err = systemService.SetRecoveryActionsOnNonCrashFailures(true)
	}
	if err != nil {
		if created {
			_ = systemService.Delete()
		}
		return err
	}
	if err = systemService.Start(); err != nil {
		return err
	}
	return waitWindowsManagedService(systemService, svc.Running, 155*time.Second)
}

func uninstallManagedService(name string) error {
	systemService, err := openWindowsManagedService(name, windows.SERVICE_QUERY_CONFIG|windows.SERVICE_QUERY_STATUS|windows.SERVICE_STOP|windows.DELETE)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil
	}
	if err != nil {
		return err
	}
	defer systemService.Close()
	if _, err = windowsManagedServiceDefinition(systemService, true); err != nil {
		return err
	}
	if err = stopWindowsManagedService(systemService); err != nil {
		return err
	}
	return systemService.Delete()
}

func lookupManagedService(name string) (*managedService, bool, error) {
	systemService, err := openWindowsManagedService(name, windows.SERVICE_QUERY_CONFIG|windows.SERVICE_QUERY_STATUS)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer systemService.Close()
	definition, err := windowsManagedServiceDefinition(systemService, false)
	if err != nil {
		return nil, false, err
	}
	status, err := systemService.Query()
	return definition, status.State == svc.Running, err
}

func windowsManagedServiceDefinition(systemService *mgr.Service, uninstall bool) (*managedService, error) {
	config, err := systemService.Config()
	if err != nil {
		return nil, err
	}
	argv, err := windows.DecomposeCommandLine(config.BinaryPathName)
	if err != nil {
		return nil, err
	}
	return managedServiceFromArgs(systemService.Name, argv, uninstall)
}

func stopWindowsManagedService(systemService *mgr.Service) error {
	status, err := systemService.Query()
	if err != nil || status.State == svc.Stopped {
		return err
	}
	if status.State != svc.StopPending {
		if _, err = systemService.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return err
		}
	}
	return waitWindowsManagedService(systemService, svc.Stopped, 30*time.Second)
}

func waitWindowsManagedService(systemService *mgr.Service, state svc.State, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := systemService.Query()
		if err != nil {
			return err
		}
		if status.State == state {
			if state == svc.Stopped && status.Win32ExitCode != 0 {
				return fmt.Errorf("service failed to stop cleanly (Windows exit code %d)", status.Win32ExitCode)
			}
			return nil
		}
		if state == svc.Running && status.State == svc.Stopped {
			return fmt.Errorf("service stopped during startup (Windows exit code %d); see the log reported by service status", status.Win32ExitCode)
		}
		select {
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for Windows service state %d (current state %d)", state, status.State)
		case <-ticker.C:
		}
	}
}

func openWindowsManagedService(name string, access uint32) (*mgr.Service, error) {
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, err
	}
	defer windows.CloseServiceHandle(manager)
	namePointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	handle, err := windows.OpenService(manager, namePointer, access)
	if err != nil {
		return nil, err
	}
	return &mgr.Service{Name: name, Handle: handle}, nil
}

func servicePipeName(s managedService) string {
	return `\\.\pipe\ProtectedPrefix\Administrators\sing-box-service-` + strings.ToLower(s.name)
}

func listenServiceControl(s managedService) (net.Listener, error) {
	return winio.ListenPipe(servicePipeName(s), &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;SY)(A;;GA;;;BA)",
		OutputBufferSize:   64 * 1024,
	})
}

func dialServiceControl(ctx context.Context, s managedService) (net.Conn, error) {
	return winio.DialPipeContext(ctx, servicePipeName(s))
}

func startManagedChild(command *exec.Cmd) (func(), error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	if err = command.Start(); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_SET_QUOTA, false, uint32(command.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, process)
		_ = windows.CloseHandle(process)
	}
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("isolate sing-box process: %w", err)
	}
	return func() { _ = windows.CloseHandle(job) }, nil
}

func signalManagedChild(process *os.Process) error {
	// Go delivers CTRL_BREAK_EVENT as os.Interrupt to the normal run command.
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(process.Pid))
}

func runManagedService(ctx context.Context, s managedService) error {
	return svc.Run(s.name, windowsManagedServiceHandler{s})
}

type windowsManagedServiceHandler struct {
	managedService
}

func (s windowsManagedServiceHandler) Execute(args []string, requests <-chan svc.ChangeRequest, statuses chan<- svc.Status) (bool, uint32) {
	status := svc.Status{State: svc.StartPending, CheckPoint: 1, WaitHint: 10000}
	statuses <- status
	if err := s.checkPaths(false); err != nil {
		return false, uint32(windows.ERROR_ACCESS_DENIED)
	}
	output, err := s.openLog()
	if err != nil {
		return false, uint32(windows.ERROR_OPEN_FAILED)
	}
	defer output.Close()
	// SCM starts the supervisor without a console. Allocate one so it
	// can deliver CTRL_BREAK_EVENT to the child's separate process group.
	if result, _, err := allocConsole.Call(); result == 0 {
		_, _ = fmt.Fprintln(output, "allocate service console:", err)
		return false, uint32(windows.ERROR_OPEN_FAILED)
	}
	defer freeConsole.Call()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- superviseManagedService(ctx, s.managedService, output, func() error {
			close(ready)
			return nil
		})
	}()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if status.State == svc.StartPending || status.State == svc.StopPending {
				status.CheckPoint++
				statuses <- status
			}
		case <-ready:
			ready = nil
			if status.State == svc.StartPending {
				status = svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
				statuses <- status
			}
		case err := <-finished:
			if err != nil {
				_, _ = fmt.Fprintln(output, err)
				return true, 1
			}
			return false, 0
		case request, ok := <-requests:
			if !ok {
				requests = nil
				cancel()
				continue
			}
			switch request.Cmd {
			case svc.Interrogate:
				statuses <- status
			case svc.Stop, svc.Shutdown:
				if status.State != svc.StopPending {
					status = svc.Status{State: svc.StopPending, CheckPoint: 1, WaitHint: 10000}
					statuses <- status
					cancel()
				}
			}
		}
	}
}
