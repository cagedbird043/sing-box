package hosts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/sing/service"

	mDNS "github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

func TestHosts(t *testing.T) {
	t.Parallel()
	require.Equal(t, []netip.Addr{netip.AddrFrom4([4]byte{127, 0, 0, 1}), netip.IPv6Loopback()}, NewFile(context.Background(), "testdata/hosts").Lookup("localhost"))
	if runtime.GOOS != "windows" {
		defaultPathResolved, err := defaultPath()
		if err != nil {
			t.Fatal(E.Cause(err, "resolve default hosts path"))
		}
		content, readErr := os.ReadFile(defaultPathResolved)
		require.NoError(t, readErr)
		hFile := NewFile(context.Background(), defaultPathResolved)
		if len(hFile.Lookup("localhost")) == 0 {
			t.Fatal("failed to resolve localhost: ", defaultPathResolved, ": \n", string(content))
		}
	}
}

func TestHostsRemoteProvider(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "sing-box test", request.Header.Get("User-Agent"))
		_, err := writer.Write([]byte("142.251.111.188 mtalk.google.com\n"))
		require.NoError(t, err)
	}))
	defer server.Close()

	cachePath := t.TempDir() + "/fcm.hosts"
	transport, err := NewTransport(
		testHTTPClientContext(),
		testLogger(),
		"hosts",
		option.HostsDNSServerOptions{
			Providers: []option.HostsProviderOptions{{
				Type:           "remote",
				Tag:            "fcm",
				URL:            server.URL,
				Path:           cachePath,
				UserAgent:      "sing-box test",
				UpdateInterval: badoption.Duration(time.Hour),
			}},
		},
	)
	require.NoError(t, err)
	require.NoError(t, transport.Start(adapter.StartStateStart))
	defer transport.Close()

	require.Equal(t, []netip.Addr{netip.MustParseAddr("142.251.111.188")}, NewFile(cachePath).Lookup("mtalk.google.com"))
	require.True(t, transport.(adapter.DNSTransportWithPreferredDomain).PreferredDomain("mtalk.google.com."))

	response, err := transport.Exchange(context.Background(), hostsQuery("mtalk.google.com.", mDNS.TypeA))
	require.NoError(t, err)
	require.Len(t, response.Answer, 1)
	require.Contains(t, response.Answer[0].String(), "142.251.111.188")
}

func TestHostsRemoteProviderKeepsCacheOnUpdateFailure(t *testing.T) {
	t.Parallel()
	cachePath := t.TempDir() + "/fcm.hosts"
	require.NoError(t, os.WriteFile(cachePath, []byte("142.251.111.188 mtalk.google.com\n"), 0o644))
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	server.Close()

	transport, err := NewTransport(
		testHTTPClientContext(),
		testLogger(),
		"hosts",
		option.HostsDNSServerOptions{
			Providers: []option.HostsProviderOptions{{
				Type:           "remote",
				Tag:            "fcm",
				URL:            server.URL,
				Path:           cachePath,
				UpdateInterval: badoption.Duration(time.Hour),
			}},
		},
	)
	require.NoError(t, err)
	require.NoError(t, transport.Start(adapter.StartStateStart))
	defer transport.Close()
	require.Equal(t, []netip.Addr{netip.MustParseAddr("142.251.111.188")}, NewFile(cachePath).Lookup("mtalk.google.com"))
}

func hostsQuery(domain string, qType uint16) *mDNS.Msg {
	return &mDNS.Msg{
		MsgHdr: mDNS.MsgHdr{
			Id: 1,
		},
		Question: []mDNS.Question{{
			Name:   domain,
			Qtype:  qType,
			Qclass: mDNS.ClassINET,
		}},
	}
}

func testHTTPClientContext() context.Context {
	return service.ContextWith[adapter.HTTPClientManager](context.Background(), testHTTPClientManager{})
}

func testLogger() log.ContextLogger {
	return log.NewNOPFactory().NewLogger("hosts-test")
}

type testHTTPClientManager struct{}

func (m testHTTPClientManager) ResolveTransport(ctx context.Context, logger logger.ContextLogger, options option.HTTPClientOptions) (adapter.HTTPTransport, error) {
	return testHTTPTransport{RoundTripper: http.DefaultTransport}, nil
}

func (m testHTTPClientManager) DefaultTransport() adapter.HTTPTransport {
	return testHTTPTransport{RoundTripper: http.DefaultTransport}
}

func (m testHTTPClientManager) ResetNetwork() {}

type testHTTPTransport struct {
	http.RoundTripper
}

func (t testHTTPTransport) CloseIdleConnections() {}

func (t testHTTPTransport) Reset() {}
