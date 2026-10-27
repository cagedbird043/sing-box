package provider

import (
	"errors"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common/x/list"
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
