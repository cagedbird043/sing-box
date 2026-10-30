package group

import (
	"context"
	"io"
	"maps"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/batch"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/x/list"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
)

func RegisterURLTest(registry *outbound.Registry) {
	outbound.Register[option.URLTestOutboundOptions](registry, C.TypeURLTest, NewURLTest)
}

var (
	_ adapter.OutboundGroup           = (*URLTest)(nil)
	_ adapter.InterfaceUpdateListener = (*URLTest)(nil)
	_ adapter.Referrer                = (*URLTest)(nil)
)

type URLTest struct {
	outbound.Adapter
	ctx                          context.Context
	outbound                     adapter.OutboundManager
	logger                       log.ContextLogger
	tags                         []string
	link                         string
	interval                     time.Duration
	tolerance                    uint16
	idleTimeout                  time.Duration
	group                        *URLTestGroup
	checkAccess                  sync.Mutex
	interruptExternalConnections bool
	members                      *providerMembers
	membersAccess                sync.RWMutex
	fallback                     option.URLTestFallbackOptions
}

func NewURLTest(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.URLTestOutboundOptions) (adapter.Outbound, error) {
	outbound := &URLTest{
		Adapter:                      outbound.NewAdapter(C.TypeURLTest, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.Outbounds),
		ctx:                          ctx,
		outbound:                     service.FromContext[adapter.OutboundManager](ctx),
		logger:                       logger,
		tags:                         options.Outbounds,
		link:                         urltest.NormalizeURL(options.URL),
		interval:                     time.Duration(options.Interval),
		tolerance:                    options.Tolerance,
		idleTimeout:                  time.Duration(options.IdleTimeout),
		interruptExternalConnections: options.InterruptExistConnections,
		members:                      newProviderMembers(ctx, options.GroupCommonOption),
		fallback:                     options.Fallback,
	}
	if len(outbound.tags) == 0 && len(options.Providers) == 0 && !options.UseAllProviders {
		return nil, E.New("missing tags")
	}
	for i, tag := range outbound.tags {
		if slices.Contains(outbound.tags[:i], tag) {
			return nil, E.New("duplicate tag: ", tag)
		}
	}
	return outbound, nil
}

func (s *URLTest) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	switch stage {
	case adapter.StartStateStart:
		outbounds, err := s.members.start(scope, s.updateMembers)
		if err != nil {
			return err
		}
		group, err := NewURLTestGroup(scope.Context(), s.outbound, s.logger, outbounds, s.link, s.interval, s.tolerance, s.idleTimeout, s.interruptExternalConnections)
		if err != nil {
			return err
		}
		s.group = group
		scope.Add(group.Close)
		group.fallback = s.fallback
		s.setMemberTags(outbounds)
	case adapter.StartStateStarted:
		s.group.PostStart()
	}
	return nil
}

func (s *URLTest) All() []string {
	s.membersAccess.RLock()
	defer s.membersAccess.RUnlock()
	return slices.Clone(s.tags)
}

func (s *URLTest) Selected(network string) adapter.Outbound {
	return s.group.selected(network)
}

func (s *URLTest) AttachConnection(closer io.Closer) func() {
	s.group.Touch()
	return s.group.interruptGroup.Add(closer, true)
}

func (s *URLTest) References() []string {
	group := s.group
	if group == nil {
		return nil
	}
	group.nodesAccess.RLock()
	defer group.nodesAccess.RUnlock()
	var references []string
	if group.selectedOutboundTCP != nil {
		references = append(references, group.selectedOutboundTCP.Tag())
	}
	if group.selectedOutboundUDP != nil && group.selectedOutboundUDP != group.selectedOutboundTCP {
		references = append(references, group.selectedOutboundUDP.Tag())
	}
	return references
}

func (s *URLTest) URLTest(ctx context.Context) (map[string]uint16, error) {
	return s.group.URLTest(ctx)
}

// URLTestHistory returns this group's member history, respecting nested URL-test targets.
func (s *URLTest) URLTestHistory(detour adapter.Outbound, network string) *adapter.URLTestHistory {
	return s.group.loadHistory(detour, network)
}

// LoadURLTestHistory is a display view: selected URL-test groups use their own target,
// while plain outbounds use their latest successful probe, regardless of target.
func LoadURLTestHistory(history *urltest.HistoryStorage, detour adapter.Outbound, network string) *adapter.URLTestHistory {
	for detour != nil {
		if test, ok := detour.(*URLTest); ok {
			return test.URLTestHistory(test.Selected(network), network)
		}
		if group, ok := detour.(adapter.OutboundGroup); ok {
			detour = group.Selected(network)
		} else {
			return history.LoadLatestURLTestHistory(detour.Tag())
		}
	}
	return nil
}

