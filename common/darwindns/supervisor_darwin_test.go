//go:build darwin

package darwindns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRunner struct {
	access   sync.Mutex
	services []string
	dns      map[string][]string
	calls    [][]string
}

func (r *fakeRunner) Output(name string, args ...string) ([]byte, error) {
	r.access.Lock()
	defer r.access.Unlock()
	if name != networkSetupPath || len(args) == 0 {
		return nil, fmt.Errorf("unexpected output command: %s %v", name, args)
	}
	switch args[0] {
	case "-listallnetworkservices":
		var output bytes.Buffer
		output.WriteString("An asterisk (*) denotes that a network service is disabled.\n")
		for _, service := range r.services {
			output.WriteString(service)
			output.WriteByte('\n')
		}
		return output.Bytes(), nil
	case "-getdnsservers":
		servers := r.dns[args[1]]
		if len(servers) == 0 {
			return []byte("There aren't any DNS Servers set on " + args[1] + ".\n"), nil
		}
		return []byte(strings.Join(servers, "\n") + "\n"), nil
	default:
		return nil, fmt.Errorf("unexpected output command: %s %v", name, args)
	}
}

func (r *fakeRunner) Run(name string, args ...string) error {
	r.access.Lock()
	defer r.access.Unlock()
	if name != networkSetupPath || len(args) < 3 || args[0] != "-setdnsservers" {
		return fmt.Errorf("unexpected run command: %s %v", name, args)
	}
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	if len(args) == 3 && args[2] == automaticDNSValue {
		r.dns[args[1]] = nil
	} else {
		r.dns[args[1]] = append([]string(nil), args[2:]...)
	}
	return nil
}

func TestRunAppliesAndRestoresDNSOnParentEOF(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	runner := &fakeRunner{
		services: []string{"Wi-Fi", "USB LAN"},
		dns: map[string][]string{
			"Wi-Fi":   {"192.0.2.53"},
			"USB LAN": nil,
		},
	}
	supervisor, err := NewSupervisor([]string{"172.19.0.2", "fdfe:dcba:9876::2"}, statePath)
	if err != nil {
		t.Fatal(err)
	}
	supervisor.runner = runner
	supervisor.pollInterval = time.Hour
	var ready bytes.Buffer
	if err := supervisor.Run(context.Background(), bytes.NewReader(nil), &ready); err != nil {
		t.Fatal(err)
	}
	if ready.String() != "ready\n" {
		t.Fatalf("ready = %q", ready.String())
	}
	if got := runner.dns["Wi-Fi"]; !reflect.DeepEqual(got, []string{"192.0.2.53"}) {
		t.Fatalf("Wi-Fi DNS after restore = %v", got)
	}
	if got := runner.dns["USB LAN"]; len(got) != 0 {
		t.Fatalf("USB LAN DNS after restore = %v", got)
	}
	wantCalls := [][]string{
		{networkSetupPath, "-setdnsservers", "Wi-Fi", "172.19.0.2", "fdfe:dcba:9876::2"},
		{networkSetupPath, "-setdnsservers", "USB LAN", "172.19.0.2", "fdfe:dcba:9876::2"},
		{networkSetupPath, "-setdnsservers", "USB LAN", "Empty"},
		{networkSetupPath, "-setdnsservers", "Wi-Fi", "192.0.2.53"},
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, wantCalls)
	}
}

func TestRunRepairsStaleStateBeforeTakingOwnership(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	content, err := json.Marshal(persistedState{Services: map[string][]string{"Wi-Fi": {"192.0.2.53"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{
		services: []string{"Wi-Fi"},
		dns:      map[string][]string{"Wi-Fi": {"172.19.0.2"}},
	}
	supervisor, err := NewSupervisor([]string{"172.19.0.2"}, statePath)
	if err != nil {
		t.Fatal(err)
	}
	supervisor.runner = runner
	supervisor.pollInterval = time.Hour
	if err := supervisor.Run(context.Background(), bytes.NewReader(nil), nil); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{networkSetupPath, "-setdnsservers", "Wi-Fi", "192.0.2.53"},
		{networkSetupPath, "-setdnsservers", "Wi-Fi", "172.19.0.2"},
		{networkSetupPath, "-setdnsservers", "Wi-Fi", "192.0.2.53"},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestReconcileAdoptsNewNetworkService(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	runner := &fakeRunner{
		services: []string{"Wi-Fi"},
		dns:      map[string][]string{"Wi-Fi": nil, "USB LAN": {"198.51.100.53"}},
	}
	supervisor, err := NewSupervisor([]string{"172.19.0.2"}, statePath)
	if err != nil {
		t.Fatal(err)
	}
	supervisor.runner = runner
	if err := supervisor.reconcile(); err != nil {
		t.Fatal(err)
	}
	runner.services = append(runner.services, "USB LAN")
	if err := supervisor.reconcile(); err != nil {
		t.Fatal(err)
	}
	if got := runner.dns["USB LAN"]; !reflect.DeepEqual(got, []string{"172.19.0.2"}) {
		t.Fatalf("USB LAN DNS = %v", got)
	}
	if err := supervisor.restoreAll(); err != nil {
		t.Fatal(err)
	}
	if got := runner.dns["USB LAN"]; !reflect.DeepEqual(got, []string{"198.51.100.53"}) {
		t.Fatalf("USB LAN restored DNS = %v", got)
	}
}
