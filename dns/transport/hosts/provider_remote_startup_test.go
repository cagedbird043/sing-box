package hosts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"

	mDNS "github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

const (
	cachedHostsContent  = "142.251.111.188 mtalk.google.com\n"
	updatedHostsContent = "142.251.111.189 mtalk.google.com\n"
)

// Plan tests 16 and 17: an expired cache must not make Start perform I/O,
// but Started must schedule its replacement without waiting another interval.
func TestHostsRemoteProviderCachedStartup(t *testing.T) {
	t.Parallel()
	requested := make(chan struct{}, 1)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requested <- struct{}{}
		select {
		case <-release:
			_, _ = writer.Write([]byte(updatedHostsContent))
		case <-request.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(unblock)
	cachePath := writeHostsStartupCache(t, -2*time.Hour)
	logger := newHostsStartupLogger()
	transport, scope := newHostsStartupTransport(t, testHTTPClientContext(), logger, cachePath, server.URL, true)
	started := make(chan error, 1)
	go func() { started <- transport.Start(adapter.StartStateStart, scope) }()
	select {
	case err := <-started:
		require.NoError(t, err)
	case <-requested:
		t.Fatal("cached Start attempted a download")
	case <-time.After(5 * time.Second):
		t.Fatal("cached Start did not return")
	}
	require.NoError(t, transport.Start(adapter.StartStatePostStart, scope))
	assertNoHostsStartupSignal(t, requested)
	response, err := transport.Exchange(context.Background(), hostsQuery("mtalk.google.com.", mDNS.TypeA))
	require.NoError(t, err)
	require.Len(t, response.Answer, 1)
	require.Contains(t, response.Answer[0].String(), "142.251.111.188")

	require.NoError(t, transport.Start(adapter.StartStateStarted, scope))
	waitHostsStartupSignal(t, requested)
	unblock()
	waitHostsStartupSignal(t, logger.updated)
	content, err := os.ReadFile(cachePath)
	require.NoError(t, err)
	require.Equal(t, updatedHostsContent, string(content))
	// The transport's existing File may retain its five-second parsed cache.
	require.Equal(t, []netip.Addr{netip.MustParseAddr("142.251.111.189")}, NewFile(context.Background(), cachePath).Lookup("mtalk.google.com"))
}

// Plan test 18: preserve the remaining lifetime instead of refreshing on Started.
func TestHostsRemoteProviderFreshCacheWaits(t *testing.T) {
	t.Parallel()
	requested := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requested <- struct{}{}
		_, _ = writer.Write([]byte(updatedHostsContent))
	}))
	t.Cleanup(server.Close)
	cachePath := writeHostsStartupCache(t, -30*time.Minute)
	transport, scope := newHostsStartupTransport(t, testHTTPClientContext(), testLogger(), cachePath, server.URL, false)
	require.NoError(t, transport.Start(adapter.StartStateStart, scope))
	provider := transport.providers[0]
	stat, err := os.Stat(cachePath)
	require.NoError(t, err)
	require.Equal(t, stat.ModTime().Add(time.Hour), provider.firstUpdateAt)
	observed := observeHostsStartupLoop(provider)
	require.NoError(t, transport.Start(adapter.StartStateStarted, scope))
	waitHostsStartupSignal(t, observed)
	assertNoHostsStartupSignal(t, requested)
}

// Plan test 20: an absent (or empty) cache keeps the initial synchronous fetch
// and required semantics. Optional failures still wait an interval after Started.
func TestHostsRemoteProviderInitialDownloadFailure(t *testing.T) {
	t.Parallel()
	for _, required := range []bool{false, true} {
		for _, emptyCache := range []bool{false, true} {
			name := "optional"
			if required {
				name = "required"
			}
			if emptyCache {
				name += "_empty_cache"
			}
			t.Run(name, func(t *testing.T) {
				requested := make(chan struct{}, 1)
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					requested <- struct{}{}
					http.Error(writer, "unavailable", http.StatusServiceUnavailable)
				}))
				t.Cleanup(server.Close)
				cachePath := t.TempDir() + "/fcm.hosts"
				if emptyCache {
					require.NoError(t, os.WriteFile(cachePath, nil, 0o644))
				}
				logger := newHostsStartupLogger()
				transport, scope := newHostsStartupTransport(t, testHTTPClientContext(), logger, cachePath, server.URL, required)
				err := transport.Start(adapter.StartStateStart, scope)
				waitHostsStartupSignal(t, requested)
				if required {
					require.ErrorContains(t, err, "initial hosts provider")
					assertNoHostsStartupSignal(t, logger.warned)
				} else {
					require.NoError(t, err)
					waitHostsStartupSignal(t, logger.warned)
					provider := transport.providers[0]
					require.True(t, provider.firstUpdateAt.IsZero())
					observed := observeHostsStartupLoop(provider)
					require.NoError(t, transport.Start(adapter.StartStateStarted, scope))
					waitHostsStartupSignal(t, observed)
					assertNoHostsStartupSignal(t, requested)
				}
				require.False(t, transport.providers[0].hasCacheFile())
			})
		}
	}
}

func TestHostsRemoteProviderInitialDownloadWaitsInterval(t *testing.T) {
	t.Parallel()
	requested := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requested <- struct{}{}
		_, _ = writer.Write([]byte(updatedHostsContent))
	}))
	t.Cleanup(server.Close)
	transport, scope := newHostsStartupTransport(t, testHTTPClientContext(), testLogger(), t.TempDir()+"/fcm.hosts", server.URL, true)
	require.NoError(t, transport.Start(adapter.StartStateStart, scope))
	waitHostsStartupSignal(t, requested)
	provider := transport.providers[0]
	require.True(t, provider.firstUpdateAt.IsZero())
	observed := observeHostsStartupLoop(provider)
	require.NoError(t, transport.Start(adapter.StartStateStarted, scope))
	waitHostsStartupSignal(t, observed)
	assertNoHostsStartupSignal(t, requested)
}