func historyTarget(detour adapter.Outbound, network, link string) (adapter.Outbound, string) {
	for detour != nil {
		if test, ok := detour.(*URLTest); ok {
			link = test.link
		}
		if group, ok := detour.(adapter.OutboundGroup); ok {
			detour = group.Selected(network)
		} else {
			return detour, link
		}
	}
	return nil, link
}

func (g *URLTestGroup) loadHistory(detour adapter.Outbound, network string) *adapter.URLTestHistory {
	leaf, link := historyTarget(detour, network, g.link)
	if leaf == nil {
		return nil
	}
	return g.history.LoadURLTestHistory(leaf.Tag(), link)
}

// InvalidateURLTestHistory ignores failures from an obsolete selected leaf.
func (s *URLTest) InvalidateURLTestHistory(leaf adapter.Outbound, network string) {
	if leaf == nil {
		return
	}
	generation := s.group.history.OutboundGeneration(leaf.Tag())
	current, loaded := s.outbound.Outbound(leaf.Tag())
	if !loaded || current != leaf {
		return
	}
	selected, link := historyTarget(s.Selected(network), network, s.group.link)
	if selected != leaf {
		return
	}
	s.group.history.DeleteURLTestHistory(leaf.Tag(), link, generation)
}

func (s *URLTest) CheckOutbounds() {
	s.group.CheckOutbounds(s.ctx, true)
}

func (s *URLTest) PerformUpdateCheck() {
	s.group.performUpdateCheck()
}

func (s *URLTest) InterfaceUpdated(ctx context.Context) {
	group := s.group
	if group == nil {
		return
	}
	if group.pause.IsDevicePaused() || group.pause.IsNetworkPaused() {
		return
	}
	go func() {
		s.checkAccess.Lock()
		defer s.checkAccess.Unlock()
		if ctx.Err() != nil {
			return
		}
		group.CheckOutbounds(ctx, true)
	}()
}

func (s *URLTest) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	s.group.Touch()
	network = N.NetworkName(network)
	if network != N.NetworkTCP && network != N.NetworkUDP {
		return nil, E.Extend(N.ErrUnknownNetwork, network)
	}
	outbound := s.group.selected(network)
	if outbound == nil {
		return nil, E.New("missing supported outbound")
	}
	leaf, _ := historyTarget(outbound, network, s.group.link)
	conn, err := outbound.DialContext(ctx, network, destination)
	if err == nil {
		return s.group.interruptGroup.NewConn(conn, interrupt.IsExternalConnectionFromContext(ctx), interrupt.IsProviderConnectionFromContext(ctx)), nil
	}
	s.logger.ErrorContext(ctx, err)
	s.InvalidateURLTestHistory(leaf, network)
	return nil, err
}

func (s *URLTest) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	s.group.Touch()
	outbound := s.group.selected(N.NetworkUDP)
	if outbound == nil {
		return nil, E.New("missing supported outbound")
	}
	leaf, _ := historyTarget(outbound, N.NetworkUDP, s.group.link)
	conn, err := outbound.ListenPacket(ctx, destination)
	if err == nil {
		return s.group.interruptGroup.NewPacketConn(conn, interrupt.IsExternalConnectionFromContext(ctx), interrupt.IsProviderConnectionFromContext(ctx)), nil
	}
	s.logger.ErrorContext(ctx, err)
	s.InvalidateURLTestHistory(leaf, N.NetworkUDP)
	return nil, err
}

type URLTestGroup struct {
	ctx                          context.Context
	outbound                     adapter.OutboundManager
	pause                        pause.Manager
	pauseCallback                *list.Element[pause.Callback]
	logger                       log.Logger
	outbounds                    []adapter.Outbound
	link                         string
	interval                     time.Duration
	tolerance                    uint16
	idleTimeout                  time.Duration
	history                      *urltest.HistoryStorage
	checking                     atomic.Bool
	selectedOutboundTCP          adapter.Outbound
	selectedOutboundUDP          adapter.Outbound
	interruptGroup               *interrupt.Group
	interruptExternalConnections bool
	access                       sync.Mutex
	updateAccess                 sync.Mutex
	ticker                       *time.Ticker
	close                        chan struct{}
	closeOnce                    sync.Once
	started                      bool
	lastActive                   common.TypedValue[time.Time]
	nodesAccess                  sync.RWMutex
	testAccess                   sync.Mutex
	fallback                     option.URLTestFallbackOptions
}

