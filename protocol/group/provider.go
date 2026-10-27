package group

import (
	"context"
	"regexp"
	"slices"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"
)

// providerMembers serializes provider notifications and owns their registrations
// in the group's lifecycle scope. Published member slices are immutable.
type providerMembers struct {
	access    sync.Mutex
	ctx       context.Context
	manager   adapter.OutboundManager
	options   option.GroupCommonOption
	providers []adapter.Provider
}

func newProviderMembers(ctx context.Context, options option.GroupCommonOption) *providerMembers {
	return &providerMembers{ctx: ctx, manager: service.FromContext[adapter.OutboundManager](ctx), options: options}
}

func (m *providerMembers) start(scope *adapter.Scope, update func([]adapter.Outbound) error) ([]adapter.Outbound, error) {
	providerManager := service.FromContext[adapter.ProviderManager](m.ctx)
	tags := slices.Clone(m.options.Providers)
	if m.options.UseAllProviders && providerManager != nil {
		for _, provider := range providerManager.Providers() {
			if !slices.Contains(tags, provider.Tag()) {
				tags = append(tags, provider.Tag())
			}
		}
	}
	if len(m.options.Outbounds)+len(tags) == 0 {
		return nil, E.New("missing outbound and provider tags")
	}
	for _, tag := range tags {
		if providerManager == nil {
			return nil, E.New("missing provider manager")
		}
		provider, loaded := providerManager.Get(tag)
		if !loaded {
			return nil, E.New("outbound provider not found: ", tag)
		}
		m.providers = append(m.providers, provider)
		element := provider.RegisterCallback(func(string) error {
			m.access.Lock()
			defer m.access.Unlock()
			nodes, err := m.collect()
			if err != nil {
				return err
			}
			return update(nodes)
		})
		scope.Add(func() error { provider.UnregisterCallback(element); return nil })
	}
	return m.collect()
}

func (m *providerMembers) collect() ([]adapter.Outbound, error) {
	var nodes []adapter.Outbound
	seen := make(map[string]bool)
	appendNode := func(node adapter.Outbound) {
		if !seen[node.Tag()] {
			seen[node.Tag()] = true
			nodes = append(nodes, node)
		}
	}
	for _, tag := range m.options.Outbounds {
		node, found := m.manager.Outbound(tag)
		if !found {
			return nil, E.New("outbound not found: ", tag)
		}
		appendNode(node)
	}
	include, exclude := (*regexp.Regexp)(m.options.Include), (*regexp.Regexp)(m.options.Exclude)
	for _, provider := range m.providers {
		for _, node := range provider.Outbounds() {
			if exclude != nil && exclude.MatchString(node.Tag()) {
				continue
			}
			if include != nil && !include.MatchString(node.Tag()) {
				continue
			}
			appendNode(node)
		}
	}
	if len(nodes) == 0 {
		node, found := m.manager.Outbound("Compatible")
		if !found {
			return nil, E.New("empty provider group: Compatible outbound not found")
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}
