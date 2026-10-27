package provider

import (
	"strings"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"
)

// A generation owns all scopes in one subscription. Rebuilding a generation
// also rebuilds detour dependents, so none retain a node from a retired scope.
type nodeGeneration struct {
	scope     *adapter.Scope
	nodes     []adapter.Outbound
	byTag     map[string]adapter.Outbound
	endpoints []adapter.Endpoint
	outbound  adapter.OutboundManager
}

type generationOutbounds struct {
	adapter.OutboundManager
	generation *nodeGeneration
	namespace  string
}

func (m *generationOutbounds) Outbound(tag string) (adapter.Outbound, bool) {
	if node, ok := m.generation.byTag[tag]; ok {
		return node, true
	}
	if strings.HasPrefix(tag, m.namespace+"/") {
		return nil, false
	}
	return m.OutboundManager.Outbound(tag)
}

func (a *Adapter) createGeneration(outbounds []option.Outbound, endpoints []option.Endpoint) (_ *nodeGeneration, err error) {
	g := &nodeGeneration{scope: adapter.NewScope(a.ctx, a.logger), byTag: make(map[string]adapter.Outbound)}
	defer func() {
		if err != nil {
			g.scope.Close()
		}
	}()
	resolver := &generationOutbounds{OutboundManager: a.outbound, generation: g, namespace: a.providerTag}
	g.outbound = resolver
	ctx := service.ContextWith[adapter.OutboundManager](service.ExtendContext(g.scope.Context()), resolver)
	outboundRegistry := service.FromContext[adapter.OutboundRegistry](a.ctx)
	endpointRegistry := service.FromContext[adapter.EndpointRegistry](a.ctx)
	tags := a.resolveOutboundTags(outbounds)
	for i, options := range outbounds {
		tag := tags[i]
		if existing, ok := a.outbound.Outbound(tag); ok {
			if _, owned := a.outboundsByTag[existing.Tag()]; !owned {
				return nil, E.New("provider outbound conflicts with existing tag: ", tag)
			}
		}
		node, createErr := outboundRegistry.CreateOutbound(adapter.WithContext(ctx, &adapter.InboundContext{Outbound: tag}), a.router, a.logFactory.NewLogger("outbound/"+options.Type+"["+tag+"]"), tag, options.Type, options.Options)
		if createErr != nil {
			return nil, E.Cause(createErr, "create provider outbound ", tag)
		}
		g.nodes = append(g.nodes, node)
		g.byTag[tag] = node
	}
	tags = a.resolveEndpointTags(endpoints)
	for i, options := range endpoints {
		tag := tags[i]
		if _, duplicate := g.byTag[tag]; duplicate {
			return nil, E.New("duplicate provider node tag: ", tag)
		}
		if existing, ok := a.outbound.Outbound(tag); ok {
			if _, owned := a.outboundsByTag[existing.Tag()]; !owned {
				return nil, E.New("provider endpoint conflicts with existing tag: ", tag)
			}
		}
		node, createErr := endpointRegistry.Create(adapter.WithContext(ctx, &adapter.InboundContext{Outbound: tag}), a.router, a.logFactory.NewLogger("endpoint/"+options.Type+"["+tag+"]"), tag, options.Type, options.Options)
		if createErr != nil {
			return nil, E.Cause(createErr, "create provider endpoint ", tag)
		}
		g.nodes = append(g.nodes, node)
		g.byTag[tag] = node
		g.endpoints = append(g.endpoints, node)
	}
	return g, nil
}

func (g *nodeGeneration) start(stage adapter.StartStage) error {
	remaining := append([]adapter.Outbound(nil), g.nodes...)
	started := make(map[string]bool, len(remaining))
	for len(remaining) != 0 {
		progress := false
		for i := 0; i < len(remaining); {
			node := remaining[i]
			ready := true
			if stage == adapter.StartStateStart {
				for _, dependency := range node.Dependencies() {
					if _, local := g.byTag[dependency]; local {
						if !started[dependency] {
							ready = false
							break
						}
					} else if _, exists := g.outbound.Outbound(dependency); !exists {
						return E.New("dependency[", dependency, "] not found for provider outbound[", node.Tag(), "]")
					}
				}
			}
			if !ready {
				i++
				continue
			}
			if lifecycle, ok := node.(adapter.Lifecycle); ok {
				if err := g.scope.Start("provider-node/"+node.Type()+"["+node.Tag()+"]", lifecycle, stage); err != nil {
					return err
				}
			}
			started[node.Tag()] = true
			remaining = append(remaining[:i], remaining[i+1:]...)
			progress = true
		}
		if !progress {
			return E.New("circular provider outbound dependency")
		}
	}
	return nil
}

func (a *Adapter) publishGeneration(g *nodeGeneration) error {
	outbounds := g.nodes[:len(g.nodes)-len(g.endpoints)]
	outboundPublisher, ok := a.outbound.(adapter.ProviderOutboundManager)
	if !ok {
		return E.New("outbound manager does not support provider namespaces")
	}
	endpointPublisher, ok := a.endpoint.(adapter.ProviderEndpointManager)
	if !ok {
		return E.New("endpoint manager does not support provider namespaces")
	}
	if err := endpointPublisher.PublishProvider(a.providerTag, g.endpoints); err != nil {
		return err
	}
	if err := outboundPublisher.PublishProvider(a.providerTag, outbounds); err != nil {
		var previous []adapter.Endpoint
		if a.generation != nil {
			previous = a.generation.endpoints
		}
		_ = endpointPublisher.PublishProvider(a.providerTag, previous)
		return err
	}
	return nil
}

func (a *Adapter) Advance(stage adapter.StartStage) (err error) {
	a.actionAccess.Lock()
	defer func() {
		a.actionAccess.Unlock()
		if err == nil && stage == adapter.StartStateStarted {
			a.MarkStarted()
		}
	}()
	a.stage = stage
	a.initialized = true
	if a.generation == nil {
		return nil
	}
	if err := a.generation.start(stage); err != nil {
		return err
	}
	if stage == adapter.StartStateStart {
		if err := a.publishGeneration(a.generation); err != nil {
			return err
		}
		a.UpdateGroups()
	}
	return nil
}
