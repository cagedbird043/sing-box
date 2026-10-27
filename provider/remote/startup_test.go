package remote

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/adapter/provider"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

const startupSubscription = `{"outbounds":[{"type":"startup-test","tag":"node"}]}`

// An in-memory subscription server keeps request/response blocking deterministic,
// including under -race, without relying on production network timeouts.
type startupTransport struct {
	requests chan struct{}
	release  chan struct{}
	status   int
}

func (s *startupTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	s.requests <- struct{}{}
	select {
	case <-s.release:
		return &http.Response{
			StatusCode: s.status,
			Status:     http.StatusText(s.status),
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(startupSubscription)),
			Request:    request,
		}, nil
	case <-request.Context().Done():
		return nil, request.Context().Err()
	}
}

func (*startupTransport) CloseIdleConnections() {}
func (*startupTransport) Reset()                {}

type startupHTTPManager struct {
	adapter.HTTPClientManager
	transport adapter.HTTPTransport
}

func (m *startupHTTPManager) DefaultTransport() adapter.HTTPTransport { return m.transport }

type startupOutboundManager struct{ adapter.OutboundManager }

func (*startupOutboundManager) Outbound(string) (adapter.Outbound, bool) { return nil, false }
func (*startupOutboundManager) PublishProvider(string, []adapter.Outbound) error {
	return nil
}

type startupEndpointManager struct{ adapter.EndpointManager }

func (*startupEndpointManager) PublishProvider(string, []adapter.Endpoint) error { return nil }

type startupNodeOptions struct{}

type startupNode struct {
	outbound.Adapter
	N.Dialer
}

func startupContext(transport adapter.HTTPTransport) context.Context {
	registry := outbound.NewRegistry()
	outbound.Register(registry, "startup-test", func(_ context.Context, _ adapter.Router, _ log.ContextLogger, tag string, _ startupNodeOptions) (adapter.Outbound, error) {
		return &startupNode{Adapter: outbound.NewAdapter("startup-test", tag, []string{"tcp"}, nil)}, nil
	})
	ctx := service.ContextWithDefaultRegistry(context.Background())
	ctx = service.ContextWith[adapter.HTTPClientManager](ctx, &startupHTTPManager{transport: transport})
	ctx = service.ContextWith[adapter.OutboundManager](ctx, &startupOutboundManager{})
	ctx = service.ContextWith[adapter.EndpointManager](ctx, &startupEndpointManager{})
	ctx = service.ContextWith[adapter.OutboundRegistry](ctx, registry)
	return service.ContextWith[option.OutboundOptionsRegistry](ctx, registry)
}

func startupOptions(t *testing.T, cacheAge time.Duration) option.ProviderRemoteOptions {
	t.Helper()
	options := option.ProviderRemoteOptions{URL: "http://subscription.test/profile"}
	if cacheAge >= 0 {
		options.Path = filepath.Join(t.TempDir(), "subscription.json")
		if err := os.WriteFile(options.Path, []byte(startupSubscription), 0o600); err != nil {
			t.Fatal(err)
		}
		mtime := time.Now().Add(-cacheAge)
		if err := os.Chtimes(options.Path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	return options
}

func newStartupRemote(t *testing.T, cacheAge time.Duration) (*ProviderRemote, *startupTransport, context.Context) {
	t.Helper()
	transport := &startupTransport{requests: make(chan struct{}, 10), release: make(chan struct{}), status: http.StatusOK}
	ctx := startupContext(transport)
	factory := log.NewNOPFactory()
	p, err := NewProviderRemote(ctx, nil, factory, "sub", startupOptions(t, cacheAge))
	if err != nil {
		t.Fatal(err)
	}
	s := p.(*ProviderRemote)
	t.Cleanup(func() {
		close(transport.release)
		_ = s.Close()
	})
	return s, transport, ctx
}

func assertNoStartupRequest(t *testing.T, transport *startupTransport) {
	t.Helper()
	select {
	case <-transport.requests:
		t.Fatal("unexpected background subscription request")
	default:
	}
}

func assertStartupReturned(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	default:
		t.Fatal("startup blocked on a background subscription request")
		return nil
	}
}

// Plan 10: an expired cache is sufficient for lifecycle advancement even while
// the subscription server blocks its first background response.
func TestRemoteProviderExpiredCacheWaitsForStarted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, transport, ctx := newStartupRemote(t, 48*time.Hour)
		startContext := adapter.NewHTTPStartContext()
		defer startContext.Close()
		for _, stage := range []adapter.StartStage{adapter.StartStateInitialize, adapter.StartStateStart} {
			if err := s.Advance(stage); err != nil {
				t.Fatal(err)
			}
		}
		done := make(chan error, 1)
		go func() { done <- s.StartContext(ctx, startContext) }()
		synctest.Wait()
		if err := assertStartupReturned(t, done); err != nil {
			t.Fatal(err)
		}
		if len(s.Outbounds()) != 1 {
			t.Fatal("startup did not restore cached nodes")
		}
		assertNoStartupRequest(t, transport)
		go func() { done <- s.Advance(adapter.StartStatePostStart) }()
		synctest.Wait()
		if err := assertStartupReturned(t, done); err != nil {
			t.Fatal(err)
		}
		assertNoStartupRequest(t, transport)
		go func() { done <- s.Advance(adapter.StartStateStarted) }()
		synctest.Wait()
		if err := assertStartupReturned(t, done); err != nil {
			t.Fatal(err)
		}
		select {
		case <-transport.requests:
		default:
			t.Fatal("expired cache did not update after Started")
		}
	})
}