// Plan test 21: failed transport resolution must never start an update loop,
// regardless of whether a cache allows startup to succeed.
func TestHostsRemoteProviderTransportFailure(t *testing.T) {
	t.Parallel()
	for _, cached := range []bool{false, true} {
		for _, required := range []bool{false, true} {
			name := "no_cache"
			if cached {
				name = "cached"
			}
			if required {
				name += "_required"
			}
			t.Run(name, func(t *testing.T) {
				cachePath := t.TempDir() + "/fcm.hosts"
				if cached {
					cachePath = writeHostsStartupCache(t, -2*time.Hour)
				}
				logger := newHostsStartupLogger()
				transport, scope := newHostsStartupTransport(t, context.Background(), logger, cachePath, "http://unused.invalid/hosts", required)
				err := transport.Start(adapter.StartStateStart, scope)
				if required && !cached {
					require.ErrorContains(t, err, "create hosts provider http client")
					assertNoHostsStartupSignal(t, logger.warned)
				} else {
					require.NoError(t, err)
					waitHostsStartupSignal(t, logger.warned)
				}
				provider := transport.providers[0]
				require.Nil(t, provider.httpClient)
				observed := observeHostsStartupLoop(provider)
				require.NoError(t, transport.Start(adapter.StartStateStarted, scope))
				assertNoHostsStartupSignal(t, observed)
				assertNoHostsStartupSignal(t, logger.failed)
			})
		}
	}
}

// Plan test 22: cancellation before Started must not initiate a request, and
// cancellation while the first timer is pending must let its owner exit.
func TestHostsRemoteProviderCloseBeforeFirstUpdate(t *testing.T) {
	t.Parallel()
	for _, closeBeforeStarted := range []bool{true, false} {
		name := "waiting"
		if closeBeforeStarted {
			name = "before_started"
		}
		t.Run(name, func(t *testing.T) {
			requested := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requested <- struct{}{}
			}))
			t.Cleanup(server.Close)
			age := -30 * time.Minute
			if closeBeforeStarted {
				age = -2 * time.Hour
			}
			transport, scope := newHostsStartupTransport(t, testHTTPClientContext(), testLogger(), writeHostsStartupCache(t, age), server.URL, false)
			require.NoError(t, transport.Start(adapter.StartStateStart, scope))
			provider := transport.providers[0]
			observed := observeHostsStartupLoop(provider)
			if closeBeforeStarted {
				require.NoError(t, provider.Close())
				require.NoError(t, transport.Start(adapter.StartStateStarted, scope))
				assertNoHostsStartupSignal(t, observed)
			} else {
				// Run the same loop with an explicit completion signal so that exit,
				// not just cancellation of its context, is asserted.
				done := make(chan struct{})
				go func() {
					defer close(done)
					provider.loopUpdate(time.Hour)
				}()
				waitHostsStartupSignal(t, observed)
				require.NoError(t, provider.Close())
				waitHostsStartupSignal(t, done)
			}
			assertNoHostsStartupSignal(t, requested)
		})
	}
}

func writeHostsStartupCache(t *testing.T, age time.Duration) string {
	t.Helper()
	path := t.TempDir() + "/fcm.hosts"
	require.NoError(t, os.WriteFile(path, []byte(cachedHostsContent), 0o644))
	mtime := time.Now().Add(age)
	require.NoError(t, os.Chtimes(path, mtime, mtime))
	return path
}

func newHostsStartupTransport(t *testing.T, ctx context.Context, logger log.ContextLogger, path, url string, required bool) (*Transport, *adapter.Scope) {
	t.Helper()
	transport, err := NewTransport(ctx, logger, "hosts", option.HostsDNSServerOptions{
		Providers: []option.HostsProviderOptions{{
			URL:            url,
			Path:           path,
			Required:       required,
			UpdateInterval: badoption.Duration(time.Hour),
		}},
	})
	require.NoError(t, err)
	scope := adapter.NewScope(ctx, logger)
	t.Cleanup(func() { require.NoError(t, scope.Close()) })
	return transport.(*Transport), scope
}

func waitHostsStartupSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for hosts provider")
	}
}

func assertNoHostsStartupSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
		t.Fatal("unexpected hosts provider activity")
	default:
	}
}

type hostsStartupLogger struct {
	log.ContextLogger
	warned  chan struct{}
	failed  chan struct{}
	updated chan struct{}
}

func newHostsStartupLogger() *hostsStartupLogger {
	return &hostsStartupLogger{
		ContextLogger: testLogger(),
		warned:        make(chan struct{}, 1),
		failed:        make(chan struct{}, 1),
		updated:       make(chan struct{}, 1),
	}
}

func (l *hostsStartupLogger) Warn(args ...any) {
	l.warned <- struct{}{}
}

func (l *hostsStartupLogger) Error(args ...any) {
	l.failed <- struct{}{}
}

func (l *hostsStartupLogger) Info(args ...any) {
	if len(args) > 0 && args[0] == "updated hosts provider[" {
		l.updated <- struct{}{}
	}
}

// Done is evaluated when the loop enters its timer select, allowing negative
// assertions without sleeping or relying on a production update interval.
type hostsStartupObservedContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *hostsStartupObservedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func observeHostsStartupLoop(provider *RemoteProvider) <-chan struct{} {
	ctx := &hostsStartupObservedContext{Context: provider.ctx, entered: make(chan struct{})}
	provider.ctx = ctx
	return ctx.entered
}
