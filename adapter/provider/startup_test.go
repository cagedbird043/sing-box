package provider

import (
	"context"
	"errors"
	"net"
	"testing"
	"testing/synctest"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type startupTestOutbound struct {
	outbound.Adapter
	N.Dialer
	requests chan struct{}
	release  chan struct{}
}

func (o *startupTestOutbound) DialContext(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
	o.requests <- struct{}{}
	select {
	case <-o.release:
		return nil, errors.New("probe released")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func newStartupTestAdapter(t *testing.T) (*Adapter, *startupTestOutbound) {
	t.Helper()
	factory := log.NewNOPFactory()
	a := NewAdapter(context.Background(), nil, nil, nil, factory, factory.Logger(), "test", "local", option.ProviderHealthCheckOptions{Enabled: true})
	node := &startupTestOutbound{
		Adapter:  outbound.NewAdapter("startup-test", "test/node", []string{"tcp"}, nil),
		requests: make(chan struct{}, 10),
		release:  make(chan struct{}),
	}
	a.outbounds = []adapter.Outbound{node}
	t.Cleanup(func() {
		close(node.release)
		_ = a.Close()
	})
	return &a, node
}

func assertNoStartupProbe(t *testing.T, node *startupTestOutbound) {
	t.Helper()
	select {
	case <-node.requests:
		t.Fatal("health check started before Started")
	default:
	}
}

// Plan 11: the initial periodic check must not occupy the lifecycle action lock.
func TestProviderInitialHealthCheckWaitsForStarted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, node := newStartupTestAdapter(t)
		if err := a.Start(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		assertNoStartupProbe(t, node)
		for _, stage := range []adapter.StartStage{adapter.StartStatePostStart, adapter.StartStateStarted} {
			done := make(chan error, 1)
			go func() { done <- a.Advance(stage) }()
			synctest.Wait()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			default:
				t.Fatalf("Advance(%v) blocked on a background health check", stage)
			}
		}
		select {
		case <-node.requests:
		default:
			t.Fatal("initial health check did not start after Started")
		}
		a.MarkStarted() // Repeated delivery is safe.
	})
}

// Plan 12: an update between Start and Started also defers its automatic check.
func TestProviderFinishUpdateHealthCheckWaitsForStarted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, node := newStartupTestAdapter(t)
		// Let the periodic loop exit so only FinishUpdate can initiate a probe.
		a.enabled = false
		if err := a.Start(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		a.enabled = true
		a.StartUpdate()
		a.FinishUpdate()
		synctest.Wait()
		assertNoStartupProbe(t, node)
		if err := a.TryStartUpdate(); err != nil {
			t.Fatalf("waiting automatic check held the action lock: %v", err)
		}
		// Release directly: a second FinishUpdate would schedule another check.
		a.actionAccess.Unlock()
		a.MarkStarted()
		synctest.Wait()
		select {
		case <-node.requests:
		default:
			t.Fatal("automatic health check did not start after Started")
		}
	})
}

// Plan 14: cancellation releases both the initial loop and update-triggered waiters.
func TestProviderCloseBeforeStartedReleasesHealthChecks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, node := newStartupTestAdapter(t)
		if err := a.Start(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		a.StartUpdate()
		a.FinishUpdate()
		waited := make(chan bool, 1)
		go func() { waited <- a.WaitStarted(a.ctx) }()
		synctest.Wait()
		if err := a.Close(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if <-waited {
			t.Fatal("cancelled readiness wait succeeded")
		}
		assertNoStartupProbe(t, node)
		// Even a late mark must not revive cancelled work.
		a.MarkStarted()
		synctest.Wait()
		assertNoStartupProbe(t, node)
	})
}

func TestProviderWaitStartedDoesNotTakeProviderLocks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, _ := newStartupTestAdapter(t)
		a.actionAccess.Lock()
		a.stateAccess.Lock()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan bool, 1)
		go func() { done <- a.WaitStarted(ctx) }()
		synctest.Wait()
		cancel()
		synctest.Wait()
		select {
		case ready := <-done:
			if ready {
				t.Error("cancelled wait succeeded")
			}
		default:
			t.Error("readiness wait blocked on provider locks")
		}
		a.MarkStarted()
		if !a.WaitStarted(context.Background()) {
			t.Error("marked readiness wait failed")
		}
		a.stateAccess.Unlock()
		a.actionAccess.Unlock()
	})
}
