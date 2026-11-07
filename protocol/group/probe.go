package group

import (
	"context"
	"io"
	"net"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/service/nodeprobe"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

func RegisterProbe(registry *outbound.Registry) {
	outbound.Register[option.ProbeOutboundOptions](registry, C.TypeProbe, NewProbe)
}

var (
	_ adapter.OutboundGroup           = (*Probe)(nil)
	_ adapter.InterfaceUpdateListener = (*Probe)(nil)
	_ adapter.Referrer                = (*Probe)(nil)
	_ nodeprobe.Group                 = (*Probe)(nil)
)

// Probe is a group whose selection is made by a node-probe service.
type Probe struct {
	outbound.Adapter
	ctx                          context.Context
	logger                       log.ContextLogger
	probeTag                     string
	policy                       string
	handle                       atomic.Pointer[nodeprobe.Handle]
	tags                         []string
	candidates                   []adapter.Outbound
	selected                     common.TypedValue[adapter.Outbound]
	history                      *urltest.HistoryStorage
	interruptGroup               *interrupt.Group
	interruptExternalConnections bool
	members                      *providerMembers
	membersAccess                sync.RWMutex
}

func NewProbe(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.ProbeOutboundOptions) (adapter.Outbound, error) {
	if options.Probe == "" {
		return nil, E.New("missing probe service tag")
	}
	if options.Policy == "" {
		return nil, E.New("missing policy")
	}
	if len(options.Outbounds) == 0 && len(options.Providers) == 0 && !options.UseAllProviders {
		return nil, E.New("missing tags")
	}
	for i, tag := range options.Outbounds {
		if slices.Contains(options.Outbounds[:i], tag) {
			return nil, E.New("duplicate tag: ", tag)
		}
	}
	return &Probe{
		Adapter:                      outbound.NewAdapter(C.TypeProbe, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.Outbounds),
		ctx:                          ctx,
		logger:                       logger,
		probeTag:                     options.Probe,
		policy:                       options.Policy,
		history:                      service.PtrFromContext[urltest.HistoryStorage](ctx),
		interruptGroup:               interrupt.NewGroup(),
		interruptExternalConnections: options.InterruptExistConnections,
		members:                      newProviderMembers(ctx, options.GroupCommonOption),
	}, nil
}

func (p *Probe) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	serviceManager := service.FromContext[adapter.ServiceManager](p.ctx)
	if serviceManager == nil {
		return E.New("missing service manager")
	}
	instance, loaded := serviceManager.Get(p.probeTag)
	if !loaded {
		return E.New("probe service not found: ", p.probeTag)
	}
	probeService, isProbe := instance.(*nodeprobe.Service)
	if !isProbe {
		return E.New("service ", p.probeTag, " is not a ", C.TypeNodeProbe, " service")
	}
	nodes, err := p.members.start(scope, p.updateMembers)
	if err != nil {
		return err
	}
	err = p.updateMembers(nodes)
	if err != nil {
		return err
	}
	handle, err := probeService.Register(p, p.policy)
	if err != nil {
		return err
	}
	p.handle.Store(handle)
	scope.Add(func() error {
		handle.Close()
		return nil
	})
	return nil
}

func (p *Probe) updateMembers(nodes []adapter.Outbound) error {
	p.membersAccess.Lock()
	tags := make([]string, 0, len(nodes))
	byTag := make(map[string]adapter.Outbound, len(nodes))
	for _, node := range nodes {
		tags = append(tags, node.Tag())
		byTag[node.Tag()] = node
	}
	var selected adapter.Outbound
	if previous := p.selected.Load(); previous != nil {
		selected = byTag[previous.Tag()]
	}
	if selected == nil && p.Tag() != "" {
		if cacheFile := service.FromContext[adapter.CacheFile](p.ctx); cacheFile != nil {
			selected = byTag[cacheFile.LoadSelected(p.Tag())]
		}
	}
	lost := selected == nil && p.selected.Load() != nil
	if selected == nil {
		selected = nodes[0]
	}
	p.tags, p.candidates = tags, nodes
	changed := p.selected.Swap(selected) != selected
	p.membersAccess.Unlock()
	if changed {
		p.interruptGroup.Interrupt(p.interruptExternalConnections)
		if p.history != nil {
			p.history.NotifyUpdated()
		}
	}
	if handle := p.handle.Load(); lost && handle != nil {
		handle.Trigger()
	}
	return nil
}

func (p *Probe) Candidates() []adapter.Outbound {
	p.membersAccess.RLock()
	defer p.membersAccess.RUnlock()
	return p.candidates
}

