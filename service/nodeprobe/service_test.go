package nodeprobe

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/observable"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
)

// node dials every destination to one test server, standing in for a proxy
// whose exit behaves like that server.
type node struct {
	tag   string
	addr  string
	dials atomic.Int32
}

func (n *node) Type() string           { return "test" }
func (n *node) Tag() string            { return n.tag }
func (n *node) Network() []string      { return []string{N.NetworkTCP} }
func (n *node) Dependencies() []string { return nil }

func (n *node) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	n.dials.Add(1)
	if n.addr == "" {
		return nil, net.ErrClosed
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, N.NetworkTCP, n.addr)
}

func (n *node) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, net.ErrClosed
}

type group struct {
	access     sync.Mutex
	candidates []adapter.Outbound
	current    adapter.Outbound
	switches   []string
}

func (g *group) Tag() string { return "test" }
func (g *group) Candidates() []adapter.Outbound {
	g.access.Lock()
	defer g.access.Unlock()
	return slices.Clone(g.candidates)
}
func (g *group) Current() adapter.Outbound {
	g.access.Lock()
	defer g.access.Unlock()
	return g.current
}

func (g *group) Choose(outbound adapter.Outbound, reason string) {
	g.access.Lock()
	defer g.access.Unlock()
	g.current = outbound
	g.switches = append(g.switches, outbound.Tag())
}

