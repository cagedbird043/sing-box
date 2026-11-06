package nodeprobe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
)

// compositeServer separates first-byte delay from body transfer for a complete
// default-sized download. Assertions use the measured samples, not exact sleeps.
func compositeServer(t *testing.T, firstByte, transfer time.Duration) string {
	t.Helper()
	instance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(firstByte)
		w.Header().Set("Content-Length", strconv.Itoa(defaultDownloadBytes))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte{0})
		w.(http.Flusher).Flush()
		time.Sleep(transfer)
		_, _ = w.Write(make([]byte, defaultDownloadBytes-1))
	}))
	t.Cleanup(instance.Close)
	return instance.Listener.Addr().String()
}

func TestCompositeHTTPExperiment(t *testing.T) {
	ctx := context.Background()
	a := &node{tag: "A", addr: compositeServer(t, 300*time.Millisecond, 300*time.Millisecond)}
	b := &node{tag: "B", addr: compositeServer(t, 400*time.Millisecond, 20*time.Millisecond)}
	far := &node{tag: "low-ratio-long-first-byte", addr: compositeServer(t, 900*time.Millisecond, time.Millisecond)}
	s := newTestService(&node{addr: compositeServer(t, 0, 0)})
	s.downloadBytes, s.maxRatio = defaultDownloadBytes, defaultMaxRTTRatio
	g := &group{candidates: []adapter.Outbound{a, b, far}, current: a}
	handle := s.Register(g, GroupOptions{})
	s.screen(ctx, false, "experiment")
	_, reports := handle.Snapshot()
	for _, candidate := range g.candidates {
		r := reports[candidate.Tag()]
		if !r.OK {
			t.Fatalf("fixture must be eligible: %s %+v", candidate.Tag(), r)
		}
		t.Logf("%s first=%s transfer=%s total=%s ratio=%.3f", candidate.Tag(), r.FirstByte, r.Transfer, r.FirstByte+r.Transfer, r.Ratio)
	}
	ar, br, fr := reports[a.tag], reports[b.tag], reports[far.tag]
	if !(ar.FirstByte < br.FirstByte && br.FirstByte+br.Transfer < ar.FirstByte+ar.Transfer && fr.Ratio < br.Ratio && fr.FirstByte+fr.Transfer > br.FirstByte+br.Transfer) {
		t.Fatal("fixture did not produce the intended measured ordering")
	}
	if g.Current() != b {
		t.Fatalf("composite policy selected %s, want B", g.Current().Tag())
	}
	for _, candidate := range []adapter.Outbound{a, b} {
		measured := s.download(ctx, candidate, s.timeout)
		if measured.junk(s.maxRatio) {
			t.Fatalf("actual request failed: %+v", measured)
		}
		t.Logf("actual request %s completed in %s", candidate.Tag(), measured.firstByte+measured.transfer)
	}
}

func TestPoolUsesFullPrecisionAndStableCandidateOrder(t *testing.T) {
	t.Parallel()
	a, b, c := &node{tag: "A"}, &node{tag: "B"}, &node{tag: "C"}
	g := &group{candidates: []adapter.Outbound{a, b, c}, current: c}
	s := newTestService(nil)
	// All scores truncate to the same integer millisecond. Map insertion and
	// current selection must not affect ties or discard sub-millisecond ranks.
	s.results = map[string]result{
		"C": {ok: true, score: 1999 * time.Microsecond},
		"B": {ok: true, score: 1001 * time.Microsecond},
		"A": {ok: true, score: 1001 * time.Microsecond},
	}
	handle := &Handle{service: s, group: g}
	for range 10 {
		pool := s.pool(handle, nil)
		if len(pool) != 3 || pool[0] != a || pool[1] != b || pool[2] != c {
			t.Fatalf("unstable or truncated ranking: %v", pool)
		}
		g.Choose(pool[0], "test")
	}
	if pool := s.pool(handle, map[string]bool{"A": true}); len(pool) != 2 || pool[0] != b {
		t.Fatalf("fallback exclusion changed stable ordering: %v", pool)
	}
}

func TestCurrentHealthCheckKeepsPassingSelectionWithoutFullScreen(t *testing.T) {
	t.Parallel()
	current := &node{tag: "current", addr: compositeServer(t, 20*time.Millisecond, 0)}
	faster := &node{tag: "faster", addr: compositeServer(t, 0, 0)}
	s := newTestService(&node{})
	g := &group{candidates: []adapter.Outbound{current, faster}, current: current}
	handle := s.Register(g, GroupOptions{})
	s.results = map[string]result{"current": {ok: true, score: time.Second}, "faster": {ok: true, score: time.Millisecond}}
	s.check(context.Background(), []*Handle{handle}, "interval", false)
	if g.Current() != current || len(g.switches) != 0 || faster.dials.Load() != 0 || current.dials.Load() != 1 {
		t.Fatalf("health check screened or switched a passing selection: switches=%v current=%d faster=%d", g.switches, current.dials.Load(), faster.dials.Load())
	}
	_, reports := handle.Snapshot()
	r := reports[current.tag]
	if !r.OK || s.results[current.tag].score != r.FirstByte+r.Transfer {
		t.Fatalf("verify did not retain full completion score: %+v %+v", r, s.results[current.tag])
	}
}

func TestFailedCurrentUsesScreenedCompletionOrder(t *testing.T) {
	t.Parallel()
	healthy := compositeServer(t, 20*time.Millisecond, 0)
	current := &node{tag: "failed"}
	a, b := &node{tag: "A", addr: healthy}, &node{tag: "B", addr: healthy}
	s := newTestService(&node{addr: healthy})
	g := &group{candidates: []adapter.Outbound{current, a, b}, current: current}
	handle := s.Register(g, GroupOptions{})
	s.results = map[string]result{
		"failed": {ok: true, score: time.Millisecond},
		"A":      {ok: true, score: 600 * time.Millisecond},
		"B":      {ok: true, score: 420 * time.Millisecond},
	}
	s.check(context.Background(), []*Handle{handle}, "interval", false)
	if g.Current() != b || a.dials.Load() != 0 || b.dials.Load() != 1 || len(g.switches) != 1 {
		t.Fatalf("fallback did not consume screened completion order: selected=%s switches=%v A=%d B=%d", g.Current().Tag(), g.switches, a.dials.Load(), b.dials.Load())
	}
}

func TestRequestScreenRetainsPassingCurrentUnlessReselect(t *testing.T) {
	t.Parallel()
	current := &node{tag: "current", addr: compositeServer(t, 150*time.Millisecond, 0)}
	faster := &node{tag: "faster", addr: compositeServer(t, 0, 0)}
	s := newTestService(&node{addr: compositeServer(t, 0, 0)})
	g := &group{candidates: []adapter.Outbound{current, faster}, current: current}
	s.Register(g, GroupOptions{URL: "http://request.test/"})
	s.screen(context.Background(), false, "requested")
	if g.Current() != current || len(g.switches) != 0 {
		t.Fatal("download reselection policy leaked into a request group")
	}
	s.screen(context.Background(), true, "network changed")
	if g.Current() != faster {
		t.Fatal("request network-change reselection lost its first-byte policy")
	}
}
