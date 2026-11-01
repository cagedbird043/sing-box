package group

import (
	"context"
	"errors"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type recheckProbe struct {
	release chan struct{}
	fail    bool
}

type recheckOutbound struct {
	adapter.Outbound
	tag     string
	calls   atomic.Int32
	started chan *recheckProbe
	probes  []*recheckProbe
}

func newRecheckOutbound(t *testing.T, tag string, failures ...bool) *recheckOutbound {
	t.Helper()
	o := &recheckOutbound{tag: tag, started: make(chan *recheckProbe, 16)}
	for _, fail := range failures {
		p := &recheckProbe{release: make(chan struct{}), fail: fail}
		o.probes = append(o.probes, p)
		t.Cleanup(func() {
			select {
			case <-p.release:
			default:
				close(p.release)
			}
		})
	}
	return o
}

func (o *recheckOutbound) Tag() string       { return o.tag }
func (o *recheckOutbound) Network() []string { return []string{N.NetworkTCP, N.NetworkUDP} }
func (o *recheckOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	i := int(o.calls.Add(1)) - 1
	if i >= len(o.probes) {
		return nil, errors.New("unexpected additional probe")
	}
	p := o.probes[i]
	o.started <- p
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if p.fail {
		return nil, errors.New("controlled probe failure")
	}
	return (&net.Dialer{}).DialContext(ctx, network, destination.String())
}

func nextRecheckProbe(t *testing.T, o *recheckOutbound) *recheckProbe {
	t.Helper()
	select {
	case p := <-o.started:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("probe did not start")
		return nil
	}
}

type recheckManager struct {
	adapter.OutboundManager
	access sync.RWMutex
	nodes  map[string]adapter.Outbound
}

func (m *recheckManager) Outbound(tag string) (adapter.Outbound, bool) {
	m.access.RLock()
	defer m.access.RUnlock()
	node, ok := m.nodes[tag]
	return node, ok
}

func (m *recheckManager) replace(node adapter.Outbound) {
	m.access.Lock()
	m.nodes[node.Tag()] = node
	m.access.Unlock()
}

func newRecheckGroup(t *testing.T, nodes ...adapter.Outbound) (*URLTest, *earlyTestRound, *recheckManager) {
	t.Helper()
	r := newEarlyTestRound(t, nodes...)
	m := &recheckManager{nodes: r.manager.nodes}
	r.group.outbound = m
	ctx, cancel := context.WithCancel(context.Background())
	r.group.ctx = ctx
	r.group.close = make(chan struct{})
	t.Cleanup(func() { cancel(); waitRecheckIdle(t, r.group) })
	s := &URLTest{Adapter: outbound.NewAdapter("urltest", "test", nil, nil), outbound: m, group: r.group, link: r.group.link}
	return s, r, m
}

func waitRecheckIdle(t *testing.T, g *URLTestGroup) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		g.recheckAccess.Lock()
		running := g.recheckRunning
		g.recheckAccess.Unlock()
		if !running {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("recheck did not finish")
		default:
			runtime.Gosched()
		}
	}
}

func TestURLTestRecheckDialFailureSwitches(t *testing.T) {
	a, b := newRecheckOutbound(t, "a", true, true), newRecheckOutbound(t, "b", false)
	s, r, _ := newRecheckGroup(t, a, b)
	storeHistory(r.history, a.Tag(), r.group.link, 10)
	s.InvalidateURLTestHistory(a, N.NetworkTCP)
	close(nextRecheckProbe(t, a).release)
	close(nextRecheckProbe(t, a).release)
	close(nextRecheckProbe(t, b).release)
	waitRecheckIdle(t, r.group)
	r.assertSelection(N.NetworkTCP, b)
	r.assertSelection(N.NetworkUDP, b)
}

func TestURLTestRecheckConfirmationSuccess(t *testing.T) {
	a, b := newRecheckOutbound(t, "a", false), newRecheckOutbound(t, "b", false)
	s, r, _ := newRecheckGroup(t, a, b)
	storeHistory(r.history, a.Tag(), r.group.link, 100)
	before := r.history.LoadURLTestHistory(a.Tag(), r.group.link)
	s.RequestRecheck(a, N.NetworkTCP)
	if r.history.LoadURLTestHistory(a.Tag(), r.group.link) != before {
		t.Fatal("request invalidated history before confirmation")
	}
	close(nextRecheckProbe(t, a).release)
	waitRecheckIdle(t, r.group)
	if after := r.history.LoadURLTestHistory(a.Tag(), r.group.link); after == nil || after == before {
		t.Fatal("confirmation did not refresh history")
	}
	if a.calls.Load() != 1 || b.calls.Load() != 0 {
		t.Fatal("successful confirmation started a full round")
	}
	r.assertSelection(N.NetworkTCP, a)
}

