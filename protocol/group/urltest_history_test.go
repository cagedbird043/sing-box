package group

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type historyTestOutbound struct {
	adapter.Outbound
	tag string
}

func (o *historyTestOutbound) Tag() string       { return o.tag }
func (o *historyTestOutbound) Network() []string { return []string{N.NetworkTCP, N.NetworkUDP} }
func (o *historyTestOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, destination.String())
}

type historyTestManager struct {
	adapter.OutboundManager
	nodes map[string]adapter.Outbound
}

func (m *historyTestManager) Outbound(tag string) (adapter.Outbound, bool) {
	outbound, ok := m.nodes[tag]
	return outbound, ok
}

func storeHistory(history *urltest.HistoryStorage, tag, link string, delay uint16) {
	history.StoreURLTestHistory(tag, link, history.OutboundGeneration(tag), &adapter.URLTestHistory{Time: time.Now(), Delay: delay})
}

func TestURLTestSelectionIsTargetScoped(t *testing.T) {
	history := urltest.NewHistoryStorage()
	a, b := &historyTestOutbound{tag: "a"}, &historyTestOutbound{tag: "b"}
	const first, second = "https://first.example/check", "https://second.example/check"
	storeHistory(history, "a", first, 10)
	storeHistory(history, "b", first, 300)
	storeHistory(history, "a", second, 300)
	storeHistory(history, "b", second, 10)
	groups := []*URLTestGroup{
		{history: history, link: first, outbounds: []adapter.Outbound{a, b}},
		{history: history, link: second, outbounds: []adapter.Outbound{a, b}},
	}
	for i, want := range []adapter.Outbound{a, b} {
		for _, network := range []string{N.NetworkTCP, N.NetworkUDP} {
			selected, available := groups[i].Select(network)
			if !available || selected != want {
				t.Fatalf("group %d network %s selected %v, available=%v; want %s", i, network, selected, available, want.Tag())
			}
		}
	}
	history.DeleteURLTestHistory("a", second, history.OutboundGeneration("a"))
	if selected, available := groups[0].Select(N.NetworkTCP); !available || selected != a {
		t.Fatal("a failure at the second target changed the first target's winner")
	}
}

func TestURLTestDoesNotUseDisplayHistoryAsFallback(t *testing.T) {
	history := urltest.NewHistoryStorage()
	a, b := &historyTestOutbound{tag: "a"}, &historyTestOutbound{tag: "b"}
	storeHistory(history, "b", "https://other.example/check", 1)
	group := &URLTestGroup{history: history, link: "https://unmeasured.example/check", outbounds: []adapter.Outbound{a, b}}
	selected, available := group.Select(N.NetworkTCP)
	if available || selected != a {
		t.Fatal("an unmeasured group used another target's display history")
	}
}

func TestURLTestNestedTargetAndDisplay(t *testing.T) {
	history := urltest.NewHistoryStorage()
	a := &historyTestOutbound{tag: "a"}
	const innerURL, outerURL = "https://inner.example/check", "https://outer.example/check"
	storeHistory(history, "a", innerURL, 10)
	storeHistory(history, "a", outerURL, 250)
	inner := &URLTest{link: innerURL, group: &URLTestGroup{history: history, link: innerURL, outbounds: []adapter.Outbound{a}, selectedOutboundTCP: a}}
	outer := &URLTestGroup{history: history, link: outerURL, outbounds: []adapter.Outbound{inner}}
	if got := outer.loadHistory(inner, N.NetworkTCP); got == nil || got.Delay != 10 {
		t.Fatal("outer group did not use the nested URL-test group's effective target")
	}
	if got := LoadURLTestHistory(history, inner, N.NetworkTCP); got == nil || got.Delay != 10 {
		t.Fatal("URL-test display used the latest unrelated target")
	}
	if got := LoadURLTestHistory(history, a, N.NetworkTCP); got == nil || got.Delay != 250 {
		t.Fatal("plain node display did not retain its latest successful probe")
	}
}

func TestURLTestCacheAndFailureAreTargetScoped(t *testing.T) {
	var firstHits, secondHits atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	history := urltest.NewHistoryStorage()
	node := &historyTestOutbound{tag: "node"}
	manager := &historyTestManager{nodes: map[string]adapter.Outbound{"node": node}}
	logger := log.NewNOPFactory().Logger()
	probe := func(link string, force bool) map[string]uint16 {
		return URLTestOutbounds(context.Background(), manager, history, logger, []adapter.Outbound{node}, link, time.Hour, force)
	}
	probe(first.URL, false)
	probe(second.URL, false)
	if firstHits.Load() != 1 || secondHits.Load() != 1 {
		t.Fatalf("a fresh result at the first target skipped the second probe: %d/%d", firstHits.Load(), secondHits.Load())
	}
	cached := probe(second.URL, false)
	if _, exists := cached[node.Tag()]; !exists || secondHits.Load() != 1 {
		t.Fatal("same-target fresh result was not reused")
	}
	second.Close()
	probe(second.URL, true)
	if history.LoadURLTestHistory(node.Tag(), second.URL) != nil {
		t.Fatal("failed target retained a successful history")
	}
	if history.LoadURLTestHistory(node.Tag(), first.URL) == nil {
		t.Fatal("failed second target erased the first target's success")
	}
}

func TestURLTestInvalidationChecksTargetAndLeafIdentity(t *testing.T) {
	history := urltest.NewHistoryStorage()
	node := &historyTestOutbound{tag: "node"}
	manager := &historyTestManager{nodes: map[string]adapter.Outbound{"node": node}}
	const first, second = "https://first.example/check", "https://second.example/check"
	group := &URLTest{outbound: manager, group: &URLTestGroup{history: history, link: first, selectedOutboundTCP: node, selectedOutboundUDP: node}}
	storeHistory(history, node.Tag(), first, 100)
	storeHistory(history, node.Tag(), second, 200)
	group.InvalidateURLTestHistory(node, N.NetworkUDP)
	if history.LoadURLTestHistory(node.Tag(), first) != nil || history.LoadURLTestHistory(node.Tag(), second) == nil {
		t.Fatal("UDP failure invalidated unrelated target history")
	}
	replacement := &historyTestOutbound{tag: node.Tag()}
	manager.nodes[node.Tag()] = replacement
	history.DeleteOutboundURLTestHistory(node.Tag())
	storeHistory(history, node.Tag(), first, 10)
	group.InvalidateURLTestHistory(node, N.NetworkTCP)
	if history.LoadURLTestHistory(node.Tag(), first) == nil {
		t.Fatal("an obsolete leaf invalidated the replacement node's success")
	}
}
