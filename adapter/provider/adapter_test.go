package provider

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/x/list"
	"github.com/sagernet/sing/service"
)

func newTestAdapter() *Adapter {
	return &Adapter{
		providerTag:      "test",
		callbackElements: make(map[*list.Element[adapter.ProviderUpdateCallback]]struct{}),
	}
}

func TestProviderSnapshotIsImmutable(t *testing.T) {
	provider := newTestAdapter()
	updatedAt := time.Unix(123, 0)
	info := adapter.SubscriptionInfo{Upload: 1}
	provider.stateAccess.Lock()
	provider.outbounds = []adapter.Outbound{nil}
	provider.outboundsByTag = map[string]adapter.Outbound{"test/node": nil}
	provider.updatedAt = updatedAt
	provider.subscriptionInfo = &info
	provider.stateAccess.Unlock()

	snapshot := provider.ProviderSnapshot()
	snapshot.Outbounds[0] = nil
	snapshot.SubscriptionInfo.Upload = 2

	current := provider.ProviderSnapshot()
	if current.UpdatedAt != updatedAt {
		t.Fatalf("unexpected updated time: %v", current.UpdatedAt)
	}
	if current.SubscriptionInfo == nil || current.SubscriptionInfo.Upload != 1 {
		t.Fatalf("snapshot mutation leaked into provider state: %+v", current.SubscriptionInfo)
	}

	outbounds := provider.Outbounds()
	outbounds = append(outbounds, nil)
	if len(provider.Outbounds()) != 1 {
		t.Fatal("outbound slice mutation leaked into provider state")
	}
}

func TestProviderActionSingleFlight(t *testing.T) {
	provider := newTestAdapter()
	if err := provider.TryStartUpdate(); err != nil {
		t.Fatal(err)
	}
	if err := provider.TryStartUpdate(); !errors.Is(err, adapter.ErrProviderBusy) {
		t.Fatalf("expected busy error, got %v", err)
	}
	provider.FinishUpdate()
	if err := provider.TryStartUpdate(); err != nil {
		t.Fatalf("action gate was not released: %v", err)
	}
	provider.FinishUpdate()
}

