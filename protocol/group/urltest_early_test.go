package group

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/observable"
)

// Each probe has its own gate; no test waits for the production probe timeout.
type earlyTestOutbound struct {
	adapter.Outbound
	tag       string
	networks  []string
	started   chan struct{}
	release   chan struct{}
	probeDone chan struct{}
	onNetwork func()
	once      sync.Once
	fail      bool
}

func newEarlyTestOutbound(t *testing.T, tag string, networks ...string) *earlyTestOutbound {
	t.Helper()
	if len(networks) == 0 {
		networks = []string{N.NetworkTCP, N.NetworkUDP}
	}
	o := &earlyTestOutbound{tag: tag, networks: networks, started: make(chan struct{}), release: make(chan struct{}), probeDone: make(chan struct{})}
	t.Cleanup(o.unblock)
	return o
}

func (o *earlyTestOutbound) Tag() string { return o.tag }
func (o *earlyTestOutbound) Network() []string {
	if o.onNetwork != nil {
		o.onNetwork()
	}
	return o.networks
}
func (o *earlyTestOutbound) unblock() { o.once.Do(func() { close(o.release) }) }
func (o *earlyTestOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	close(o.started)
	select {
	case <-o.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if o.fail {
		return nil, errors.New("controlled probe failure")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, network, destination.String())
	if err != nil {
		return nil, err
	}
	return &earlyTestConn{Conn: conn, done: o.probeDone}, nil
}

type earlyTestConn struct {
	net.Conn
	done chan struct{}
	once sync.Once
}

func (c *earlyTestConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.done) })
	return err
}

// Network is read under nodesAccess by selection. Waiting for this signal,
// then reading the selection, ensures the rejected-result callback has finished.
func watchEarlyTestSelectionAfter(a, probed *earlyTestOutbound) <-chan struct{} {
	selected := make(chan struct{})
	var once sync.Once
	a.onNetwork = func() {
		select {
		case <-probed.probeDone:
			once.Do(func() { close(selected) })
		default:
		}
	}
	return selected
}

type earlyTestRound struct {
	t       *testing.T
	group   *URLTestGroup
	history *urltest.HistoryStorage
	manager *historyTestManager
	updates <-chan struct{}
	done    chan struct{}
}

func newEarlyTestRound(t *testing.T, nodes ...adapter.Outbound) *earlyTestRound {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	history := urltest.NewHistoryStorage()
	hook := observable.NewSubscriber[struct{}](128)
	history.AddUpdateHook(hook)
	updates, _ := hook.Subscription()
	t.Cleanup(func() { history.Close(); hook.Close() })
	manager := &historyTestManager{nodes: make(map[string]adapter.Outbound)}
	for _, node := range nodes {
		manager.nodes[node.Tag()] = node
	}
	g := &URLTestGroup{
		ctx: context.Background(), outbound: manager, history: history,
		logger: log.NewNOPFactory().Logger(), outbounds: nodes, link: server.URL,
		interval: time.Hour, tolerance: 1, interruptGroup: interrupt.NewGroup(),
	}
	if len(nodes) > 0 {
		g.selectedOutboundTCP, _ = g.selectLocked(N.NetworkTCP)
		g.selectedOutboundUDP, _ = g.selectLocked(N.NetworkUDP)
	}
	return &earlyTestRound{t: t, group: g, history: history, manager: manager, updates: updates, done: make(chan struct{})}
}

func waitEarlyTest(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for controlled probe")
	}
}

func (r *earlyTestRound) start(force bool) {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r.t.Cleanup(func() { cancel(); waitEarlyTest(r.t, r.done) })
	go func() {
		defer close(r.done)
		if _, err := r.group.urlTest(ctx, force); err != nil {
			r.t.Errorf("urlTest: %v", err)
		}
	}()
}

func (r *earlyTestRound) waitSelection(network string, want adapter.Outbound) {
	r.t.Helper()
	for r.group.selected(network) != want {
		waitEarlyTest(r.t, r.updates)
	}
}