func NewURLTestGroup(ctx context.Context, outboundManager adapter.OutboundManager, logger log.Logger, outbounds []adapter.Outbound, link string, interval time.Duration, tolerance uint16, idleTimeout time.Duration, interruptExternalConnections bool) (*URLTestGroup, error) {
	if interval == 0 {
		interval = C.DefaultURLTestInterval
	}
	if tolerance == 0 {
		tolerance = 50
	}
	if idleTimeout == 0 {
		idleTimeout = C.DefaultURLTestIdleTimeout
	}
	if interval > idleTimeout {
		return nil, E.New("interval must be less or equal than idle_timeout")
	}
	history := service.PtrFromContext[urltest.HistoryStorage](ctx)
	if history == nil {
		return nil, E.New("missing URL test history storage")
	}
	return &URLTestGroup{
		ctx:                          ctx,
		outbound:                     outboundManager,
		logger:                       logger,
		outbounds:                    outbounds,
		link:                         urltest.NormalizeURL(link),
		interval:                     interval,
		tolerance:                    tolerance,
		idleTimeout:                  idleTimeout,
		history:                      history,
		close:                        make(chan struct{}),
		pause:                        service.FromContext[pause.Manager](ctx),
		interruptGroup:               interrupt.NewGroup(),
		interruptExternalConnections: interruptExternalConnections,
	}, nil
}

func (g *URLTestGroup) PostStart() {
	g.access.Lock()
	defer g.access.Unlock()
	g.started = true
	g.lastActive.Store(time.Now())
	go g.CheckOutbounds(g.ctx, false)
}

func (g *URLTestGroup) Touch() {
	g.access.Lock()
	defer g.access.Unlock()
	if !g.started {
		return
	}
	if g.ticker != nil {
		g.lastActive.Store(time.Now())
		return
	}
	ticker := time.NewTicker(g.interval)
	g.ticker = ticker
	g.pauseCallback = pause.RegisterTicker(g.pause, ticker, g.interval, nil)
	go g.loopCheck(ticker, g.close)
}

func (g *URLTestGroup) Close() error {
	g.access.Lock()
	g.started = false
	if g.ticker != nil {
		g.ticker.Stop()
		g.ticker = nil
	}
	if g.pauseCallback != nil {
		g.pause.UnregisterCallback(g.pauseCallback)
		g.pauseCallback = nil
	}
	g.access.Unlock()
	g.closeOnce.Do(func() { close(g.close) })
	g.testAccess.Lock()
	g.testAccess.Unlock()
	return nil
}

func (g *URLTestGroup) Select(network string) (adapter.Outbound, bool) {
	g.nodesAccess.RLock()
	defer g.nodesAccess.RUnlock()
	return g.selectLocked(network)
}

func (g *URLTestGroup) selectLocked(network string) (adapter.Outbound, bool) {
	var minDelay uint16
	var minOutbound adapter.Outbound
	if g.fallback.Enabled {
		maxDelay := time.Duration(g.fallback.MaxDelay).Milliseconds()
		var first adapter.Outbound
		for _, detour := range g.outbounds {
			if !common.Contains(detour.Network(), network) {
				continue
			}
			if first == nil {
				first = detour
			}
			history := g.loadHistory(detour, network)
			if history == nil {
				continue
			}
			if maxDelay <= 0 || int64(history.Delay) <= maxDelay {
				return detour, true
			}
			if minOutbound == nil || history.Delay < minDelay {
				minOutbound, minDelay = detour, history.Delay
			}
		}
		if minOutbound != nil {
			return minOutbound, true
		}
		return first, false
	}
	switch network {
	case N.NetworkTCP:
		if g.selectedOutboundTCP != nil {
			if history := g.loadHistory(g.selectedOutboundTCP, N.NetworkTCP); history != nil {
				minOutbound = g.selectedOutboundTCP
				minDelay = history.Delay
			}
		}
	case N.NetworkUDP:
		if g.selectedOutboundUDP != nil {
			if history := g.loadHistory(g.selectedOutboundUDP, N.NetworkUDP); history != nil {
				minOutbound = g.selectedOutboundUDP
				minDelay = history.Delay
			}
		}
	}
	for _, detour := range g.outbounds {
		if !common.Contains(detour.Network(), network) {
			continue
		}
		history := g.loadHistory(detour, network)
		if history == nil {
			continue
		}
		if minDelay == 0 || uint32(minDelay) > uint32(history.Delay)+uint32(g.tolerance) {
			minDelay = history.Delay
			minOutbound = detour
		}
	}
	if minOutbound == nil {
		for _, detour := range g.outbounds {
			if !common.Contains(detour.Network(), network) {
				continue
			}
			return detour, false
		}
		return nil, false
	}
	return minOutbound, true
}