// server answers a ranged download after delay, then sends the body in 16 KiB
// chunks paced by gap.
func server(t *testing.T, delay, gap time.Duration) string {
	t.Helper()
	instance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		const size = 64 * 1024
		w.Header().Set("Content-Length", strconv.Itoa(size))
		w.WriteHeader(http.StatusPartialContent)
		chunk := make([]byte, 16*1024)
		for sent := 0; sent < size; sent += len(chunk) {
			if sent > 0 {
				time.Sleep(gap)
			}
			_, _ = w.Write(chunk)
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(instance.Close)
	return instance.Listener.Addr().String()
}

func newTestService(domestic *node) *Service {
	return &Service{
		logger:         log.NewNOPFactory().Logger(),
		targets:        []string{"http://probe.test/file"},
		downloadBytes:  64 * 1024,
		timeout:        2 * time.Second,
		maxRatio:       4,
		domesticURL:    "http://domestic.test/file",
		direct:         domestic,
		results:        make(map[string]result),
		networkChanged: make(chan struct{}, 1),
		triggers:       make(chan *Handle, 64),
		screenRequests: make(chan struct{}, 1),
		screenInterval: time.Hour,
		checkInterval:  time.Hour,
	}
}

func TestFetchJudgesTransferAgainstFirstByte(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// Far but healthy: slow first byte, quick body.
	far := fetch(ctx, &node{addr: server(t, 200*time.Millisecond, 0)}, "http://probe.test/file", 64*1024, 2*time.Second)
	if far.err != nil || far.junk(4) {
		t.Fatalf("far healthy node judged junk: %+v", far)
	}
	// Near but throttled: quick first byte, body drags.
	throttled := fetch(ctx, &node{addr: server(t, 0, 150*time.Millisecond)}, "http://probe.test/file", 64*1024, 2*time.Second)
	if throttled.err != nil || !throttled.junk(4) {
		t.Fatalf("throttled node passed: %+v", throttled)
	}
	timedOut := fetch(ctx, &node{addr: server(t, 0, time.Second)}, "http://probe.test/file", 64*1024, 500*time.Millisecond)
	if timedOut.err == nil {
		t.Fatal("download over the time limit passed")
	}
	short := fetch(ctx, &node{addr: server(t, 20*time.Millisecond, 0)}, "http://probe.test/file", 128*1024, 2*time.Second)
	if short.err == nil {
		t.Fatal("body shorter than the requested size passed")
	}
}

func TestCheckSwitchesAlongPoolPastJunk(t *testing.T) {
	t.Parallel()
	healthy := server(t, 20*time.Millisecond, 0)
	throttled := server(t, 0, 150*time.Millisecond)
	s := newTestService(&node{addr: healthy})
	current := &node{tag: "current", addr: throttled}
	stale := &node{tag: "stale", addr: throttled}
	good := &node{tag: "good", addr: healthy}
	g := &group{candidates: []adapter.Outbound{current, stale, good}, current: current}
	s.results = map[string]result{
		"current": {ok: true, latency: time.Millisecond},
		"stale":   {ok: true, latency: 2 * time.Millisecond},
		"good":    {ok: true, latency: 3 * time.Millisecond},
	}
	s.checkDownloadGroup(context.Background(), &Handle{service: s, group: g}, make(map[string]bool), false)
	if g.current != good {
		t.Fatalf("selected %s, want good", g.current.Tag())
	}
	if len(g.switches) != 1 {
		t.Fatalf("switched %v, want a single switch to the first passing candidate", g.switches)
	}
	if s.results["stale"].ok || s.results["current"].ok {
		t.Fatal("disqualified members stayed in the pool")
	}
}

func TestCheckKeepsSelectionWhenLocalNetworkIsDown(t *testing.T) {
	t.Parallel()
	s := newTestService(&node{})
	current := &node{tag: "current"}
	good := &node{tag: "good", addr: server(t, 20*time.Millisecond, 0)}
	g := &group{candidates: []adapter.Outbound{current, good}, current: current}
	s.results = map[string]result{"current": {ok: true}, "good": {ok: true}}
	s.checkDownloadGroup(context.Background(), &Handle{service: s, group: g}, make(map[string]bool), false)
	if g.current != current || !s.results["current"].ok {
		t.Fatal("selection judged while the domestic check failed")
	}
}

func TestScreenKeepsPassingSelectionUnlessNetworkChanged(t *testing.T) {
	t.Parallel()
	healthy := server(t, 20*time.Millisecond, 0)
	slow := server(t, 300*time.Millisecond, 0)
	s := newTestService(&node{addr: healthy})
	current := &node{tag: "current", addr: slow}
	fastest := &node{tag: "fastest", addr: healthy}
	broken := &node{tag: "broken"}
	g := &group{candidates: []adapter.Outbound{current, fastest, broken}, current: current}
	s.groups = []*Handle{{service: s, group: g}}
	s.screen(context.Background(), false, "test")
	if g.current != current {
		t.Fatalf("passing selection replaced by %s", g.current.Tag())
	}
	if s.results["broken"].ok {
		t.Fatal("unreachable node entered the pool")
	}
	s.screen(context.Background(), true, "test")
	if g.current != fastest {
		t.Fatalf("network change selected %s, want fastest", g.current.Tag())
	}
}

func TestScreenTreatsAllNodesFailingAsNetworkProblem(t *testing.T) {
	t.Parallel()
	s := newTestService(&node{addr: server(t, 20*time.Millisecond, 0)})
	current := &node{tag: "current"}
	g := &group{candidates: []adapter.Outbound{current, &node{tag: "other"}}, current: current}
	s.groups = []*Handle{{service: s, group: g}}
	s.results = map[string]result{"current": {ok: true}}
	s.screen(context.Background(), true, "test")
	if g.current != current || !s.results["current"].ok {
		t.Fatal("pool replaced although every node failed")
	}
}

func TestRequestGroupPrefersOrder(t *testing.T) {
	t.Parallel()
	s := newTestService(&node{addr: server(t, 20*time.Millisecond, 0)})
	direct := &node{tag: "direct", addr: server(t, 100*time.Millisecond, 0)}
	proxy := &node{tag: "proxy", addr: server(t, 20*time.Millisecond, 0)}
	g := &group{candidates: []adapter.Outbound{direct, proxy}, current: proxy}
	handle := &Handle{service: s, group: g, options: GroupOptions{URL: "http://target.test/", PreferOrder: true}}
	s.checkRequestGroup(context.Background(), handle, false)
	if g.current != direct {
		t.Fatalf("selected %s, want the first reachable member", g.current.Tag())
	}
	direct.addr = ""
	s.checkRequestGroup(context.Background(), handle, false)
	if g.current != proxy {
		t.Fatalf("selected %s after direct broke, want proxy", g.current.Tag())
	}
}

func TestTriggeredCheckLeavesHungNodeAtOnce(t *testing.T) {
	t.Parallel()
	healthy := server(t, 20*time.Millisecond, 0)
	s := newTestService(&node{addr: healthy})
	s.timeout = 5 * time.Second
	// Longer than the triggered limit, shorter than the scheduled one.
	hung := &node{tag: "hung", addr: server(t, 3*time.Second, 0)}
	good := &node{tag: "good", addr: healthy}
	first := &group{candidates: []adapter.Outbound{hung, good}, current: hung}
	second := &group{candidates: []adapter.Outbound{hung, good}, current: hung}
	other := &group{candidates: []adapter.Outbound{hung, good}, current: good}
	s.groups = []*Handle{{service: s, group: first}, {service: s, group: second}, {service: s, group: other}}
	s.results = map[string]result{"hung": {ok: true, latency: time.Millisecond}, "good": {ok: true, latency: 2 * time.Millisecond}}
	start := time.Now()
	s.check(context.Background(), s.sharingSelection(s.groups[0]), "test", true)
	if elapsed := time.Since(start); elapsed > triggeredTimeout+time.Second {
		t.Fatalf("leaving the hung node took %s", elapsed)
	}
	if first.current != good || second.current != good {
		t.Fatal("a group on the hung node kept it")
	}
	if hung.dials.Load() != 1 {
		t.Fatalf("hung node tested %d times, want once for both groups", hung.dials.Load())
	}
	if len(other.switches) != 0 {
		t.Fatal("a group on another node was switched")
	}
}

func waitState(t *testing.T, handle *Handle, predicate func(State) bool) State {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, _ := handle.Snapshot()
		if predicate(state) {
			return state
		}
		time.Sleep(time.Millisecond)
	}
	state, _ := handle.Snapshot()
	t.Fatalf("screen state did not settle: %+v", state)
	return state
}

