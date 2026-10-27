package provider_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/hash"
	"github.com/sagernet/sing-box/experimental/cachefile"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/log"
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

func TestRemoteProviderUnusableCache(t *testing.T) {
	const valid = `{"outbounds":[{"type":"shadowsocks","tag":"fresh","server":"127.0.0.1","server_port":10001,"method":"aes-128-gcm","password":"test"}]}`
	const missingResolver = `{"outbounds":[{"type":"shadowsocks","tag":"stale","server":"proxy.example","server_port":10001,"method":"aes-128-gcm","password":"test","domain_resolver":"dns-edgex"}]}`
	for _, test := range []struct {
		name          string
		content       string
		usePath       bool
		hashMismatch  bool
		invalidated   bool
		fetchFails    bool
		cacheWriteErr bool
	}{
		{name: "file_parse_error", content: "{invalid", usePath: true},
		{name: "saved_parse_error", content: "{invalid"},
		{name: "file_missing_resolver", content: missingResolver, usePath: true},
		{name: "saved_missing_resolver", content: missingResolver},
		{name: "file_hash_mismatch", content: valid, usePath: true, hashMismatch: true},
		{name: "file_invalidated", content: missingResolver, usePath: true, invalidated: true},
		{name: "saved_invalidated", invalidated: true},
		{name: "file_fetch_failure", content: missingResolver, usePath: true, fetchFails: true},
		{name: "saved_fetch_failure", content: missingResolver, fetchFails: true},
		{name: "cache_write_failure", usePath: true, invalidated: true, cacheWriteErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "subscription.json")
			cacheOptions := option.CacheFileOptions{Path: filepath.Join(directory, "cache.db")}
			seedContext := context.Background()
			cache := cachefile.New(seedContext, log.StdLogger(), cacheOptions)
			scope := adapter.NewScope(seedContext, log.StdLogger())
			if err := cache.Start(adapter.StartStateInitialize, scope); err != nil {
				t.Fatal(err)
			}
			saved := &adapter.SavedSubscription{}
			if !test.invalidated {
				saved.LastUpdated = time.Now()
				saved.LastEtag = `"stale"`
				if test.usePath {
					saved.Hash = hash.MakeHash([]byte(test.content))
					if test.hashMismatch {
						saved.Hash = hash.MakeHash([]byte("different content"))
					}
				} else {
					saved.Content = []byte(test.content)
				}
			}
			if err := cache.SaveSubscription("sub", saved); err != nil {
				t.Fatal(err)
			}
			if err := scope.Close(); err != nil {
				t.Fatal(err)
			}
			if test.usePath && test.content != "" {
				if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var requests atomic.Int32
			var conditional atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				conditional.Store(r.Header.Get("If-None-Match") != "")
				if test.fetchFails {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				if test.cacheWriteErr {
					// Make persistence fail only after construction and cache restoration.
					if err := os.MkdirAll(path, 0o700); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
				}
				_, _ = w.Write([]byte(valid))
			}))
			defer server.Close()
			providerOptions := map[string]any{"type": "remote", "tag": "sub", "url": server.URL, "http_client": map[string]any{"version": 1}}
			if test.usePath {
				providerOptions["path"] = path
			}
			config, err := json.Marshal(map[string]any{
				"log":          map[string]any{"disabled": true},
				"providers":    []any{providerOptions},
				"outbounds":    []any{map[string]any{"type": "direct", "tag": "direct"}},
				"experimental": map[string]any{"cache_file": map[string]any{"enabled": true, "path": cacheOptions.Path}},
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx := service.ContextWithDefaultRegistry(include.Context(context.Background()))
			options, err := json.UnmarshalExtendedContext[option.Options](ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			instance, err := box.New(box.Options{Options: options, Context: ctx})
			if err != nil {
				t.Fatal(err)
			}
			defer instance.Close()
			err = instance.Start()
			if test.fetchFails {
				if err == nil {
					t.Fatal("startup succeeded without a usable cache or initial download")
				}
				if saved := service.FromContext[adapter.CacheFile](ctx).LoadSubscription("sub"); saved != nil && (!saved.LastUpdated.IsZero() || len(saved.Content) != 0 || saved.LastEtag != "") {
					t.Fatalf("failed cache was not invalidated: %+v", saved)
				}
				if test.usePath {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("failed cache file remains: %v", err)
					}
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, found := instance.Outbound().Outbound("sub/fresh"); !found {
					t.Fatal("initial download did not publish fresh provider members")
				}
			}
			if requests.Load() != 1 {
				t.Fatalf("initial download requests: %d", requests.Load())
			}
			if conditional.Load() {
				t.Fatal("initial download reused the discarded cache ETag")
			}
		})
	}
}