func TestURLTestRecheckAllFailPreservesSelection(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "nested"}[nested], func(t *testing.T) {
			a, b := newRecheckOutbound(t, "a", true, true), newRecheckOutbound(t, "b", true)
			s, r, m := newRecheckGroup(t, a, b)
			inner := s
			if nested {
				inner.Adapter = outbound.NewAdapter("urltest", "inner", nil, nil)
				outer := &URLTestGroup{ctx: r.group.ctx, outbound: m, history: r.history, logger: r.group.logger,
					outbounds: []adapter.Outbound{inner}, link: "http://unused.invalid", interval: time.Hour,
					selectedOutboundTCP: inner, selectedOutboundUDP: inner, interruptGroup: interrupt.NewGroup()}
				s = &URLTest{outbound: m, group: outer}
				m.replace(inner)
			}
			storeHistory(r.history, a.Tag(), inner.link, 10)
			storeHistory(r.history, b.Tag(), inner.link, 1)
			innerCloser := &earlyTestCloser{closed: make(chan struct{})}
			r.group.interruptGroup.Add(innerCloser, false)
			outerCloser := &earlyTestCloser{closed: make(chan struct{})}
			s.group.interruptGroup.Add(outerCloser, false)
			s.InvalidateURLTestHistory(a, N.NetworkTCP)
			close(nextRecheckProbe(t, a).release)
			close(nextRecheckProbe(t, a).release)
			close(nextRecheckProbe(t, b).release)
			waitRecheckIdle(t, s.group)
			r.assertSelection(N.NetworkTCP, a)
			if nested && s.Selected(N.NetworkTCP) != inner {
				t.Fatal("outer selection changed")
			}
			for _, closed := range []<-chan struct{}{innerCloser.closed, outerCloser.closed} {
				select {
				case <-closed:
					t.Fatal("all-failure round interrupted a connection")
				default:
				}
			}
		})
	}
}

func TestURLTestRecheckSingleFlightAndCooldown(t *testing.T) {
	a := newRecheckOutbound(t, "a", false, false)
	s, r, _ := newRecheckGroup(t, a)
	now := time.Unix(1, 0)
	r.group.recheckNow = func() time.Time { return now }
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() { defer wg.Done(); s.RequestRecheck(a, N.NetworkTCP) }()
	}
	wg.Wait()
	if a.calls.Load() > 1 {
		t.Fatal("concurrent requests started multiple confirmations")
	}
	close(nextRecheckProbe(t, a).release)
	waitRecheckIdle(t, r.group)
	now = now.Add(recheckCooldown - time.Nanosecond)
	s.RequestRecheck(a, N.NetworkUDP)
	if a.calls.Load() != 1 {
		t.Fatal("request inside cooldown probed again")
	}
	now = now.Add(time.Nanosecond)
	s.RequestRecheck(a, N.NetworkUDP)
	close(nextRecheckProbe(t, a).release)
	waitRecheckIdle(t, r.group)
	if a.calls.Load() != 2 {
		t.Fatal("request at cooldown boundary was not accepted")
	}
}

func TestURLTestRecheckWaitsForBusyRound(t *testing.T) {
	a := newRecheckOutbound(t, "a", true, true, true)
	b := newRecheckOutbound(t, "b", false, false)
	s, r, _ := newRecheckGroup(t, a, b)
	storeHistory(r.history, a.Tag(), r.group.link, 10)
	r.start(true)
	oldA, oldB := nextRecheckProbe(t, a), nextRecheckProbe(t, b)
	s.RequestRecheck(a, N.NetworkTCP)
	close(nextRecheckProbe(t, a).release)
	close(oldA.release)
	close(oldB.release)
	waitEarlyTest(t, r.done)
	close(nextRecheckProbe(t, a).release)
	close(nextRecheckProbe(t, b).release)
	waitRecheckIdle(t, r.group)
	r.assertSelection(N.NetworkTCP, b)
	if a.calls.Load() != 3 || b.calls.Load() != 2 {
		t.Fatal("busy round replaced the requested forced round")
	}
}

