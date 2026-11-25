package main

import (
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

func TestWindowsManagedService(t *testing.T) {
	definition := managedService{
		name: "isolated", executable: `C:\Program Files\sing-box\sing-box.exe`, configDir: `C:\ProgramData\isolated config`,
		dataDir: `C:\ProgramData\isolated data`, interval: 5 * time.Minute,
	}
	config := windowsManagedServiceConfig(definition)
	if config.StartType != mgr.StartAutomatic || !config.DelayedAutoStart || config.ServiceStartName != "LocalSystem" || config.ServiceType != windows.SERVICE_WIN32_OWN_PROCESS {
		t.Errorf("unexpected Windows service configuration: %+v", config)
	}
	argv, err := windows.DecomposeCommandLine(config.BinaryPathName)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := managedServiceFromArgs(definition.name, argv)
	if err != nil || parsed != definition {
		t.Fatalf("Windows SCM args %q recovered %+v: %v", argv, parsed, err)
	}
}

func TestPrivateURLDescriptor(t *testing.T) {
	for _, tc := range []struct {
		sddl    string
		private bool
	}{
		{"O:BA" + privateURLACL, true},
		{"O:SYD:P(A;;FA;;;BA)(A;;FA;;;SY)", true},
		{"O:BUD:P(A;;FA;;;SY)(A;;FA;;;BA)", false},
		{"O:BAD:(A;;FA;;;SY)(A;;FA;;;BA)", false},
		{"O:BAD:P(A;;FA;;;SY)(A;;FA;;;BU)", false},
	} {
		descriptor, err := windows.SecurityDescriptorFromString(tc.sddl)
		if err != nil {
			t.Fatal(err)
		}
		if private := restrictedURLDescriptor(descriptor); private != tc.private {
			t.Errorf("descriptor %q is restricted: got %t, want %t", tc.sddl, private, tc.private)
		}
	}
}