func (r *earlyTestRound) waitHistory(node adapter.Outbound) {
	r.t.Helper()
	for r.history.LoadURLTestHistory(node.Tag(), r.group.link) == nil {
		waitEarlyTest(r.t, r.updates)
	}
}

func (r *earlyTestRound) assertSelection(network string, want adapter.Outbound) {
	r.t.Helper()
	if got := r.group.selected(network); got != want {
		r.t.Fatalf("%s selected %s, want %s", network, got.Tag(), want.Tag())
	}
}

func (r *earlyTestRound) assertRunning() {
	r.t.Helper()
	select {
	case <-r.done:
		r.t.Fatal("round ended before blocked probe was released")
	default:
	}
}

// Plan test 1.
func TestURLTestEarlySelection(t *testing.T) {
	a, b := newEarlyTestOutbound(t, "a"), newEarlyTestOutbound(t, "b")
	r := newEarlyTestRound(t, a, b)
	a.fail = true
	b.unblock()
	r.start(true)
	waitEarlyTest(t, a.started)
	r.waitSelection(N.NetworkTCP, b)
	r.assertRunning()
	a.unblock()
	waitEarlyTest(t, r.done)
	r.assertSelection(N.NetworkTCP, b)
}

// Plan test 2.
func TestURLTestExistingHistoryWaitsForRoundEnd(t *testing.T) {
	a, b := newEarlyTestOutbound(t, "a"), newEarlyTestOutbound(t, "b")
	r := newEarlyTestRound(t, a, b)
	storeHistory(r.history, a.Tag(), r.group.link, 60000)
	a.fail = true
	b.unblock()
	r.start(true)
	r.waitHistory(b)
	r.assertSelection(N.NetworkTCP, a)
	r.assertSelection(N.NetworkUDP, a)
	r.assertRunning()
	a.unblock()
	waitEarlyTest(t, r.done)
	r.assertSelection(N.NetworkTCP, b)
}

// Plan test 3.
func TestURLTestEarlySelectionPerNetwork(t *testing.T) {
	a := newEarlyTestOutbound(t, "a")
	b := newEarlyTestOutbound(t, "b", N.NetworkTCP)
	c := newEarlyTestOutbound(t, "c")
	r := newEarlyTestRound(t, a, b, c)
	a.fail = true
	r.start(true)
	waitEarlyTest(t, a.started)
	b.unblock()
	r.waitSelection(N.NetworkTCP, b)
	r.assertSelection(N.NetworkUDP, a)
	c.unblock()
	r.waitSelection(N.NetworkUDP, c)
	r.assertSelection(N.NetworkTCP, b)
	r.assertRunning()
	a.unblock()
	waitEarlyTest(t, r.done)
}

// Plan test 4. The selector's unselected leaf must not satisfy pending selection.
func TestURLTestEarlySelectionIgnoresUnselectedLeaf(t *testing.T) {
	a, b, c := newEarlyTestOutbound(t, "a"), newEarlyTestOutbound(t, "b"), newEarlyTestOutbound(t, "c")
	selector := &Selector{Adapter: outbound.NewAdapter("selector", "selector", nil, nil), tags: []string{"a", "b"}}
	selector.selected.Store(a)
	r := newEarlyTestRound(t, selector, c)
	r.manager.nodes[a.Tag()], r.manager.nodes[b.Tag()] = a, b
	a.fail = true
	checked := watchEarlyTestSelectionAfter(a, b)
	r.start(true)
	b.unblock()
	waitEarlyTest(t, checked)
	r.assertSelection(N.NetworkTCP, selector)
	c.unblock()
	r.waitSelection(N.NetworkTCP, c)
	r.assertRunning()
	a.unblock()
	waitEarlyTest(t, r.done)
}