func TestProviderCallbackSeesCommittedState(t *testing.T) {
	provider := newTestAdapter()
	updatedAt := time.Unix(456, 0)
	called := 0
	element := provider.RegisterCallback(func(tag string) error {
		called++
		if tag != "test" {
			t.Fatalf("unexpected provider tag: %s", tag)
		}
		if provider.ProviderSnapshot().UpdatedAt != updatedAt {
			t.Fatal("callback observed stale provider state")
		}
		return nil
	})

	provider.UpdateMetadata(updatedAt, nil)
	provider.UpdateGroups()
	if called != 1 {
		t.Fatalf("expected one callback, got %d", called)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	provider.UpdateGroups()
	provider.UnregisterCallback(element)
	if called != 1 {
		t.Fatalf("callback ran after close: %d", called)
	}
}

func TestManagerProvidersReturnsSnapshot(t *testing.T) {
	manager := &Manager{providers: []adapter.Provider{nil}}
	providers := manager.Providers()
	providers = append(providers, nil)
	if len(manager.Providers()) != 1 {
		t.Fatal("manager provider slice mutation leaked into manager state")
	}
}

type historyTestOutbound struct {
	outbound.Adapter
	N.Dialer
	dial func(context.Context, string, M.Socksaddr) (net.Conn, error)
}

func (o *historyTestOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return o.dial(ctx, network, destination)
}

type historyTestManager struct {
	adapter.OutboundManager
}

func (m *historyTestManager) Outbound(string) (adapter.Outbound, bool) {
	return nil, false
}

func TestProviderGenerationClearsRetiredHistory(t *testing.T) {
	type nodeOptions struct{ Endpoint string }
	registry := outbound.NewRegistry()
	outbound.Register(registry, "history-test", func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options nodeOptions) (adapter.Outbound, error) {
		if options.Endpoint == "invalid" {
			return nil, errors.New("invalid endpoint")
		}
		return &historyTestOutbound{Adapter: outbound.NewAdapter("history-test", tag, []string{"tcp"}, nil)}, nil
	})
	ctx := service.ContextWith[adapter.OutboundRegistry](service.ContextWithDefaultRegistry(context.Background()), registry)
	factory := log.NewNOPFactory()
	provider := NewAdapter(ctx, nil, &historyTestManager{}, nil, factory, factory.Logger(), "test", "local", option.ProviderHealthCheckOptions{})
	provider.history = urltest.NewHistoryStorage()
	t.Cleanup(func() { _ = provider.Close() })
	oldOptions := []option.Outbound{
		{Type: "history-test", Tag: "node", Options: &nodeOptions{Endpoint: "old"}},
		{Type: "history-test", Tag: "removed", Options: &nodeOptions{Endpoint: "removed"}},
	}
	if err := provider.UpdateProvider(nil, oldOptions, nil, nil, time.Unix(1, 0), nil); err != nil {
		t.Fatal(err)
	}
	oldNode, _ := provider.Outbound("test/node")
	oldGeneration := provider.history.OutboundGeneration("test/node")
	prior := &adapter.URLTestHistory{Time: time.Unix(2, 0), Delay: 20}
	for _, tag := range []string{"test/node", "test/removed", "unrelated/node"} {
		for _, target := range []string{urltest.DefaultURL, "https://other.example/check"} {
			provider.history.StoreURLTestHistory(tag, target, provider.history.OutboundGeneration(tag), prior)
		}
	}
	provider.health = map[string]adapter.ProviderHealth{"test/node": {Available: true, Delay: 20, CheckedAt: prior.Time}}
	if err := provider.UpdateProvider(oldOptions, oldOptions, nil, nil, time.Unix(3, 0), nil); err != nil {
		t.Fatal(err)
	}
	if node, _ := provider.Outbound("test/node"); node != oldNode {
		t.Fatal("metadata-only refresh rebuilt the node")
	}
	if provider.history.LoadURLTestHistory("test/node", urltest.DefaultURL) != prior || !provider.ProviderSnapshot().Health["test/node"].Available {
		t.Fatal("metadata-only refresh discarded health")
	}
	invalidOptions := []option.Outbound{{Type: "history-test", Tag: "node", Options: &nodeOptions{Endpoint: "invalid"}}}
	if err := provider.UpdateProvider(oldOptions, invalidOptions, nil, nil, time.Unix(4, 0), nil); err == nil {
		t.Fatal("invalid generation accepted")
	}
	if provider.history.LoadURLTestHistory("test/node", urltest.DefaultURL) != prior {
		t.Fatal("failed refresh discarded live node history")
	}
	callbackCalled := false
	provider.RegisterCallback(func(string) error {
		callbackCalled = true
		for _, tag := range []string{"test/node", "test/removed"} {
			if provider.history.LoadLatestURLTestHistory(tag) != nil {
				t.Fatalf("callback observed retired history for %s", tag)
			}
		}
		if len(provider.ProviderSnapshot().Health) != 0 {
			t.Fatal("replacement inherited previous generation health")
		}
		return nil
	})
	newOptions := []option.Outbound{{Type: "history-test", Tag: "node", Options: &nodeOptions{Endpoint: "replacement"}}}
	if err := provider.UpdateProvider(oldOptions, newOptions, nil, nil, time.Unix(5, 0), nil); err != nil {
		t.Fatal(err)
	}
	if node, _ := provider.Outbound("test/node"); node == oldNode {
		t.Fatal("changed endpoint retained its previous generation")
	}
	if !callbackCalled {
		t.Fatal("replacement did not notify groups")
	}
	for _, target := range []string{urltest.DefaultURL, "https://other.example/check"} {
		if provider.history.LoadURLTestHistory("test/node", target) != nil || provider.history.LoadURLTestHistory("test/removed", target) != nil {
			t.Fatalf("retired generation retained history for %s", target)
		}
		if provider.history.LoadURLTestHistory("unrelated/node", target) != prior {
			t.Fatal("refresh discarded unrelated provider history")
		}
	}
	// Simulate an old group probe completing after the provider replacement.
	provider.history.StoreURLTestHistory("test/node", urltest.DefaultURL, oldGeneration, prior)
	if provider.history.LoadLatestURLTestHistory("test/node") != nil {
		t.Fatal("retired probe restored the previous endpoint's history")
	}
	current := &adapter.URLTestHistory{Time: time.Unix(6, 0), Delay: 30}
	provider.history.StoreURLTestHistory("test/node", urltest.DefaultURL, provider.history.OutboundGeneration("test/node"), current)
	provider.history.DeleteURLTestHistory("test/node", urltest.DefaultURL, oldGeneration)
	if provider.history.LoadURLTestHistory("test/node", urltest.DefaultURL) != current {
		t.Fatal("retired probe erased the replacement endpoint's success")
	}
}