func (p *Probe) Current() adapter.Outbound {
	return p.selected.Load()
}

// ProbeHandle returns the node-probe registration, or nil before start.
func (p *Probe) ProbeHandle() *nodeprobe.Handle {
	return p.handle.Load()
}

func (p *Probe) Choose(detour adapter.Outbound, reason string) {
	previous := p.selected.Swap(detour)
	if previous == detour {
		return
	}
	previousTag := "none"
	if previous != nil {
		previousTag = previous.Tag()
	}
	p.logger.Info("switch ", previousTag, " -> ", detour.Tag(), " (", reason, ")")
	if cacheFile := service.FromContext[adapter.CacheFile](p.ctx); cacheFile != nil {
		err := cacheFile.StoreSelected(p.Tag(), detour.Tag())
		if err != nil {
			p.logger.Error("store selected: ", err)
		}
	}
	p.interruptGroup.Interrupt(p.interruptExternalConnections)
	if p.history != nil {
		p.history.NotifyUpdated()
	}
}

func (p *Probe) All() []string {
	p.membersAccess.RLock()
	defer p.membersAccess.RUnlock()
	return slices.Clone(p.tags)
}

func (p *Probe) Selected(network string) adapter.Outbound {
	return p.selected.Load()
}

func (p *Probe) AttachConnection(closer io.Closer) func() {
	return p.interruptGroup.Add(closer, true)
}

func (p *Probe) References() []string {
	selected := p.selected.Load()
	if selected == nil {
		return nil
	}
	return []string{selected.Tag()}
}

func (p *Probe) InterfaceUpdated(ctx context.Context) {
	if handle := p.handle.Load(); handle != nil {
		handle.NetworkChanged()
	}
}

// DialContext serves callers that dial the group itself, such as a DNS detour;
// routed connections dial the leaf and are observed by ObserveProbeConnection.
func (p *Probe) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	selected := p.selected.Load()
	handle := p.handle.Load()
	conn, err := selected.DialContext(ctx, network, destination)
	if err != nil {
		if ctx.Err() == nil && handle != nil {
			handle.ConnectionDialFailed(selected.Tag())
		}
		return nil, err
	}
	if handle != nil {
		conn = observe([]probeLink{{handle, selected.Tag()}}, conn)
	}
	return p.interruptGroup.NewConn(conn, interrupt.IsExternalConnectionFromContext(ctx), interrupt.IsProviderConnectionFromContext(ctx)), nil
}

func (p *Probe) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	selected := p.selected.Load()
	handle := p.handle.Load()
	conn, err := selected.ListenPacket(ctx, destination)
	if err != nil {
		if ctx.Err() == nil && handle != nil {
			handle.ConnectionDialFailed(selected.Tag())
		}
		return nil, err
	}
	return p.interruptGroup.NewPacketConn(conn, interrupt.IsExternalConnectionFromContext(ctx), interrupt.IsProviderConnectionFromContext(ctx)), nil
}

// probeLink is a probe group on a routed chain and the member it routed through.
type probeLink struct {
	handle *nodeprobe.Handle
	node   string
}

func probeLinks(chain []adapter.Outbound) []probeLink {
	var links []probeLink
	for i, outbound := range chain[:max(len(chain)-1, 0)] {
		if probe, isProbe := outbound.(*Probe); isProbe {
			if handle := probe.handle.Load(); handle != nil {
				links = append(links, probeLink{handle, chain[i+1].Tag()})
			}
		}
	}
	return links
}

func observe(links []probeLink, conn net.Conn) net.Conn {
	for _, link := range links {
		link.handle.ConnectionStarted(link.node)
	}
	return newProbeConn(conn, func(failed bool, down, up int64) {
		for _, link := range links {
			link.handle.ConnectionEnded(link.node, failed, down, up)
		}
	})
}

// ObserveProbeConnection wraps a routed connection whose chain passes probe
// groups, counting it for the member each group routed through.
func ObserveProbeConnection(chain []adapter.Outbound, conn net.Conn) net.Conn {
	links := probeLinks(chain)
	if len(links) == 0 {
		return conn
	}
	return observe(links, conn)
}

// ReportProbeDialFailure counts a routed connection whose leaf failed to
// connect against the members of the probe groups on its chain.
func ReportProbeDialFailure(chain []adapter.Outbound, err error) {
	if E.IsCanceled(err) {
		return
	}
	for _, link := range probeLinks(chain) {
		link.handle.ConnectionDialFailed(link.node)
	}
}
