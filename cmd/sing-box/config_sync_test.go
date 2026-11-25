package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/log"
)

func TestConfigSync(t *testing.T) {
	if runtime.GOOS == "windows" && requireServiceAdmin() != nil {
		t.Skip("private configuration URL files require an elevated Administrator session")
	}
	previousContext := globalCtx
	globalCtx = include.Context(context.Background())
	t.Cleanup(func() { globalCtx = previousContext })
	syncer := configSync{target: filepath.Join(t.TempDir(), serviceConfigFilename), interval: 10 * time.Millisecond}
	if _, _, err := syncer.pull(t.Context()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing URL file: %v", err)
	}

	const (
		valid    = `{"outbounds":[{"type":"direct","tag":"direct"}]}`
		urlPath  = "/private-config-location"
		urlQuery = "token=private-test-credential"
	)
	var response atomic.Int32
	var privateURL string
	pullsSeen := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer requestConfigReload(pullsSeen)
		if r.URL.Path != urlPath || r.URL.RawQuery != urlQuery {
			http.NotFound(w, r)
			return
		}
		switch response.Load() {
		case 0:
			_, _ = io.WriteString(w, valid)
		case 1:
			fmt.Fprintf(w, `{"outbounds":[{"type":%q}]}`, privateURL)
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	t.Cleanup(server.Close)
	privateURL = server.URL + urlPath + "?" + urlQuery
	if err := writePrivateURLFile(syncer.urlFile(), privateURL); err != nil {
		t.Fatal(err)
	}
	logged := make(configSyncTestLog, 1)
	factory := log.NewDefaultFactory(t.Context(), log.Formatter{DisableColors: true}, logged, "", nil, false)
	if err := factory.Start(); err != nil {
		t.Fatal(err)
	}
	previousLogger := log.StdLogger()
	log.SetStdLogger(factory.Logger())
	t.Cleanup(func() {
		log.SetStdLogger(previousLogger)
		factory.Close()
	})

	ctx, stop := context.WithCancel(t.Context())
	reload := make(chan struct{}, 4)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		syncer.watch(ctx, nil, reload)
	}()
	t.Cleanup(func() { stop(); <-finished })
	select {
	case <-reload:
	case <-time.After(5 * time.Second):
		t.Fatal("valid changed configuration did not signal reload")
	}
	for range 3 {
		select {
		case <-pullsSeen:
		case <-time.After(5 * time.Second):
			t.Fatal("watcher did not check for identical configuration")
		}
	}
	if len(reload) != 0 {
		t.Fatal("identical configuration signaled an additional reload")
	}
	response.Store(1)
	select {
	case message := <-logged:
		if !strings.Contains(message, "unknown outbound type") {
			t.Fatalf("watcher did not log the validation reason: %q", message)
		}
		assertSyncDiagnosticRedacted(t, message, server.URL, urlPath, urlQuery)
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not log the validation error")
	}
	stop()
	<-finished
	response.Store(0)
	assertConfigContent(t, syncer.target, valid)
	original, err := os.Stat(syncer.target)
	if err != nil {
		t.Fatal(err)
	}
	if changed, _, err := syncer.pull(t.Context()); changed || err != nil {
		t.Fatalf("identical configuration: changed = %t, err = %v", changed, err)
	}
	if newInfo, err := os.Stat(syncer.target); err != nil || !os.SameFile(original, newInfo) {
		t.Fatalf("identical configuration was replaced: %v", err)
	}

	for _, tc := range []struct {
		state  int32
		reason string
	}{
		{1, "unknown outbound type"},
		{2, "HTTP 502 Bad Gateway"},
	} {
		response.Store(tc.state)
		changed, _, err := syncer.pull(t.Context())
		if changed || err == nil || !strings.Contains(err.Error(), tc.reason) {
			t.Fatalf("changed = %t, expected %q, got %v", changed, tc.reason, err)
		}
		assertSyncErrorRedacted(t, err, server.URL, urlPath, urlQuery)
		assertConfigContent(t, syncer.target, valid)
	}
	server.Close()
	changed, _, err := syncer.pull(t.Context())
	if changed || err == nil || !strings.Contains(err.Error(), "dial tcp") {
		t.Fatalf("failed connection: changed = %t, err = %v", changed, err)
	}
	assertSyncErrorRedacted(t, err, server.URL, urlPath, urlQuery)
	assertConfigContent(t, syncer.target, valid)
	if files, err := filepath.Glob(filepath.Join(filepath.Dir(syncer.target), ".config-pull-*")); err != nil || len(files) != 0 {
		t.Fatalf("temporary downloads left behind: %v (%v)", files, err)
	}
}

func TestConfigSyncTLSFailure(t *testing.T) {
	if runtime.GOOS == "windows" && requireServiceAdmin() != nil {
		t.Skip("private configuration URL files require an elevated Administrator session")
	}
	server := httptest.NewUnstartedServer(http.NotFoundHandler())
	server.Config.ErrorLog = stdlog.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	syncer := configSync{target: filepath.Join(t.TempDir(), serviceConfigFilename)}
	const path, query = "/private-tls-config", "key=private-tls-credential"
	if err := writePrivateURLFile(syncer.urlFile(), server.URL+path+"?"+query); err != nil {
		t.Fatal(err)
	}
	changed, _, err := syncer.pull(t.Context())
	if changed || err == nil || !strings.Contains(err.Error(), "x509:") {
		t.Fatalf("certificate failure: changed = %t, err = %v", changed, err)
	}
	assertSyncDiagnosticRedacted(t, err.Error(), server.URL, path, query)
}

type configSyncTestLog chan string

func (l configSyncTestLog) Write(message []byte) (int, error) {
	select {
	case l <- string(message):
	default:
	}
	return len(message), nil
}

func assertConfigContent(t *testing.T, path string, expected string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil || string(content) != expected {
		t.Fatalf("config = %q, err = %v; expected %q", content, err, expected)
	}
}

func assertSyncErrorRedacted(t *testing.T, err error, server, path, query string) {
	t.Helper()
	assertSyncDiagnosticRedacted(t, err.Error(), server, path, query)
}

func assertSyncDiagnosticRedacted(t *testing.T, message, server, path, query string) {
	t.Helper()
	for _, secret := range []string{server, "127.0.0.1", path, query} {
		if strings.Contains(message, secret) {
			t.Fatalf("sync diagnostic exposed %q: %q", secret, message)
		}
	}
}