func TestManualScreenDownloadsSharedPoolAndCoalesces(t *testing.T) {
	t.Parallel()
	healthy := server(t, 20*time.Millisecond, 0)
	selected := &node{tag: "selected", addr: server(t, 100*time.Millisecond, 0)}
	fast := &node{tag: "fast", addr: healthy}
	s := newTestService(&node{addr: healthy})
	g := &group{candidates: []adapter.Outbound{selected, fast}, current: selected}
	handle := s.Register(g, GroupOptions{})
	other := s.Register(&group{candidates: []adapter.Outbound{selected, fast}, current: fast}, GroupOptions{})
	s.history = urltest.NewHistoryStorage()
	hook := observable.NewSubscriber[struct{}](64)
	defer hook.Close()
	s.history.AddUpdateHook(hook)
	updates, _ := hook.Subscription()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.loop(ctx) }()
	defer func() { cancel(); <-done }()
	waitState(t, handle, func(state State) bool { return !state.Running && state.Generation == 1 })
	for len(updates) > 0 {
		<-updates
	}
	before := selected.dials.Load()
	handle.Screen()
	waitState(t, handle, func(state State) bool { return state.Running })
	var callers sync.WaitGroup
	for range 32 {
		callers.Add(1)
		go func() { defer callers.Done(); handle.Screen(); other.Screen() }()
	}
	callers.Wait()
	finished := waitState(t, handle, func(state State) bool { return !state.Running && state.Generation == 2 })
	if finished.Error != "" || finished.ScreenedAt.IsZero() || selected.dials.Load() != before+1 {
		t.Fatalf("manual screening did not perform one shared download: %+v dials=%d", finished, selected.dials.Load()-before)
	}
	if g.Current() != selected {
		t.Fatal("manual screening replaced a passing selection")
	}
	if len(s.screenRequests) != 0 {
		t.Fatal("concurrent requests queued a redundant screening")
	}
	if len(updates) < 2 {
		t.Fatal("screen start/completion did not publish updates without a selection change")
	}
	_, reports := other.Snapshot()
	if !reports["selected"].OK || !reports["fast"].OK {
		t.Fatalf("shared pool reports missing: %+v", reports)
	}
}

