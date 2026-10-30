package group

import (
	"context"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
)

const recheckCooldown = 10 * time.Second

// RequestRecheck confirms a selected leaf's failure without invalidating its history.
func (s *URLTest) RequestRecheck(leaf adapter.Outbound, network string) {
	s.group.requestRecheck(leaf, network)
}

func (g *URLTestGroup) requestRecheck(leaf adapter.Outbound, network string) {
	if leaf == nil {
		return
	}
	generation := g.history.OutboundGeneration(leaf.Tag())
	if current, loaded := g.outbound.Outbound(leaf.Tag()); !loaded || current != leaf {
		return
	}
	selected, link := historyTarget(g.selected(network), network, g.link)
	if selected != leaf {
		return
	}
	g.recheckAccess.Lock()
	if g.ctx.Err() != nil || g.recheckRunning {
		g.recheckAccess.Unlock()
		return
	}
	nowFunc := g.recheckNow
	if nowFunc == nil {
		nowFunc = time.Now
	}
	now := nowFunc()
	if !g.recheckLast.IsZero() && now.Sub(g.recheckLast) < recheckCooldown {
		g.recheckAccess.Unlock()
		return
	}
	g.recheckRunning = true
	g.recheckLast = now
	g.recheckAccess.Unlock()
	go func() {
		defer func() {
			g.recheckAccess.Lock()
			g.recheckRunning = false
			g.recheckAccess.Unlock()
		}()
		g.recheck(leaf, generation, link)
	}()
}

func (g *URLTestGroup) recheck(leaf adapter.Outbound, generation uint64, link string) {
	if current, loaded := g.outbound.Outbound(leaf.Tag()); !loaded || current != leaf {
		return
	}
	ctx, cancel := context.WithTimeout(g.ctx, C.TCPTimeout)
	delay, err := urltest.URLTest(ctx, link, leaf)
	cancel()
	if current, loaded := g.outbound.Outbound(leaf.Tag()); !loaded || current != leaf {
		return
	}
	if err == nil {
		g.history.StoreURLTestHistory(leaf.Tag(), link, generation, &adapter.URLTestHistory{
			Time: time.Now(), Delay: delay,
		})
		return
	}
	if g.ctx.Err() != nil {
		return
	}
	g.history.DeleteURLTestHistory(leaf.Tag(), link, generation)
	_, ran, _ := g.tryURLTest(g.ctx, true, true)
	if !ran && g.ctx.Err() == nil {
		g.testAccess.Lock()
		g.testAccess.Unlock()
		_, _, _ = g.tryURLTest(g.ctx, true, true)
	}
}
