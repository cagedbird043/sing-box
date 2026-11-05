package nodeprobe

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// node dials every destination to one test server, standing in for a proxy
// whose exit behaves like that server.
type node struct {
	tag  string
	addr string
}

func (n *node) Type() string           { return "test" }
func (n *node) Tag() string            { return n.tag }
func (n *node) Network() []string      { return []string{N.NetworkTCP} }
func (n *node) Dependencies() []string { return nil }

func (n *node) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
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

func (g *group) Tag() string                    { return "test" }
func (g *group) Candidates() []adapter.Outbound { return g.candidates }
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
		logger:        log.NewNOPFactory().Logger(),
		targets:       []string{"http://probe.test/file"},
		downloadBytes: 64 * 1024,
		timeout:       2 * time.Second,
		maxRatio:      4,
		domesticURL:   "http://domestic.test/file",
		direct:        domestic,
		results:       make(map[string]result),
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
	s.checkDownloadGroup(context.Background(), &Handle{service: s, group: g}, make(map[string]bool))
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
	s.checkDownloadGroup(context.Background(), &Handle{service: s, group: g}, make(map[string]bool))
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
