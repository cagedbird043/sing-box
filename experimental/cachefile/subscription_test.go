package cachefile

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/hash"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
)

func TestSubscriptionPendingFlushAndFormatIsolation(t *testing.T) {
	ctx := context.Background()
	options := option.CacheFileOptions{Path: filepath.Join(t.TempDir(), "cache.db")}
	open := func() (*CacheFile, *adapter.Scope) {
		t.Helper()
		cache := New(ctx, log.StdLogger(), options)
		scope := adapter.NewScope(ctx, log.StdLogger())
		if err := cache.Start(adapter.StartStateInitialize, scope); err != nil {
			t.Fatal(err)
		}
		return cache, scope
	}
	cache, scope := open()
	subscription := &adapter.SavedSubscription{Content: []byte("subscription"), Hash: hash.MakeHash([]byte("path-cache")), LastUpdated: time.Unix(123, 0), LastEtag: "etag", URLHash: []byte("url")}
	if err := cache.SaveSubscription("sub", subscription); err != nil {
		t.Fatal(err)
	}
	loaded := cache.LoadSubscription("sub")
	if loaded == nil || !bytes.Equal(loaded.Content, subscription.Content) || !loaded.Hash.Equal(subscription.Hash) {
		t.Fatal("pending subscription cannot be read")
	}
	if err := cache.SaveRuleSet("sub", &adapter.SavedBinary{Content: []byte("rule-set")}); err != nil {
		t.Fatal(err)
	}
	cache.Flush()
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	cache, scope = open()
	defer scope.Close()
	loaded = cache.LoadSubscription("sub")
	if loaded == nil || !bytes.Equal(loaded.Content, subscription.Content) || loaded.LastEtag != "etag" || !loaded.LastUpdated.Equal(subscription.LastUpdated) {
		t.Fatal("subscription did not survive flush/reopen")
	}
	if rules := cache.LoadRuleSet("sub"); rules == nil || string(rules.Content) != "rule-set" {
		t.Fatal("subscription bucket overwrote rule-set cache")
	}
	data, err := subscription.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	data[0] = 2
	if err := loaded.UnmarshalBinary(data); err == nil {
		t.Fatal("incompatible v2 cache layout accepted")
	}
}
