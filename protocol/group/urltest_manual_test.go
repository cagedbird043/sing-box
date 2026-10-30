package group

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/log"
	N "github.com/sagernet/sing/common/network"
)

type manualTestManager struct {
	historyTestManager
	order []adapter.Outbound
}

func (m *manualTestManager) Outbounds() []adapter.Outbound { return m.order }

func newManualTestTarget(t *testing.T) (string, *atomic.Int32) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	return server.URL, &hits
}

func TestURLTestMembersProbesSelectorAndOwningGroupTargets(t *testing.T) {
	firstURL, firstHits := newManualTestTarget(t)
	secondURL, secondHits := newManualTestTarget(t)
	selectorURL, selectorHits := newManualTestTarget(t)
	history := urltest.NewHistoryStorage()
	node, other := &historyTestOutbound{tag: "node"}, &historyTestOutbound{tag: "other"}
	manager := &manualTestManager{historyTestManager: historyTestManager{nodes: map[string]adapter.Outbound{}}}
	newGroup := func(tag, link string, members ...adapter.Outbound) *URLTest {
		return &URLTest{
			Adapter: outbound.NewAdapter("urltest", tag, nil, nil),
			link:    link,
			group: &URLTestGroup{
				ctx: context.Background(), outbound: manager, history: history, link: link,
				outbounds: members, tolerance: 50, interruptGroup: interrupt.NewGroup(),
			},
		}
	}
	first := newGroup("first", firstURL, other, node)
	second := newGroup("second", secondURL, node)
	selector := &Selector{Adapter: outbound.NewAdapter("selector", "selector", nil, nil), tags: []string{"node"}, link: selectorURL, history: history}
	selector.selected.Store(node)
	for _, it := range []adapter.Outbound{node, other, first, second, selector} {
		manager.nodes[it.Tag()] = it
		manager.order = append(manager.order, it)
	}
	storeHistory(history, other.Tag(), firstURL, 100)
	storeHistory(history, node.Tag(), firstURL, 60000)
	first.PerformUpdateCheck()
	if first.Selected(N.NetworkTCP) != other {
		t.Fatal("setup: first group did not select the faster member")
	}

	URLTestMembers(context.Background(), manager, history, log.NewNOPFactory().Logger(), []adapter.Outbound{selector})

	if firstHits.Load() != 1 || secondHits.Load() != 1 || selectorHits.Load() != 1 {
		t.Fatalf("node was not probed once per target: %d/%d/%d", firstHits.Load(), secondHits.Load(), selectorHits.Load())
	}
	if history.LoadURLTestHistory(node.Tag(), urltest.DefaultURL) != nil {
		t.Fatal("a selector with its own url was probed at the default target")
	}
	if first.Selected(N.NetworkTCP) != node {
		t.Fatal("owning group did not re-evaluate its selection after the manual test")
	}
	shown := selector.URLTestHistory(node, N.NetworkTCP)
	if shown == nil || shown != history.LoadURLTestHistory(node.Tag(), selectorURL) {
		t.Fatal("selector did not display its own target")
	}
	storeHistory(history, node.Tag(), secondURL, 1)
	if selector.URLTestHistory(node, N.NetworkTCP) != shown {
		t.Fatal("a newer probe at another group's target replaced the selector's display")
	}
}

func TestURLTestMembersDefaultsToGlobalTarget(t *testing.T) {
	history := urltest.NewHistoryStorage()
	node := &historyTestOutbound{tag: "node"}
	selector := &Selector{Adapter: outbound.NewAdapter("selector", "selector", nil, nil), tags: []string{"node"}, link: urltest.NormalizeURL(""), history: history}
	storeHistory(history, node.Tag(), urltest.DefaultURL, 120)
	storeHistory(history, node.Tag(), "https://other.example/check", 1)
	selector.selected.Store(node)
	if got := selector.URLTestHistory(node, N.NetworkTCP); got == nil || got.Delay != 120 {
		t.Fatal("selector without url did not display the default target")
	}
	if got := LoadURLTestHistory(history, node, N.NetworkTCP); got == nil || got.Delay != 120 {
		t.Fatal("plain node did not display the default target")
	}
}

func TestURLTestGroupsOfNestedGroupOwnsItsMembers(t *testing.T) {
	node := &historyTestOutbound{tag: "node"}
	manager := &manualTestManager{historyTestManager: historyTestManager{nodes: map[string]adapter.Outbound{"node": node}}}
	inner := &URLTest{Adapter: outbound.NewAdapter("urltest", "inner", nil, nil), link: "https://inner.example/check",
		group: &URLTestGroup{outbounds: []adapter.Outbound{node}}}
	outer := &URLTest{Adapter: outbound.NewAdapter("urltest", "outer", nil, nil), link: "https://outer.example/check",
		group: &URLTestGroup{outbounds: []adapter.Outbound{inner}}}
	manager.order = []adapter.Outbound{node, inner, outer}
	if groups := urlTestGroupsOf(manager, node); len(groups) != 1 || groups[0] != inner {
		t.Fatalf("nested member was attributed to the outer group: %v", groups)
	}
}