// Hold the same state as a round that completed selection but has not yet
// released testAccess. The failed confirmation must not be lost in this window.
func TestURLTestRecheckAfterBusySelection(t *testing.T) {
	a, b := newRecheckOutbound(t, "a", true, true), newRecheckOutbound(t, "b", false)
	s, r, _ := newRecheckGroup(t, a, b)
	storeHistory(r.history, a.Tag(), r.group.link, 10)
	r.group.testAccess.Lock()
	r.group.checking.Store(true)
	s.RequestRecheck(a, N.NetworkTCP)
	close(nextRecheckProbe(t, a).release)
	for r.history.LoadURLTestHistory(a.Tag(), r.group.link) != nil {
		waitEarlyTest(t, r.updates)
	}
	r.group.checking.Store(false)
	r.group.testAccess.Unlock()
	close(nextRecheckProbe(t, a).release)
	close(nextRecheckProbe(t, b).release)
	waitRecheckIdle(t, r.group)
	r.assertSelection(N.NetworkTCP, b)
}

func TestURLTestRecheckRejectsObsoleteLeaf(t *testing.T) {
	a, b := newRecheckOutbound(t, "a", false), newRecheckOutbound(t, "b", false)
	s, r, m := newRecheckGroup(t, a, b)
	s.RequestRecheck(b, N.NetworkTCP)
	m.replace(newRecheckOutbound(t, "a", false))
	s.RequestRecheck(a, N.NetworkTCP)
	if !r.group.recheckLast.IsZero() || a.calls.Load() != 0 || b.calls.Load() != 0 {
		t.Fatal("obsolete or unselected leaf requested a confirmation")
	}
}

func TestURLTestRecheckReplacementDuringConfirmation(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			a := newRecheckOutbound(t, "a", fail)
			s, r, m := newRecheckGroup(t, a)
			s.RequestRecheck(a, N.NetworkTCP)
			p := nextRecheckProbe(t, a)
			m.replace(newRecheckOutbound(t, "a", false))
			r.history.DeleteOutboundURLTestHistory(a.Tag())
			storeHistory(r.history, a.Tag(), r.group.link, 123)
			before := r.history.LoadURLTestHistory(a.Tag(), r.group.link)
			close(p.release)
			waitRecheckIdle(t, r.group)
			if r.history.LoadURLTestHistory(a.Tag(), r.group.link) != before || a.calls.Load() != 1 {
				t.Fatal("obsolete confirmation affected replacement history or ran a round")
			}
		})
	}
}

func TestURLTestRecheckClose(t *testing.T) {
	a := newRecheckOutbound(t, "a", true)
	s, r, _ := newRecheckGroup(t, a)
	ctx, cancel := context.WithCancel(context.Background())
	r.group.ctx = ctx
	defer cancel()
	s.RequestRecheck(a, N.NetworkTCP)
	nextRecheckProbe(t, a)
	cancel()
	if err := r.group.Close(); err != nil {
		t.Fatal(err)
	}
	waitRecheckIdle(t, r.group)
	s.RequestRecheck(a, N.NetworkTCP)
	if a.calls.Load() != 1 {
		t.Fatal("canceled group accepted another confirmation")
	}
}

func TestURLTestRecheckUsesNestedTarget(t *testing.T) {
	a := newRecheckOutbound(t, "a", false)
	inner, r, m := newRecheckGroup(t, a)
	outer := &URLTestGroup{ctx: r.group.ctx, outbound: m, history: r.history, logger: r.group.logger,
		outbounds: []adapter.Outbound{inner}, link: "http://unused.invalid", interval: time.Hour,
		selectedOutboundTCP: inner, interruptGroup: interrupt.NewGroup()}
	outer.requestRecheck(a, N.NetworkTCP)
	close(nextRecheckProbe(t, a).release)
	waitRecheckIdle(t, outer)
	if r.history.LoadURLTestHistory(a.Tag(), inner.link) == nil {
		t.Fatal("confirmation did not use the nested group's target")
	}
	if r.history.LoadURLTestHistory(a.Tag(), outer.link) != nil || a.calls.Load() != 1 {
		t.Fatal("confirmation probed the outer target or started a round")
	}
}

func TestURLTestRecheckRejectsObsoleteGeneration(t *testing.T) {
	a := newRecheckOutbound(t, "a", false)
	s, r, _ := newRecheckGroup(t, a)
	s.RequestRecheck(a, N.NetworkTCP)
	p := nextRecheckProbe(t, a)
	r.history.DeleteOutboundURLTestHistory(a.Tag())
	storeHistory(r.history, a.Tag(), r.group.link, 123)
	before := r.history.LoadURLTestHistory(a.Tag(), r.group.link)
	close(p.release)
	waitRecheckIdle(t, r.group)
	if r.history.LoadURLTestHistory(a.Tag(), r.group.link) != before {
		t.Fatal("obsolete confirmation changed the new generation's history")
	}
}