func (g *URLTestGroup) loopCheck(ticker *time.Ticker, closeChan <-chan struct{}) {
	if time.Since(g.lastActive.Load()) > g.interval {
		g.lastActive.Store(time.Now())
		g.CheckOutbounds(g.ctx, false)
	}
	for {
		select {
		case <-closeChan:
			return
		case <-ticker.C:
		}
		if time.Since(g.lastActive.Load()) > g.idleTimeout {
			g.access.Lock()
			if g.ticker == ticker {
				g.ticker.Stop()
				g.ticker = nil
				g.pause.UnregisterCallback(g.pauseCallback)
				g.pauseCallback = nil
			}
			g.access.Unlock()
			return
		}
		g.CheckOutbounds(g.ctx, false)
	}
}

func (g *URLTestGroup) CheckOutbounds(ctx context.Context, force bool) {
	_, _ = g.urlTest(ctx, force)
}

func (g *URLTestGroup) URLTest(ctx context.Context) (map[string]uint16, error) {
	return g.urlTest(ctx, true)
}

func (g *URLTestGroup) urlTest(ctx context.Context, force bool) (map[string]uint16, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if g.checking.Swap(true) {
		return make(map[string]uint16), nil
	}
	defer g.checking.Store(false)
	g.testAccess.Lock()
	defer g.testAccess.Unlock()
	g.nodesAccess.RLock()
	nodes := slices.Clone(g.outbounds)
	g.nodesAccess.RUnlock()
	result := URLTestOutbounds(ctx, g.outbound, g.history, g.logger, nodes, g.link, g.interval, force)
	g.performUpdateCheck()
	return result, nil
}

type urlTestResult struct {
	delay uint16
	err   error
}

type urlTestBatch struct {
	ctx      context.Context
	outbound adapter.OutboundManager
	history  *urltest.HistoryStorage
	logger   log.Logger
	batch    *batch.Batch[any]
	checked  map[string]bool
	groups   []urlTestBatchGroup
	access   sync.Mutex
	result   map[string]uint16
}

type urlTestBatchGroup struct {
	outbound adapter.OutboundGroup
	link     string
}

func URLTestOutbounds(ctx context.Context, outboundManager adapter.OutboundManager, history *urltest.HistoryStorage, logger log.Logger, outbounds []adapter.Outbound, link string, interval time.Duration, force bool) map[string]uint16 {
	b, _ := batch.New(ctx, batch.WithConcurrencyNum[any](10))
	testBatch := &urlTestBatch{
		ctx:      ctx,
		outbound: outboundManager,
		history:  history,
		logger:   logger,
		batch:    b,
		checked:  make(map[string]bool),
		result:   make(map[string]uint16),
	}
	testBatch.test(outbounds, link, interval, force)
	b.Wait()
	for _, outboundGroup := range testBatch.groups {
		leaf, link := historyTarget(outboundGroup.outbound, N.NetworkTCP, outboundGroup.link)
		if leaf == nil {
			continue
		}
		groupHistory := history.LoadURLTestHistory(leaf.Tag(), link)
		if groupHistory != nil {
			testBatch.result[outboundGroup.outbound.Tag()] = groupHistory.Delay
		}
	}
	return testBatch.result
}

