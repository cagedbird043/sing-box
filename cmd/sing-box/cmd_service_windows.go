package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sagernet/sing-box/experimental/deprecated"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/service"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const windowsServiceLogfile = "sing-box.log"

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

func preflightManagedService(s managedService) error { return nil }

func managedServiceLog(s managedService) string {
	return filepath.Join(s.dataDir, windowsServiceLogfile)
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
		if err = systemService.UpdateConfig(config); err != nil {
			return err
		}
		if err = stopWindowsManagedService(systemService); err != nil {
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
	return waitWindowsManagedService(systemService, svc.Running, 135*time.Second)
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
	if _, err = windowsManagedServiceDefinition(systemService); err != nil {
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
	definition, err := windowsManagedServiceDefinition(systemService)
	if err != nil {
		return nil, false, err
	}
	status, err := systemService.Query()
	return definition, status.State == svc.Running, err
}

func windowsManagedServiceDefinition(systemService *mgr.Service) (*managedService, error) {
	config, err := systemService.Config()
	if err != nil {
		return nil, err
	}
	argv, err := windows.DecomposeCommandLine(config.BinaryPathName)
	if err != nil {
		return nil, err
	}
	definition, err := managedServiceFromArgs(systemService.Name, argv)
	return &definition, err
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
		if err != nil || status.State == state {
			return err
		}
		if state == svc.Running && status.State == svc.Stopped {
			return fmt.Errorf("service stopped during startup (exit code %d); see %s in the service data directory", status.Win32ExitCode, windowsServiceLogfile)
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

func reloadManagedService(ctx context.Context, name string) error {
	systemService, err := openWindowsManagedService(name, windows.SERVICE_PAUSE_CONTINUE)
	if err != nil {
		return err
	}
	defer systemService.Close()
	_, err = systemService.Control(svc.ParamChange)
	return err
}

func runPlatform() error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !isService {
		return run(context.Background(), nil, nil)
	}
	return svc.Run(runServiceName, windowsManagedServiceHandler{})
}

type windowsManagedServiceHandler struct{}

func (windowsManagedServiceHandler) Execute(args []string, requests <-chan svc.ChangeRequest, statuses chan<- svc.Status) (bool, uint32) {
	status := svc.Status{State: svc.StartPending, CheckPoint: 1, WaitHint: 10000}
	statuses <- status
	logfile, err := os.OpenFile(windowsServiceLogfile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return false, uint32(windows.ERROR_OPEN_FAILED)
	}
	defer logfile.Close()
	os.Stdout, os.Stderr = logfile, logfile
	factory := log.NewDefaultFactory(context.Background(), log.Formatter{BaseTime: time.Now(), DisableColors: true}, logfile, "", nil, false)
	if err = factory.Start(); err != nil {
		return false, uint32(windows.ERROR_OPEN_FAILED)
	}
	defer factory.Close()
	log.SetStdLogger(factory.Logger())
	service.MustRegister[deprecated.Manager](globalCtx, deprecated.NewStderrManager(log.StdLogger()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pulls := make(chan struct{}, 1)
	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() { finished <- run(ctx, pulls, func() { close(started) }) }()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if status.State == svc.StartPending || status.State == svc.StopPending {
				status.CheckPoint++
				statuses <- status
			}
		case <-started:
			started = nil
			if status.State == svc.StartPending {
				status = svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown | svc.AcceptParamChange}
				statuses <- status
			}
		case err := <-finished:
			if err != nil && ctx.Err() == nil {
				log.Error(err)
				return true, 1
			}
			return false, 0
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				statuses <- status
			case svc.Stop, svc.Shutdown:
				if status.State != svc.StopPending {
					status = svc.Status{State: svc.StopPending, CheckPoint: 1, WaitHint: 10000}
					statuses <- status
					cancel()
				}
			case svc.ParamChange:
				requestConfigReload(pulls)
			}
		}
	}
}