// Plan test 5. Provider checks use the same generation-aware history write API.
func TestURLTestEarlySelectionReconcilesSharedHistory(t *testing.T) {
	a, b := newEarlyTestOutbound(t, "a"), newEarlyTestOutbound(t, "b")
	r := newEarlyTestRound(t, a, b)
	storeHistory(r.history, b.Tag(), r.group.link, 10)
	r.start(false)
	waitEarlyTest(t, a.started)
	r.assertSelection(N.NetworkTCP, b)
	r.assertSelection(N.NetworkUDP, b)
	r.assertRunning()
	select {
	case <-b.started:
		t.Fatal("fresh shared history was not reused")
	default:
	}
	a.unblock()
	waitEarlyTest(t, r.done)
}

// Plan test 6.
func TestURLTestRejectedGenerationKeepsEarlySelectionPending(t *testing.T) {
	a, b, c := newEarlyTestOutbound(t, "a"), newEarlyTestOutbound(t, "b"), newEarlyTestOutbound(t, "c")
	r := newEarlyTestRound(t, a, b, c)
	a.fail = true
	checked := watchEarlyTestSelectionAfter(a, b)
	r.start(true)
	waitEarlyTest(t, b.started)
	r.history.DeleteOutboundURLTestHistory(b.Tag())
	b.unblock()
	waitEarlyTest(t, checked)
	r.assertSelection(N.NetworkTCP, a)
	c.unblock()
	r.waitSelection(N.NetworkTCP, c)
	if r.history.LoadURLTestHistory(b.Tag(), r.group.link) != nil {
		t.Fatal("retired generation result was stored")
	}
	r.assertRunning()
	a.unblock()
	waitEarlyTest(t, r.done)
}

// Plan test 7. B wins before A completes; the first available member wins at end.
func TestURLTestEarlySelectionFallback(t *testing.T) {
	a, b := newEarlyTestOutbound(t, "a"), newEarlyTestOutbound(t, "b")
	r := newEarlyTestRound(t, a, b)
	r.group.fallback = option.URLTestFallbackOptions{Enabled: true}
	b.unblock()
	r.start(true)
	r.waitSelection(N.NetworkTCP, b)
	r.assertRunning()
	a.unblock()
	waitEarlyTest(t, r.done)
	r.assertSelection(N.NetworkTCP, a)
}

func TestURLTestEarlySelectionFallbackThreshold(t *testing.T) {
	for _, tc := range []struct {
		name  string
		b, c  uint16
		wantC bool
	}{
		{"first-within-limit", 90, 10, false},
		{"later-within-limit", 150, 80, true},
		{"all-over-limit", 150, 120, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b, c := newEarlyTestOutbound(t, "a"), newEarlyTestOutbound(t, "b"), newEarlyTestOutbound(t, "c")
			r := newEarlyTestRound(t, a, b, c)
			a.fail = true
			r.group.fallback = option.URLTestFallbackOptions{Enabled: true, MaxDelay: badoption.Duration(100 * time.Millisecond)}
			storeHistory(r.history, b.Tag(), r.group.link, tc.b)
			storeHistory(r.history, c.Tag(), r.group.link, tc.c)
			want := b
			if tc.wantC {
				want = c
			}
			r.start(false)
			waitEarlyTest(t, a.started)
			r.assertSelection(N.NetworkTCP, want)
			r.assertRunning()
			a.unblock()
			waitEarlyTest(t, r.done)
			r.assertSelection(N.NetworkTCP, want)
		})
	}
}

func TestURLTestFreshHistoryCallsObserver(t *testing.T) {
	node := newEarlyTestOutbound(t, "cached")
	r := newEarlyTestRound(t, node)
	storeHistory(r.history, node.Tag(), r.group.link, 10)
	observed := false
	result := urlTestOutbounds(context.Background(), r.manager, r.history, r.group.logger,
		[]adapter.Outbound{node}, r.group.link, time.Hour, false, func() { observed = true })
	if !observed || result[node.Tag()] != 10 {
		t.Fatal("fresh result was not delivered to the history observer")
	}
	select {
	case <-node.started:
		t.Fatal("fresh history unexpectedly started a probe")
	default:
	}
}

