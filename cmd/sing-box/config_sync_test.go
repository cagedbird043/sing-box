package main

import (
	"io"
	stdlog "log"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestConfigSyncTLSFailure(t *testing.T) {
	if runtime.GOOS == "windows" && requireServiceAdmin() != nil {
		t.Skip("private configuration URL files require an elevated Administrator session")
	}
	server := httptest.NewUnstartedServer(http.NotFoundHandler())
	server.Config.ErrorLog = stdlog.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	syncer := managedService{configDir: t.TempDir()}
	const path, query = "/private-tls-config", "key=private-tls-credential"
	if err := writeManagedFile(syncer.urlPath(), []byte(server.URL+path+"?"+query), true); err != nil {
		t.Fatal(err)
	}
	candidate, _, err := syncer.pull(t.Context())
	if candidate != "" || err == nil || !strings.Contains(err.Error(), "x509:") {
		t.Fatalf("certificate failure: candidate = %q, err = %v", candidate, err)
	}
	assertSyncDiagnosticRedacted(t, err.Error(), server.URL, path, query)
}

func assertSyncDiagnosticRedacted(t *testing.T, message, server, path, query string) {
	t.Helper()
	for _, secret := range []string{server, "127.0.0.1", path, query} {
		if strings.Contains(message, secret) {
			t.Fatalf("sync diagnostic exposed %q: %q", secret, message)
		}
	}
}