func TestProviderHealthCheckFailureIsTargetScoped(t *testing.T) {
	factory := log.NewNOPFactory()
	provider := NewAdapter(context.Background(), nil, nil, nil, factory, factory.Logger(), "test", "local", option.ProviderHealthCheckOptions{URL: "https://provider.example/check"})
	provider.history = urltest.NewHistoryStorage()
	defer provider.cancel()
	node := &historyTestOutbound{
		Adapter: outbound.NewAdapter("history-test", "test/node", []string{"tcp"}, nil),
		dial: func(context.Context, string, M.Socksaddr) (net.Conn, error) {
			return nil, errors.New("unavailable")
		},
	}
	provider.outbounds = []adapter.Outbound{node}
	prior := &adapter.URLTestHistory{Time: time.Unix(1, 0), Delay: 20}
	provider.history.StoreURLTestHistory(node.Tag(), provider.link, provider.history.OutboundGeneration(node.Tag()), prior)
	provider.history.StoreURLTestHistory(node.Tag(), urltest.DefaultURL, provider.history.OutboundGeneration(node.Tag()), prior)
	if _, err := provider.HealthCheck(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.history.LoadURLTestHistory(node.Tag(), provider.link) != nil {
		t.Fatal("failed provider target retained success")
	}
	if provider.history.LoadURLTestHistory(node.Tag(), urltest.DefaultURL) != prior {
		t.Fatal("provider failure erased another target's success")
	}
	if provider.ProviderSnapshot().Health[node.Tag()].Available {
		t.Fatal("failed provider probe remained available")
	}
}

func TestProviderHealthCheckCancellationPreservesSuccess(t *testing.T) {
	for _, source := range []string{"caller", "provider"} {
		t.Run(source, func(t *testing.T) {
			factory := log.NewNOPFactory()
			provider := NewAdapter(context.Background(), nil, nil, nil, factory, factory.Logger(), "test", "local", option.ProviderHealthCheckOptions{})
			provider.history = urltest.NewHistoryStorage()
			defer provider.cancel()
			started := make(chan struct{})
			node := &historyTestOutbound{
				Adapter: outbound.NewAdapter("history-test", "test/node", []string{"tcp"}, nil),
				dial: func(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
					close(started)
					<-ctx.Done()
					return nil, ctx.Err()
				},
			}
			provider.outbounds = []adapter.Outbound{node}
			prior := &adapter.URLTestHistory{Time: time.Unix(1, 0), Delay: 20}
			provider.history.StoreURLTestHistory(node.Tag(), urltest.DefaultURL, provider.history.OutboundGeneration(node.Tag()), prior)
			previousHealth := adapter.ProviderHealth{Available: true, Delay: prior.Delay, CheckedAt: prior.Time}
			provider.health = map[string]adapter.ProviderHealth{node.Tag(): previousHealth}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := provider.HealthCheck(ctx)
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("health check did not start")
			}
			if source == "provider" {
				provider.cancel()
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("expected cancellation, got %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("health check did not stop after cancellation")
			}
			if provider.history.LoadURLTestHistory(node.Tag(), urltest.DefaultURL) != prior {
				t.Fatal("cancellation erased scoped success")
			}
			if current := provider.ProviderSnapshot().Health[node.Tag()]; current != previousHealth {
				t.Fatalf("cancellation changed health: %+v", current)
			}
		})
	}
}