// Plan 13: without a cache, StartContext waits for the initial fetch and returns
// its failure instead of treating it as a background update.
func TestRemoteProviderNoCacheFetchIsSynchronous(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, transport, ctx := newStartupRemote(t, -1)
				transport.status = status
				startContext := adapter.NewHTTPStartContext()
				defer startContext.Close()
				done := make(chan error, 1)
				go func() { done <- s.StartContext(ctx, startContext) }()
				synctest.Wait()
				select {
				case <-transport.requests:
				default:
					t.Fatal("Start did not fetch the missing subscription")
				}
				select {
				case err := <-done:
					t.Fatalf("Start returned before its initial response: %v", err)
				default:
				}
				// Allow the response without closing the channel twice in cleanup.
				transport.release <- struct{}{}
				synctest.Wait()
				err := assertStartupReturned(t, done)
				if (err != nil) != (status != http.StatusOK) {
					t.Fatalf("initial fetch status %d: %v", status, err)
				}
				if err == nil && len(s.Outbounds()) != 1 {
					t.Fatal("initial fetch did not supply nodes")
				}
			})
		})
	}
}

// Plan 14: Close releases a readiness wait and an unexpired-cache timer, without
// issuing a request. The synctest bubble also requires all goroutines to exit.
func TestRemoteProviderCloseReleasesStartupWaits(t *testing.T) {
	for _, age := range []time.Duration{48 * time.Hour, 0} {
		t.Run(age.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, transport, ctx := newStartupRemote(t, age)
				startContext := adapter.NewHTTPStartContext()
				defer startContext.Close()
				if err := s.StartContext(ctx, startContext); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				if age == 0 {
					s.MarkStarted()
					synctest.Wait()
				}
				assertNoStartupRequest(t, transport)
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				assertNoStartupRequest(t, transport)
			})
		})
	}
}

// Plan 15: creating a provider after the manager's Started stage must release
// its background work, including an expired-cache update.
func TestRemoteProviderManagerCreateAfterStarted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &startupTransport{requests: make(chan struct{}, 10), release: make(chan struct{}), status: http.StatusOK}
		ctx := startupContext(transport)
		factory := log.NewNOPFactory()
		registry := provider.NewRegistry()
		RegisterProvider(registry)
		manager := provider.NewManager(factory.Logger(), registry)
		scope := adapter.NewScope(ctx, factory.Logger())
		t.Cleanup(func() {
			close(transport.release)
			_ = scope.Close()
		})
		for _, stage := range adapter.ListStartStages {
			if err := manager.Start(stage, scope); err != nil {
				t.Fatal(err)
			}
		}
		options := startupOptions(t, 48*time.Hour)
		done := make(chan error, 1)
		go func() { done <- manager.Create(ctx, nil, factory, "sub", "remote", &options) }()
		synctest.Wait()
		if err := assertStartupReturned(t, done); err != nil {
			t.Fatal(err)
		}
		select {
		case <-transport.requests:
		default:
			t.Fatal("provider created after Started never updated")
		}
	})
}