func TestScreenTerminalStates(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"offline", "paused", "all failed", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			domestic := &node{addr: server(t, 10*time.Millisecond, 0)}
			s := newTestService(domestic)
			current := &node{tag: "broken"}
			handle := s.Register(&group{candidates: []adapter.Outbound{current}, current: current}, GroupOptions{})
			s.history = urltest.NewHistoryStorage()
			hook := observable.NewSubscriber[struct{}](8)
			defer hook.Close()
			s.history.AddUpdateHook(hook)
			updates, _ := hook.Subscription()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "offline":
				domestic.addr = ""
			case "paused":
				s.pause = service.FromContext[pause.Manager](pause.WithDefaultManager(ctx))
				s.pause.DevicePause()
			case "cancelled":
				cancel()
			}
			s.screen(ctx, false, "requested")
			state, reports := handle.Snapshot()
			if state.Running || state.Generation != 1 || state.ScreenedAt.IsZero() || state.Error == "" {
				t.Fatalf("unterminated or falsely successful %s: %+v", mode, state)
			}
			if len(updates) < 2 {
				t.Fatal("terminal screening did not publish start and completion")
			}
			if mode == "all failed" {
				if report, exists := reports["broken"]; !exists || report.OK || report.Error == "" {
					t.Fatalf("failed member not reported: %+v", reports)
				}
			} else if len(reports) != 0 {
				t.Fatalf("skipped members must remain unchecked: %+v", reports)
			}
		})
	}
}

func TestRequestReportsAreIsolatedByGroupAndDownload(t *testing.T) {
	t.Parallel()
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/drop" {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer fixture.Close()
	n := &node{tag: "same", addr: fixture.Listener.Addr().String()}
	s := newTestService(&node{addr: server(t, 10*time.Millisecond, 0)})
	pass := s.Register(&group{candidates: []adapter.Outbound{n}, current: n}, GroupOptions{URL: fixture.URL + "/ok"})
	fail := s.Register(&group{candidates: []adapter.Outbound{n}, current: n}, GroupOptions{URL: fixture.URL + "/drop"})
	download := s.Register(&group{candidates: []adapter.Outbound{n}, current: n}, GroupOptions{})
	s.record(nil, n, sample{firstByte: 750 * time.Microsecond, transfer: 1250 * time.Microsecond}, true)
	s.checkRequestGroup(context.Background(), pass, false)
	s.checkRequestGroup(context.Background(), fail, false)
	passState, passReports := pass.Snapshot()
	failState, failReports := fail.Snapshot()
	_, downloadReports := download.Snapshot()
	if passState.Error != "" || !passReports[n.tag].OK || passReports[n.tag].Transfer != 0 {
		t.Fatalf("successful request contaminated: %+v %+v", passState, passReports)
	}
	if failState.Error == "" || failReports[n.tag].OK || failReports[n.tag].Error == "" {
		t.Fatalf("failed request hidden: %+v %+v", failState, failReports)
	}
	if report := downloadReports[n.tag]; !report.OK || report.Ratio != float64(1250)/750 || report.FirstByte.Milliseconds() != 0 {
		t.Fatalf("download overwritten or ratio truncated: %+v", report)
	}
	delete(downloadReports, n.tag)
	_, retained := download.Snapshot()
	if len(retained) != 1 {
		t.Fatal("snapshot exposed mutable internal map")
	}
}

