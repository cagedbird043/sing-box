package nodeprobe

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// node dials every destination to one test server, standing in for a proxy
// whose exit behaves like that server.
type node struct {
	tag    string
	access sync.Mutex
	addr   string
	// When set, dials wait for it to close and then fail, like a member
	// closed by a provider refresh mid-dial.
	closed chan struct{}
	dialed chan struct{}
}

func (n *node) Type() string           { return "test" }
func (n *node) Tag() string            { return n.tag }
func (n *node) Network() []string      { return []string{N.NetworkTCP} }
func (n *node) Dependencies() []string { return nil }

func (n *node) set(addr string) {
	n.access.Lock()
	defer n.access.Unlock()
	n.addr = addr
}

func (n *node) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if n.closed != nil {
		close(n.dialed)
		<-n.closed
		return nil, net.ErrClosed
	}
	n.access.Lock()
	addr := n.addr
	n.access.Unlock()
	if addr == "" {
		return nil, net.ErrClosed
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, N.NetworkTCP, addr)
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

const testBytes = 64 * 1024

// server answers a ranged download, sending the body in 16 KiB chunks paced by
// gap. The gap is the slowness under test: a throttled exit.
func server(t *testing.T, gap time.Duration) string {
	t.Helper()
	instance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(testBytes))
		w.WriteHeader(http.StatusPartialContent)
		chunk := make([]byte, 16*1024)
		for sent := 0; sent < testBytes; sent += len(chunk) {
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

func testOptions(policy option.NodeProbePolicyOptions) option.NodeProbeServiceOptions {
	if policy.Screen == nil {
		policy.Screen = &option.NodeProbeScreenOptions{Measures: []string{"bulk"}}
	}
	return option.NodeProbeServiceOptions{
		Measures: map[string]option.NodeProbeMeasureOptions{
			"bulk": {
				Kind:    "download",
				Targets: []string{"http://probe.test/file"},
				Bytes:   testBytes,
				Timeout: badoption.Duration(2 * time.Second),
			},
		},
		Policies: map[string]option.NodeProbePolicyOptions{"p": policy},
	}
}

func newTestService(t *testing.T, policy option.NodeProbePolicyOptions, g *group) (*Service, *Handle) {
	t.Helper()
	s, err := newService(log.NewNOPFactory().Logger(), "probe", testOptions(policy), nil)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := s.Register(g, "p")
	if err != nil {
		t.Fatal(err)
	}
	return s, handle
}

func screen(s *Service) {
	s.screen(context.Background(), s.policyOrder, "test")
}

func TestScreenSelectsLowestScoreAmongQualified(t *testing.T) {
	t.Parallel()
	fast := &node{tag: "fast", addr: server(t, 0)}
	slow := &node{tag: "slow", addr: server(t, 60*time.Millisecond)}
	dead := &node{tag: "dead"}
	g := &group{candidates: []adapter.Outbound{dead, slow, fast}, current: dead}
	s, handle := newTestService(t, option.NodeProbePolicyOptions{
		Qualify: "m.bulk.ok_rate > 0 && m.bulk.throughput.p50 > 512*KiB",
		Score:   "m.bulk.total.p50",
	}, g)
	screen(s)
	if current := g.Current(); current != fast {
		t.Fatalf("selected %v, want fast", current.Tag())
	}
	snapshot := handle.Snapshot()
	if report := snapshot.Reports["slow"]; report.OK || report.Error != "qualify: m.bulk.throughput.p50 > 512 * KiB" {
		t.Fatalf("slow report: %+v", report)
	}
	if report := snapshot.Reports["dead"]; report.OK || report.Error != "qualify: m.bulk.ok_rate > 0" {
		t.Fatalf("dead report: %+v", report)
	}
	if stats := snapshot.Measures["fast"]["bulk"]; stats.Count != 1 || stats.Throughput.P50 <= 512*1024 {
		t.Fatalf("fast stats: %+v", stats)
	}
}

func TestSwitchExpressionGatesReplacement(t *testing.T) {
	t.Parallel()
	a := &node{tag: "a", addr: server(t, 0)}
	b := &node{tag: "b", addr: server(t, 0)}
	g := &group{candidates: []adapter.Outbound{a, b}, current: b}
	// Every member scores by index, so a is better; the switch expression
	// demands a 10x improvement, which index 0 vs 1 never gives.
	s, _ := newTestService(t, option.NodeProbePolicyOptions{
		Qualify: "m.bulk.last_ok",
		Switch:  "!current.qualified || best.score < current.score * 0.1 - 1",
	}, g)
	screen(s)
	if len(g.switches) != 0 {
		t.Fatalf("switched to %v despite the switch expression", g.switches)
	}
	b.set("")
	screen(s)
	if current := g.Current(); current != a {
		t.Fatalf("selected %v after the current member died, want a", current.Tag())
	}
}

func TestCheckVerifiesBeforeSwitching(t *testing.T) {
	t.Parallel()
	a := &node{tag: "a", addr: server(t, 0)}
	b := &node{tag: "b", addr: server(t, 0)}
	c := &node{tag: "c", addr: server(t, 0)}
	g := &group{candidates: []adapter.Outbound{a, b, c}, current: a}
	s, handle := newTestService(t, option.NodeProbePolicyOptions{
		Check:   &option.NodeProbeCheckOptions{Measures: []string{"bulk"}},
		Qualify: "m.bulk.last_ok",
	}, g)
	screen(s)
	if g.Current() != a {
		t.Fatalf("selected %v, want a", g.Current().Tag())
	}
	// a and b die; b ranks next but must be measured before it is chosen.
	a.set("")
	b.set("")
	s.check(context.Background(), handle, "test", make(map[string]bool))
	if !slices.Equal(g.switches, []string{"c"}) {
		t.Fatalf("switches %v, want only c", g.switches)
	}
}

func TestPenaltyExcludesMemberUntilItExpires(t *testing.T) {
	t.Parallel()
	a := &node{tag: "a", addr: server(t, 0)}
	b := &node{tag: "b", addr: server(t, 0)}
	g := &group{candidates: []adapter.Outbound{a, b}, current: a}
	s, handle := newTestService(t, option.NodeProbePolicyOptions{
		Qualify: "m.bulk.last_ok",
		Penalty: "!qualified && is_current ? 30*minute : 0",
	}, g)
	screen(s)
	a.set("")
	screen(s)
	if g.Current() != b {
		t.Fatalf("selected %v after a failed, want b", g.Current().Tag())
	}
	a.set(server(t, 0))
	screen(s)
	if g.Current() != b {
		t.Fatalf("returned to penalized a")
	}
	report := handle.Snapshot().Reports["a"]
	if report.OK || report.Error != "penalized" || time.Until(report.PenaltyUntil) < 29*time.Minute {
		t.Fatalf("a report: %+v", report)
	}
}

func TestTrafficFailuresDisqualifyCurrent(t *testing.T) {
	t.Parallel()
	a := &node{tag: "a", addr: server(t, 0)}
	b := &node{tag: "b", addr: server(t, 0)}
	g := &group{candidates: []adapter.Outbound{a, b}, current: a}
	s, handle := newTestService(t, option.NodeProbePolicyOptions{
		Check:   &option.NodeProbeCheckOptions{Measures: []string{"bulk"}, OnTrafficFailure: true},
		Qualify: "m.bulk.last_ok && (traffic.conns < 4 || traffic.fail_rate < 0.5)",
	}, g)
	screen(s)
	for range 3 {
		handle.ConnectionStarted("a")
		handle.ConnectionEnded("a", true, 100, 10)
	}
	handle.ConnectionStarted("a")
	handle.ConnectionEnded("a", false, 100, 10)
	select {
	case triggered := <-s.triggers:
		if triggered != handle {
			t.Fatal("unexpected trigger")
		}
	default:
		t.Fatal("a failing connection on the current member did not trigger a check")
	}
	s.check(context.Background(), handle, "test", make(map[string]bool))
	if g.Current() != b {
		t.Fatalf("selected %v, want b after a's real connections failed", g.Current().Tag())
	}
	traffic := handle.Snapshot().Traffic["a"]
	if traffic.Conns != 4 || traffic.Aborted != 3 || traffic.OK != 1 || traffic.BytesDown != 400 || traffic.FailRate != 0.75 {
		t.Fatalf("traffic: %+v", traffic)
	}
}

func TestConfigurationErrors(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		policy option.NodeProbePolicyOptions
		want   string
	}{
		{"unknown measure in expression", option.NodeProbePolicyOptions{Qualify: "m.bluk.ok_rate > 0"}, "unknown measure: bluk"},
		{"unknown measure in switch view", option.NodeProbePolicyOptions{Switch: "best.m.nope.count > 0"}, "unknown measure: nope"},
		{"non-boolean qualify", option.NodeProbePolicyOptions{Qualify: "m.bulk.count"}, "qualify"},
		{"unknown variable", option.NodeProbePolicyOptions{Score: "latency"}, "score"},
		{"unknown screen measure", option.NodeProbePolicyOptions{Screen: &option.NodeProbeScreenOptions{Measures: []string{"x"}}}, "unknown measure: x"},
	} {
		_, err := newService(log.NewNOPFactory().Logger(), "probe", testOptions(testCase.policy), nil)
		if err == nil || !strings.Contains(err.Error(), testCase.want) {
			t.Errorf("%s: error %v, want %q", testCase.name, err, testCase.want)
		}
	}
}

func TestReplacedMemberFailuresAreDiscarded(t *testing.T) {
	t.Parallel()
	old := &node{tag: "a", closed: make(chan struct{}), dialed: make(chan struct{})}
	fresh := &node{tag: "a", addr: server(t, 0)}
	g := &group{candidates: []adapter.Outbound{old}}
	s, handle := newTestService(t, option.NodeProbePolicyOptions{Qualify: "m.bulk.ok_rate == 1"}, g)
	done := make(chan struct{})
	go func() {
		screen(s)
		close(done)
	}()
	// A provider refresh swaps the member, then closes the old one mid-dial.
	<-old.dialed
	g.access.Lock()
	g.candidates = []adapter.Outbound{fresh}
	g.access.Unlock()
	close(old.closed)
	<-done
	if g.Current() != fresh {
		t.Fatalf("selected %v, want the replacement", g.Current())
	}
	if stats := handle.Snapshot().Measures["a"]["bulk"]; stats.Count != 1 || stats.OKRate != 1 {
		t.Fatalf("stats %+v: the replaced member's failure was recorded", stats)
	}
}
