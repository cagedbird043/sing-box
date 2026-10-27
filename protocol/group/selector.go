package group

import (
	"context"
	"io"
	"net"
	"slices"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

func RegisterSelector(registry *outbound.Registry) {
	outbound.Register[option.SelectorOutboundOptions](registry, C.TypeSelector, NewSelector)
}

var (
	_ adapter.OutboundGroup = (*Selector)(nil)
	_ adapter.Referrer      = (*Selector)(nil)
)

type Selector struct {
	outbound.Adapter
	ctx                          context.Context
	outbound                     adapter.OutboundManager
	logger                       logger.ContextLogger
	tags                         []string
	defaultTag                   string
	outbounds                    map[string]adapter.Outbound
	selected                     common.TypedValue[adapter.Outbound]
	history                      *urltest.HistoryStorage
	interruptGroup               *interrupt.Group
	interruptExternalConnections bool
	members                      *providerMembers
	membersAccess                sync.RWMutex
}

func NewSelector(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.SelectorOutboundOptions) (adapter.Outbound, error) {
	outbound := &Selector{
		Adapter:                      outbound.NewAdapter(C.TypeSelector, tag, nil, options.Outbounds),
		ctx:                          ctx,
		outbound:                     service.FromContext[adapter.OutboundManager](ctx),
		logger:                       logger,
		tags:                         options.Outbounds,
		defaultTag:                   options.Default,
		outbounds:                    make(map[string]adapter.Outbound),
		history:                      service.PtrFromContext[urltest.HistoryStorage](ctx),
		interruptGroup:               interrupt.NewGroup(),
		interruptExternalConnections: options.InterruptExistConnections,
		members:                      newProviderMembers(ctx, options.GroupCommonOption),
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

func (s *Selector) Network() []string {
	selected := s.selected.Load()
	if selected == nil {
		return []string{N.NetworkTCP, N.NetworkUDP}
	}
	return selected.Network()
}

func (s *Selector) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	nodes, err := s.members.start(scope, s.updateMembers)
	if err != nil {
		return err
	}
	return s.updateMembers(nodes)
}

func (s *Selector) All() []string {
	s.membersAccess.RLock()
	defer s.membersAccess.RUnlock()
	return slices.Clone(s.tags)
}

func (s *Selector) Selected(network string) adapter.Outbound {
	return s.selected.Load()
}

func (s *Selector) AttachConnection(closer io.Closer) func() {
	return s.interruptGroup.Add(closer, true)
}

func (s *Selector) References() []string {
	selected := s.selected.Load()
	if selected == nil {
		return nil
	}
	return []string{selected.Tag()}
}

func (s *Selector) SelectOutbound(tag string) bool {
	s.membersAccess.RLock()
	defer s.membersAccess.RUnlock()
	detour, loaded := s.outbounds[tag]
	if !loaded {
		return false
	}
	if s.selected.Swap(detour) == detour {
		return true
	}
	if s.Tag() != "" {
		cacheFile := service.FromContext[adapter.CacheFile](s.ctx)
		if cacheFile != nil {
			err := cacheFile.StoreSelected(s.Tag(), tag)
			if err != nil {
				s.logger.Error("store selected: ", err)
			}
		}
	}
	s.interruptGroup.Interrupt(s.interruptExternalConnections)
	if s.history != nil {
		s.history.NotifyUpdated()
	}
	return true
}

func (s *Selector) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	conn, err := s.selected.Load().DialContext(ctx, network, destination)
	if err != nil {
		return nil, err
	}
	return s.interruptGroup.NewConn(conn, interrupt.IsExternalConnectionFromContext(ctx), interrupt.IsProviderConnectionFromContext(ctx)), nil
}

func (s *Selector) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	conn, err := s.selected.Load().ListenPacket(ctx, destination)
	if err != nil {
		return nil, err
	}
	return s.interruptGroup.NewPacketConn(conn, interrupt.IsExternalConnectionFromContext(ctx), interrupt.IsProviderConnectionFromContext(ctx)), nil
}

func RealTag(detour adapter.Outbound, network string) string {
	for {
		group, isGroup := detour.(adapter.OutboundGroup)
		if !isGroup {
			return detour.Tag()
		}
		detour = group.Selected(network)
		if detour == nil {
			return ""
		}
	}
}

func (s *Selector) updateMembers(nodes []adapter.Outbound) error {
	s.membersAccess.Lock()
	defer s.membersAccess.Unlock()
	tags := make([]string, 0, len(nodes))
	outbounds := make(map[string]adapter.Outbound, len(nodes))
	for _, node := range nodes {
		tags = append(tags, node.Tag())
		outbounds[node.Tag()] = node
	}
	var selected adapter.Outbound
	if previous := s.selected.Load(); previous != nil {
		selected = outbounds[previous.Tag()]
	}
	if selected == nil && s.Tag() != "" {
		if cacheFile := service.FromContext[adapter.CacheFile](s.ctx); cacheFile != nil {
			selected = outbounds[cacheFile.LoadSelected(s.Tag())]
		}
	}
	if selected == nil && s.defaultTag != "" {
		selected = outbounds[s.defaultTag]
		if selected == nil && len(s.members.providers) == 0 {
			return E.New("default outbound not found: ", s.defaultTag)
		}
	}
	if selected == nil {
		selected = nodes[0]
	}
	s.tags, s.outbounds = tags, outbounds
	if s.selected.Swap(selected) != selected {
		s.interruptGroup.Interrupt(s.interruptExternalConnections)
		if s.history != nil {
			s.history.NotifyUpdated()
		}
	}
	return nil
}