func TestPreferOrderLeavesSkippedMembersUnchecked(t *testing.T) {
	t.Parallel()
	good := server(t, 10*time.Millisecond, 0)
	first := &node{tag: "first"}
	second := &node{tag: "second", addr: good}
	s := newTestService(&node{addr: good})
	handle := s.Register(&group{candidates: []adapter.Outbound{first, second}, current: second}, GroupOptions{URL: "http://test/", PreferOrder: true})
	s.checkRequestGroup(context.Background(), handle, false)
	_, previous := handle.Snapshot()
	if len(previous) != 2 {
		t.Fatal("initial fallback was not tested")
	}
	first.addr = good
	s.screen(context.Background(), false, "requested")
	state, reports := handle.Snapshot()
	if state.Running || state.Generation != 1 || state.Error != "" || len(reports) != 1 || !reports[first.tag].OK {
		t.Fatalf("early stop retained stale member or failed to complete: %+v %+v", state, reports)
	}
}

func TestConcurrentSnapshotsAndScreenPruneStaleMembers(t *testing.T) {
	t.Parallel()
	good := server(t, 5*time.Millisecond, 0)
	n := &node{tag: "live", addr: good}
	s := newTestService(&node{addr: good})
	removed := &node{tag: "removed", addr: good}
	g := &group{candidates: []adapter.Outbound{n, removed}, current: n}
	handle := s.Register(g, GroupOptions{})
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				state, reports := handle.Snapshot()
				_ = state
				delete(reports, "live")
			}
		}()
	}
	for round := range 4 {
		s.screen(context.Background(), false, "test")
		if round == 0 {
			g.access.Lock()
			g.candidates = []adapter.Outbound{n}
			g.access.Unlock()
		}
	}
	close(stop)
	readers.Wait()
	state, reports := handle.Snapshot()
	if state.Generation != 4 || len(reports) != 1 || !reports["live"].OK || len(s.results) != 1 {
		t.Fatalf("stale data retained or snapshot mutation leaked: %+v %+v %+v", state, reports, s.results)
	}
}

func TestManualScreenWhilePausedTerminates(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	s := newTestService(&node{})
	s.pause = service.FromContext[pause.Manager](pause.WithDefaultManager(ctx))
	s.pause.NetworkPause()
	n := &node{tag: "unchecked"}
	handle := s.Register(&group{candidates: []adapter.Outbound{n}, current: n}, GroupOptions{})
	done := make(chan struct{})
	go func() { defer close(done); s.loop(ctx) }()
	defer func() { cancel(); <-done }()
	waitState(t, handle, func(state State) bool { return !state.Running && state.Generation == 1 })
	handle.Screen()
	state := waitState(t, handle, func(state State) bool { return !state.Running && state.Generation == 2 })
	if state.Error == "" || n.dials.Load() != 0 {
		t.Fatalf("paused manual request did not terminate honestly: %+v dials=%d", state, n.dials.Load())
	}
}

func TestFailedDownloadsDoNotMaskRequestScreen(t *testing.T) {
	t.Parallel()
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer fixture.Close()
	n := &node{tag: "same", addr: fixture.Listener.Addr().String()}
	s := newTestService(&node{addr: server(t, 10*time.Millisecond, 0)})
	download := s.Register(&group{candidates: []adapter.Outbound{n}, current: n}, GroupOptions{})
	request := s.Register(&group{candidates: []adapter.Outbound{n}, current: n}, GroupOptions{URL: fixture.URL})
	s.results[n.tag] = result{ok: true}
	s.screen(context.Background(), false, "requested")
	downloadState, downloadReports := download.Snapshot()
	requestState, requestReports := request.Snapshot()
	if downloadState.Generation != 1 || downloadState.Running || downloadState.Error == "" || downloadReports[n.tag].OK {
		t.Fatalf("all-failed download screen faked success: %+v %+v", downloadState, downloadReports)
	}
	if requestState.Generation != 1 || requestState.Running || requestState.Error != "" || !requestReports[n.tag].OK {
		t.Fatalf("download failure masked independent request: %+v %+v", requestState, requestReports)
	}
	if !s.results[n.tag].ok {
		t.Fatal("all-failed screening destroyed the previous pool")
	}
}