type earlyTestCloser struct {
	closed chan struct{}
	once   sync.Once
}

func (c *earlyTestCloser) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func assertEarlyTestClosed(t *testing.T, signal <-chan struct{}, want bool) {
	t.Helper()
	select {
	case <-signal:
		if !want {
			t.Fatal("connection was unexpectedly interrupted")
		}
	default:
		if want {
			t.Fatal("connection was not interrupted")
		}
	}
}

// Plan test 8.
func TestURLTestEarlySelectionInterrupts(t *testing.T) {
	for _, cached := range []bool{false, true} {
		for _, external := range []bool{false, true} {
			name := map[bool]string{false: "nil", true: "cached"}[cached] + "/" + map[bool]string{false: "internal-only", true: "external"}[external]
			t.Run(name, func(t *testing.T) {
				a, b := newEarlyTestOutbound(t, "a"), newEarlyTestOutbound(t, "b")
				r := newEarlyTestRound(t, a, b)
				a.fail = true
				r.group.interruptExternalConnections = external
				if !cached {
					r.group.selectedOutboundTCP, r.group.selectedOutboundUDP = nil, nil
				}
				internalConn := &earlyTestCloser{closed: make(chan struct{})}
				externalConn := &earlyTestCloser{closed: make(chan struct{})}
				r.group.interruptGroup.Add(internalConn, false)
				r.group.interruptGroup.Add(externalConn, true)
				b.unblock()
				r.start(true)
				r.waitSelection(N.NetworkTCP, b)
				assertEarlyTestClosed(t, internalConn.closed, cached)
				assertEarlyTestClosed(t, externalConn.closed, cached && external)
				r.assertRunning()
				a.unblock()
				waitEarlyTest(t, r.done)
			})
		}
	}
}

// Plan test 9. Inner probes never satisfy outer-target selection of plain leaves.
func TestURLTestNestedEarlySelectionKeepsTargetsIsolated(t *testing.T) {
	a, b, c := newEarlyTestOutbound(t, "a"), newEarlyTestOutbound(t, "b"), newEarlyTestOutbound(t, "c")
	innerRound := newEarlyTestRound(t, a, b)
	outerRound := newEarlyTestRound(t, c)
	innerRound.history = outerRound.history
	innerRound.group.history = outerRound.history
	innerRound.group.outbound = outerRound.manager
	outerRound.manager.nodes[a.Tag()], outerRound.manager.nodes[b.Tag()] = a, b
	inner := &URLTest{Adapter: outbound.NewAdapter("urltest", "inner", []string{N.NetworkTCP, N.NetworkUDP}, nil), link: innerRound.group.link, group: innerRound.group}
	outerRound.manager.nodes[inner.Tag()] = inner
	outerRound.group.outbounds = []adapter.Outbound{c, inner, b}
	a.fail, c.fail = true, true
	// Outer-target B is already known but cannot be reused at the inner target.
	storeHistory(outerRound.history, b.Tag(), outerRound.group.link, 60000)
	outerRound.start(false)
	waitEarlyTest(t, a.started)
	waitEarlyTest(t, b.started)
	innerRound.assertSelection(N.NetworkTCP, a)
	b.unblock()
	for innerRound.group.selected(N.NetworkTCP) != b {
		waitEarlyTest(t, outerRound.updates)
	}
	if outerRound.history.LoadURLTestHistory(b.Tag(), innerRound.group.link) == nil {
		t.Fatal("inner selection did not use its own target")
	}
	storeHistory(outerRound.history, b.Tag(), innerRound.group.link, 10)
	outerRound.assertSelection(N.NetworkTCP, b)
	outerRound.assertRunning()
	a.unblock()
	c.unblock()
	waitEarlyTest(t, outerRound.done)
	outerRound.assertSelection(N.NetworkTCP, inner)
}
