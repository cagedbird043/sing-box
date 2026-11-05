package daemon_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestProviderServiceRemoteSubscriptionURL(t *testing.T) {
	const subscription = `{"outbounds":[{"type":"shadowsocks","tag":"node","server":"127.0.0.1","server_port":10001,"method":"aes-128-gcm","password":"local-test"}]}`
	var downloads atomic.Int32
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/subscription" || r.URL.Query().Get("token") != "local+test" {
			t.Error("unexpected local subscription request")
			http.NotFound(w, r)
			return
		}
		downloads.Add(1)
		w.Header().Set("Subscription-Userinfo", "upload=1; download=2; total=4096; expire=2000000000")
		_, _ = w.Write([]byte(subscription))
	}))
	t.Cleanup(httpServer.Close)
	subscriptionURL := httpServer.URL + "/subscription?token=local%2Btest&format=sing-box#source"
	localPath := filepath.Join(t.TempDir(), "local.json")
	if err := os.WriteFile(localPath, []byte(subscription), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]any{
		"log":          map[string]any{"disabled": true},
		"experimental": map[string]any{"cache_file": map[string]any{"enabled": true, "path": filepath.Join(t.TempDir(), "cache.db")}},
		"providers": []any{
			map[string]any{"type": "remote", "tag": "remote", "url": subscriptionURL, "http_client": map[string]any{"version": 1}},
			map[string]any{"type": "local", "tag": "local", "path": localPath},
			map[string]any{"type": "inline", "tag": "inline", "outbounds": []any{map[string]any{"type": "direct", "tag": "node"}}},
		},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := service.ContextWithDefaultRegistry(include.Context(context.Background()))
	started := daemon.NewStartedService(daemon.ServiceOptions{Context: ctx})
	t.Cleanup(started.Close)
	if err := started.StartOrReloadService(ctx, string(config), nil); err != nil {
		t.Fatal(err)
	}
	if downloads.Load() != 1 {
		t.Fatalf("expected one real local subscription download, got %d", downloads.Load())
	}

	listener := bufconn.Listen(1024 * 1024)
	server := daemon.NewServer(started, "test-secret")
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	connection, err := grpc.NewClient(
		"passthrough:///provider-source-test",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	client := daemon.NewProviderServiceClient(connection)
	requestContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, authorization := range []string{"", "Bearer wrong-secret"} {
		deniedContext := requestContext
		if authorization != "" {
			deniedContext = metadata.AppendToOutgoingContext(deniedContext, "authorization", authorization)
		}
		if response, err := client.ListProviders(deniedContext, &emptypb.Empty{}); response != nil || status.Code(err) != codes.Unauthenticated {
			t.Fatalf("unauthenticated ListProviders returned data or unexpected status: %v", err)
		}
		if response, err := client.GetProvider(deniedContext, &daemon.GetProviderRequest{ProviderTag: "remote"}); response != nil || status.Code(err) != codes.Unauthenticated {
			t.Fatalf("unauthenticated GetProvider returned data or unexpected status: %v", err)
		}
		stream, err := client.SubscribeProviders(deniedContext, &emptypb.Empty{})
		if err == nil {
			response, receiveErr := stream.Recv()
			if response != nil {
				t.Fatal("unauthenticated SubscribeProviders returned provider data")
			}
			err = receiveErr
		}
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("unexpected unauthenticated SubscribeProviders status: %v", err)
		}
	}

	authorizedContext := metadata.AppendToOutgoingContext(requestContext, "authorization", "Bearer test-secret")
	assertProvider := func(provider *daemon.Provider) {
		t.Helper()
		if provider == nil || len(provider.Nodes) != 1 {
			t.Fatal("expected an initialized provider with one node")
		}
		switch provider.Tag {
		case "remote":
			if provider.Url != subscriptionURL {
				t.Fatal("remote subscription URL does not exactly match configured URL")
			}
			if provider.Subscription == nil || provider.Subscription.UploadBytes != 1 || provider.Subscription.DownloadBytes != 2 || provider.Subscription.TotalBytes != 4096 || provider.Subscription.ExpireAtSeconds != 2000000000 {
				t.Fatal("remote subscription metadata was not preserved")
			}
		case "local", "inline":
			if provider.Url != "" {
				t.Fatal("non-remote provider exposed a subscription source")
			}
		default:
			t.Fatalf("unexpected provider tag: %s", provider.Tag)
		}
	}
	assertList := func(providers *daemon.ProviderList) {
		t.Helper()
		if providers == nil || len(providers.Providers) != 3 {
			t.Fatal("expected all three initialized provider types")
		}
		for _, provider := range providers.Providers {
			assertProvider(provider)
		}
	}
	providers, err := client.ListProviders(authorizedContext, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	assertList(providers)
	for _, tag := range []string{"remote", "local", "inline"} {
		provider, err := client.GetProvider(authorizedContext, &daemon.GetProviderRequest{ProviderTag: tag})
		if err != nil {
			t.Fatal(err)
		}
		assertProvider(provider)
	}
	stream, err := client.SubscribeProviders(authorizedContext, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	assertList(initial)
}
