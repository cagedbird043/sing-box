package provider_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"
)

func TestRemoteProviderRefreshAndOfflineCache(t *testing.T) {
	var content atomic.Value
	content.Store(`proxies:
  - {name: local-ss, type: ss, server: 127.0.0.1, port: 10001, cipher: aes-128-gcm, password: first}
  - {name: obsolete, type: ss, server: 127.0.0.1, port: 10002, cipher: aes-128-gcm, password: obsolete}
`)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(content.Load().(string)))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "subscription.json")
	config, err := json.Marshal(map[string]any{
		"log":       map[string]any{"disabled": true},
		"providers": []any{map[string]any{"type": "remote", "tag": "sub", "url": server.URL, "path": path, "http_client": map[string]any{"version": 1}}},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}, map[string]any{"type": "selector", "tag": "select", "providers": []string{"sub"}}, map[string]any{"type": "urltest", "tag": "auto", "use_all_providers": true, "include": "sub/(local-ss|added|obsolete)$", "url": server.URL, "interval": "1h", "idle_timeout": "1h", "fallback": map[string]any{"enabled": true, "max_delay": "50ms"}}},
		"route":     map[string]any{"final": "select"},
		"services":  []any{map[string]any{"type": "api", "tag": "api", "listen": "127.0.0.1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	start := func() (*box.Box, context.Context) {
		t.Helper()
		ctx := service.ContextWithDefaultRegistry(include.Context(context.Background()))
		options, err := json.UnmarshalExtendedContext[option.Options](ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		instance, err := box.New(box.Options{Options: options, Context: ctx})
		if err != nil {
			t.Fatal(err)
		}
		if err = instance.Start(); err != nil {
			instance.Close()
			t.Fatal(err)
		}
		return instance, ctx
	}
	instance, ctx := start()
	defer func() {
		if instance != nil {
			instance.Close()
		}
	}()
	provider, found := service.FromContext[adapter.ProviderManager](ctx).Get("sub")
	if !found {
		t.Fatal("provider missing")
	}
	outbound := instance.Outbound()
	if service.FromContext[adapter.OutboundManager](ctx) != outbound {
		t.Fatal("provider generation replaced the global outbound manager")
	}
	old, found := outbound.Outbound("sub/local-ss")
	if !found {
		t.Fatal("initial provider node missing")
	}
	selector, _ := outbound.Outbound("select")
	group := selector.(adapter.OutboundGroup)
	if !slices.Equal(group.All(), []string{"sub/local-ss", "sub/obsolete"}) {
		t.Fatalf("initial members: %v", group.All())
	}
	content.Store(`proxies:
  - {name: local-ss, type: ss, server: 127.0.0.1, port: 10003, cipher: aes-128-gcm, password: replacement}
  - {name: added, type: ss, server: 127.0.0.1, port: 10004, cipher: aes-128-gcm, password: added}
`)
	if err := provider.(adapter.ProviderUpdater).Update(); err != nil {
		t.Fatal(err)
	}
	replaced, found := outbound.Outbound("sub/local-ss")
	if service.FromContext[adapter.OutboundManager](ctx) != outbound {
		t.Fatal("refresh replaced the global outbound manager")
	}
	if !found || replaced == old {
		t.Fatal("changed node was not replaced")
	}
	if _, found = outbound.Outbound("sub/obsolete"); found {
		t.Fatal("deleted node remains in manager")
	}
	if _, found = outbound.Outbound("sub/added"); !found {
		t.Fatal("added node missing")
	}
	if !slices.Equal(group.All(), []string{"sub/local-ss", "sub/added"}) {
		t.Fatalf("refreshed members: %v", group.All())
	}
	if group.Selected("tcp") != replaced {
		t.Fatal("selector retains retired generation")
	}
	autoOutbound, _ := outbound.Outbound("auto")
	auto := autoOutbound.(adapter.OutboundGroup)
	if !slices.Equal(auto.All(), []string{"sub/local-ss", "sub/added"}) {
		t.Fatalf("URL-test refreshed members: %v", auto.All())
	}
	if auto.Selected("tcp") != replaced {
		t.Fatal("URL-test retains retired generation")
	}
	content.Store(`{"outbounds":[{"type":"shadowsocks","tag":"bad","server":"127.0.0.1","server_port":80,"method":"aes-128-gcm","password":"bad","detour":"cycle"},{"type":"shadowsocks","tag":"cycle","server":"127.0.0.1","server_port":81,"method":"aes-128-gcm","password":"bad","detour":"bad"}]}`)
	if err := provider.(adapter.ProviderUpdater).Update(); err == nil {
		t.Fatal("circular dependency accepted")
	}
	current, found := outbound.Outbound("sub/local-ss")
	if !found || current != replaced {
		t.Fatal("failed refresh changed live generation")
	}
	if _, found := outbound.Outbound("sub/bad"); found {
		t.Fatal("failed generation was published")
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
	instance = nil
	server.Close()
	before := requests.Load()
	instance, _ = start()
	if requests.Load() != before {
		t.Fatal("offline start unexpectedly downloaded")
	}
	if _, found := instance.Outbound().Outbound("sub/added"); !found {
		t.Fatal("offline cache failed to restore changed subscription")
	}
	if _, found := instance.Outbound().Outbound("sub/obsolete"); found {
		t.Fatal("offline cache restored removed node")
	}
}
