package endpoint

import (
	"context"
	"strings"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
)

var (
	_ adapter.EndpointManager       = (*Manager)(nil)
	_ adapter.OutboundFingerprinter = (*Manager)(nil)
)

type Manager struct {
	registry          adapter.EndpointRegistry
	access            sync.Mutex
	scope             *adapter.Scope
	endpoints         []adapter.Endpoint
	endpointByTag     map[string]adapter.Endpoint
	providerEndpoints map[string][]adapter.Endpoint
	fingerprintByTag  map[string]string
}

func NewManager(registry adapter.EndpointRegistry) *Manager {
	return &Manager{
		registry:          registry,
		endpointByTag:     make(map[string]adapter.Endpoint),
		providerEndpoints: make(map[string][]adapter.Endpoint),
		fingerprintByTag:  make(map[string]string),
	}
}

func (m *Manager) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	m.access.Lock()
	defer m.access.Unlock()
	if stage == adapter.StartStateInitialize {
		m.scope = scope
	}
	if stage == adapter.StartStateStart {
		return nil
	}
	for _, endpoint := range m.endpoints {
		name := "endpoint/" + endpoint.Type() + "[" + endpoint.Tag() + "]"
		err := scope.Start(name, endpoint, stage)
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) StartEndpoint(endpoint adapter.Endpoint) error {
	return m.scope.Start("endpoint/"+endpoint.Type()+"["+endpoint.Tag()+"]", endpoint, adapter.StartStateStart)
}

func (m *Manager) Endpoints() []adapter.Endpoint {
	m.access.Lock()
	defer m.access.Unlock()
	endpoints := append([]adapter.Endpoint(nil), m.endpoints...)
	for _, nodes := range m.providerEndpoints {
		endpoints = append(endpoints, nodes...)
	}
	return endpoints
}

func (m *Manager) Get(tag string) (adapter.Endpoint, bool) {
	m.access.Lock()
	defer m.access.Unlock()
	endpoint, found := m.endpointByTag[tag]
	return endpoint, found
}

func (m *Manager) OutboundFingerprint(tag string) (string, bool) {
	m.access.Lock()
	defer m.access.Unlock()
	fingerprint, found := m.fingerprintByTag[tag]
	return fingerprint, found
}

func (m *Manager) Create(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, outboundType string, options any) error {
	endpoint, err := m.registry.Create(ctx, router, logger, tag, outboundType, options)
	if err != nil {
		return err
	}
	m.access.Lock()
	defer m.access.Unlock()
	_, loaded := m.endpointByTag[tag]
	if loaded {
		return E.New("duplicate endpoint tag: ", tag)
	}
	m.endpoints = append(m.endpoints, endpoint)
	m.endpointByTag[tag] = endpoint
	if fingerprint := adapter.OutboundFingerprint(outboundType, options); fingerprint != "" {
		m.fingerprintByTag[tag] = fingerprint
	}
	return nil
}

func (m *Manager) PublishProvider(namespace string, nodes []adapter.Endpoint, fingerprints map[string]string) error {
	m.access.Lock()
	defer m.access.Unlock()
	prefix := namespace + "/"
	old := m.providerEndpoints[namespace]
	owned := make(map[string]bool, len(old))
	for _, node := range old {
		owned[node.Tag()] = true
	}
	seen := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		tag := node.Tag()
		if namespace == "" || !strings.HasPrefix(tag, prefix) || len(tag) == len(prefix) {
			return E.New("invalid provider endpoint namespace: ", tag)
		}
		if seen[tag] || (m.endpointByTag[tag] != nil && !owned[tag]) {
			return E.New("duplicate endpoint tag: ", tag)
		}
		seen[tag] = true
	}
	for _, node := range old {
		delete(m.endpointByTag, node.Tag())
		delete(m.fingerprintByTag, node.Tag())
	}
	for _, node := range nodes {
		m.endpointByTag[node.Tag()] = node
		if fingerprint := fingerprints[node.Tag()]; fingerprint != "" {
			m.fingerprintByTag[node.Tag()] = fingerprint
		}
	}
	if len(nodes) == 0 {
		delete(m.providerEndpoints, namespace)
	} else {
		m.providerEndpoints[namespace] = nodes
	}
	return nil
}