func (b *urlTestBatch) test(outbounds []adapter.Outbound, link string, interval time.Duration, force bool) {
	link = urltest.NormalizeURL(link)
	for _, detour := range outbounds {
		tag := detour.Tag()
		if b.checked[tag] {
			continue
		}
		switch nested := detour.(type) {
		case *URLTest:
			b.checked[tag] = true
			b.groups = append(b.groups, urlTestBatchGroup{nested, link})
			b.batch.Go(tag, func() (any, error) {
				nestedResult, _ := nested.group.urlTest(b.ctx, force)
				b.access.Lock()
				maps.Copy(b.result, nestedResult)
				b.access.Unlock()
				return nil, nil
			})
		case adapter.OutboundGroup:
			b.checked[tag] = true
			b.groups = append(b.groups, urlTestBatchGroup{nested, link})
			b.test(common.FilterNotNil(common.Map(nested.All(), func(it string) adapter.Outbound {
				member, _ := b.outbound.Outbound(it)
				return member
			})), link, interval, force)
		default:
			history := b.history.LoadURLTestHistory(tag, link)
			if !force && history != nil && time.Since(history.Time) < interval {
				b.access.Lock()
				b.result[tag] = history.Delay
				b.access.Unlock()
				b.checked[tag] = true
				continue
			}
			b.checked[tag] = true
			generation := b.history.OutboundGeneration(tag)
			b.batch.Go(tag, func() (any, error) {
				testCtx, cancel := context.WithTimeout(b.ctx, C.TCPTimeout)
				defer cancel()
				testChan := make(chan urlTestResult, 1)
				go func() {
					delay, testErr := urltest.URLTest(testCtx, link, detour)
					testChan <- urlTestResult{delay, testErr}
				}()
				var testResult urlTestResult
				select {
				case testResult = <-testChan:
				case <-testCtx.Done():
					testResult.err = testCtx.Err()
				}
				current, loaded := b.outbound.Outbound(tag)
				if !loaded || current != detour {
					return nil, nil
				}
				if testResult.err != nil {
					if b.ctx.Err() != nil {
						return nil, nil
					}
					b.logger.Debug("outbound ", tag, " unavailable: ", testResult.err)
					b.history.DeleteURLTestHistory(tag, link, generation)
				} else {
					b.logger.Debug("outbound ", tag, " available: ", testResult.delay, "ms")
					b.history.StoreURLTestHistory(tag, link, generation, &adapter.URLTestHistory{
						Time:  time.Now(),
						Delay: testResult.delay,
					})
					b.access.Lock()
					b.result[tag] = testResult.delay
					b.access.Unlock()
				}
				return nil, nil
			})
		}
	}
}

func (g *URLTestGroup) performUpdateCheck() {
	g.updateAccess.Lock()
	defer g.updateAccess.Unlock()
	g.nodesAccess.Lock()
	defer g.nodesAccess.Unlock()
	var (
		updated  bool
		selected bool
	)
	if outbound, exists := g.selectLocked(N.NetworkTCP); outbound != nil && (g.selectedOutboundTCP == nil || (exists && outbound != g.selectedOutboundTCP)) {
		if g.selectedOutboundTCP != nil {
			updated = true
		}
		g.selectedOutboundTCP = outbound
		selected = true
	}
	if outbound, exists := g.selectLocked(N.NetworkUDP); outbound != nil && (g.selectedOutboundUDP == nil || (exists && outbound != g.selectedOutboundUDP)) {
		if g.selectedOutboundUDP != nil {
			updated = true
		}
		g.selectedOutboundUDP = outbound
		selected = true
	}
	if updated {
		g.interruptGroup.Interrupt(g.interruptExternalConnections)
	}
	if selected {
		g.history.NotifyUpdated()
	}
}

func (g *URLTestGroup) selected(network string) adapter.Outbound {
	g.nodesAccess.RLock()
	defer g.nodesAccess.RUnlock()
	var selected adapter.Outbound
	if network == N.NetworkUDP {
		selected = g.selectedOutboundUDP
	} else {
		selected = g.selectedOutboundTCP
	}
	if selected == nil {
		selected, _ = g.selectLocked(network)
	}
	return selected
}

func (s *URLTest) setMemberTags(nodes []adapter.Outbound) {
	tags := make([]string, 0, len(nodes))
	for _, node := range nodes {
		tags = append(tags, node.Tag())
	}
	s.membersAccess.Lock()
	s.tags = tags
	s.membersAccess.Unlock()
}

func (s *URLTest) updateMembers(nodes []adapter.Outbound) error {
	g := s.group
	if g == nil {
		return nil
	}
	g.testAccess.Lock()
	g.nodesAccess.Lock()
	oldTCP, oldUDP := g.selectedOutboundTCP, g.selectedOutboundUDP
	g.outbounds = nodes
	g.selectedOutboundTCP, g.selectedOutboundUDP = nil, nil
	g.selectedOutboundTCP, _ = g.selectLocked(N.NetworkTCP)
	g.selectedOutboundUDP, _ = g.selectLocked(N.NetworkUDP)
	changed := oldTCP != g.selectedOutboundTCP || oldUDP != g.selectedOutboundUDP
	g.nodesAccess.Unlock()
	g.testAccess.Unlock()
	s.setMemberTags(nodes)
	if changed {
		g.interruptGroup.Interrupt(g.interruptExternalConnections)
		g.history.NotifyUpdated()
	}
	g.access.Lock()
	active := g.started && time.Since(g.lastActive.Load()) <= g.idleTimeout
	if g.ticker != nil {
		g.ticker.Reset(g.interval)
	}
	g.access.Unlock()
	if active {
		go g.CheckOutbounds(g.ctx, true)
	}
	return nil
}
