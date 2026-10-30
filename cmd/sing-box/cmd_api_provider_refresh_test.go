package main

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sagernet/sing-box/daemon"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeAPIProviderRefreshClient struct {
	refresh func(context.Context, string) (*daemon.ProviderActionResult, error)
	calls   []string
}

func (c *fakeAPIProviderRefreshClient) RefreshProvider(ctx context.Context, request *daemon.ProviderActionRequest, _ ...grpc.CallOption) (*daemon.ProviderActionResult, error) {
	c.calls = append(c.calls, request.GetProviderTag())
	return c.refresh(ctx, request.GetProviderTag())
}

func TestAPIProviderRefreshBusyThenSuccess(t *testing.T) {
	provider := &daemon.Provider{Tag: "busy"}
	client := &fakeAPIProviderRefreshClient{}
	client.refresh = func(context.Context, string) (*daemon.ProviderActionResult, error) {
		if len(client.calls) < 3 {
			return nil, status.Error(codes.ResourceExhausted, "provider action lock held")
		}
		return &daemon.ProviderActionResult{Provider: provider}, nil
	}
	var stderr bytes.Buffer
	providers, err := refreshAPIProviders(context.Background(), client, nil, []string{"busy"}, &stderr, 0, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 3 {
		t.Fatalf("refresh calls = %v, want three calls", client.calls)
	}
	if len(providers) != 1 || providers[0] != provider {
		t.Fatalf("providers = %v, want refreshed provider", providers)
	}
	if got, want := stderr.String(), "busy: already in progress, waiting...\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestAPIProviderRefreshBusyTimeoutContinues(t *testing.T) {
	busy := &daemon.Provider{Tag: "busy"}
	healthy := &daemon.Provider{Tag: "healthy"}
	client := &fakeAPIProviderRefreshClient{
		refresh: func(_ context.Context, tag string) (*daemon.ProviderActionResult, error) {
			if tag == "busy" {
				return nil, status.Error(codes.ResourceExhausted, "provider action lock held")
			}
			return &daemon.ProviderActionResult{Provider: healthy}, nil
		},
	}
	var stderr bytes.Buffer
	providers, err := refreshAPIProviders(context.Background(), client, []*daemon.Provider{busy, healthy}, []string{"busy", "healthy"}, &stderr, 0, time.Millisecond)
	if want := "failed to refresh providers: busy: already in progress"; err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
	if len(client.calls) < 2 || client.calls[len(client.calls)-1] != "healthy" {
		t.Fatalf("refresh calls = %v, want busy attempts followed by healthy", client.calls)
	}
	for _, tag := range client.calls[:len(client.calls)-1] {
		if tag != "busy" {
			t.Fatalf("unexpected refresh call for %q before healthy", tag)
		}
	}
	if len(providers) != 2 || providers[0] != busy || providers[1] != healthy {
		t.Fatalf("providers = %v, want listed busy provider and refreshed healthy provider", providers)
	}
	if got, want := stderr.String(), "busy: already in progress, waiting...\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestAPIProviderRefreshNonBusyError(t *testing.T) {
	client := &fakeAPIProviderRefreshClient{
		refresh: func(context.Context, string) (*daemon.ProviderActionResult, error) {
			return nil, status.Error(codes.PermissionDenied, "refresh denied")
		},
	}
	var stderr bytes.Buffer
	providers, err := refreshAPIProviders(context.Background(), client, nil, []string{"denied"}, &stderr, time.Hour, time.Hour)
	if want := "failed to refresh providers: denied: refresh denied"; err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
	if len(client.calls) != 1 {
		t.Fatalf("refresh calls = %v, want one call", client.calls)
	}
	if len(providers) != 0 {
		t.Fatalf("providers = %v, want none", providers)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want no waiting message", stderr.String())
	}
}

type cancelAPIProviderRefreshWriter struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelAPIProviderRefreshWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	w.cancel()
	return n, err
}

func TestAPIProviderRefreshCanceledWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &fakeAPIProviderRefreshClient{
		refresh: func(context.Context, string) (*daemon.ProviderActionResult, error) {
			return nil, status.Error(codes.ResourceExhausted, "provider action lock held")
		},
	}
	stderr := &cancelAPIProviderRefreshWriter{cancel: cancel}
	_, err := refreshAPIProviders(ctx, client, nil, []string{"busy", "other"}, stderr, time.Hour, time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("refresh calls = %v, want one call without retry or next tag", client.calls)
	}
	if got, want := stderr.String(), "busy: already in progress, waiting...\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}
