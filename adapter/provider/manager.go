package provider

import (
	"context"
	"io"
	"os"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/taskmonitor"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/sing/common/x/list"
)

var _ adapter.ProviderManager = (*Manager)(nil)

type Manager struct {
	logger          log.ContextLogger
	registry        adapter.ProviderRegistry
	access          sync.RWMutex
	started         bool
	stage           adapter.StartStage
	providers       []adapter.Provider
	providerByTag   map[string]adapter.Provider
	updateCallbacks list.List[adapter.ProviderManagerUpdateCallback]
}

func NewManager(logger logger.ContextLogger, registry adapter.ProviderRegistry) *Manager {
	return &Manager{
		logger:        logger,
		registry:      registry,
		providerByTag: make(map[string]adapter.Provider),
	}
}

func (m *Manager) Initialize() {
}

func (m *Manager) Start(stage adapter.StartStage) error {
	m.access.Lock()
	if m.started && m.stage >= stage {
		panic("already started")
	}
	m.started = true
	m.stage = stage
	providers := append([]adapter.Provider(nil), m.providers...)
	m.access.Unlock()
	if stage == adapter.StartStateStart && len(providers) > 0 {
		startContext := adapter.NewHTTPStartContext()
		defer startContext.Close()
		for _, provider := range providers {
			if contextStarter, ok := provider.(interface {
				StartContext(ctx context.Context, startContext *adapter.HTTPStartContext) error
			}); ok {
				err := contextStarter.StartContext(context.Background(), startContext)
				if err != nil {
					return E.Cause(err, stage, " provider/", provider.Type(), "[", provider.Tag(), "]")
				}
			}
		}
		return nil
	}
	return nil
}

func (m *Manager) Close() error {
	monitor := taskmonitor.New(m.logger, C.StopTimeout)
	m.access.Lock()
	m.started = false
	m.stage = 0
	providers := m.providers
	m.providers = nil
	clear(m.providerByTag)
	m.access.Unlock()
	var err error
	for _, provider := range providers {
		if closer, isCloser := provider.(io.Closer); isCloser {
			monitor.Start("close provider/", provider.Type(), "[", provider.Tag(), "]")
			err = E.Append(err, closer.Close(), func(err error) error {
				return E.Cause(err, "close provider/", provider.Type(), "[", provider.Tag(), "]")
			})
			monitor.Finish()
		}
	}
	m.notifyProviderManagerCallbacks()
	return err
}

func (m *Manager) Providers() []adapter.Provider {
	m.access.RLock()
	defer m.access.RUnlock()
	return append([]adapter.Provider(nil), m.providers...)
}

func (m *Manager) Get(tag string) (adapter.Provider, bool) {
	m.access.RLock()
	provider, found := m.providerByTag[tag]
	m.access.RUnlock()
	return provider, found
}

func (m *Manager) Remove(tag string) error {
	m.access.Lock()
	provider, found := m.providerByTag[tag]
	if !found {
		m.access.Unlock()
		return os.ErrInvalid
	}
	delete(m.providerByTag, tag)
	index := common.Index(m.providers, func(it adapter.Provider) bool {
		return it == provider
	})
	if index == -1 {
		panic("invalid provider index")
	}
	m.providers = append(m.providers[:index], m.providers[index+1:]...)
	started := m.started
	m.access.Unlock()
	var err error
	if started {
		err = common.Close(provider)
	}
	m.notifyProviderManagerCallbacks()
	return err
}

func (m *Manager) Create(ctx context.Context, router adapter.Router, logFactory log.Factory, tag string, providerType string, options any) error {
	if tag == "" {
		return os.ErrInvalid
	}

	provider, err := m.registry.CreateProvider(ctx, router, logFactory, tag, providerType, options)
	if err != nil {
		return err
	}
	m.access.Lock()
	changed := false
	defer func() {
		m.access.Unlock()
		if changed {
			m.notifyProviderManagerCallbacks()
		}
	}()
	if m.started {
		if m.stage >= adapter.StartStateStart {
			if contextStarter, ok := provider.(interface {
				StartContext(ctx context.Context, startContext *adapter.HTTPStartContext) error
			}); ok {
				startContext := adapter.NewHTTPStartContext()
				err = contextStarter.StartContext(context.Background(), startContext)
				startContext.Close()
				if err != nil {
					return E.Cause(err, "start provider/", provider.Type(), "[", provider.Tag(), "]")
				}
			}
		}
	}
	if existsProvider, loaded := m.providerByTag[tag]; loaded {
		if m.started {
			err = common.Close(existsProvider)
			if err != nil {
				return E.Cause(err, "close provider", provider.Type(), "[", existsProvider.Tag(), "]")
			}
		}
		existsIndex := common.Index(m.providers, func(it adapter.Provider) bool {
			return it == existsProvider
		})
		if existsIndex == -1 {
			panic("invalid provider index")
		}
		m.providers = append(m.providers[:existsIndex], m.providers[existsIndex+1:]...)
	}
	m.providers = append(m.providers, provider)
	m.providerByTag[tag] = provider
	changed = true
	return nil
}

func (m *Manager) RegisterProviderManagerCallback(callback adapter.ProviderManagerUpdateCallback) *list.Element[adapter.ProviderManagerUpdateCallback] {
	m.access.Lock()
	defer m.access.Unlock()
	return m.updateCallbacks.PushBack(callback)
}

func (m *Manager) UnregisterProviderManagerCallback(element *list.Element[adapter.ProviderManagerUpdateCallback]) {
	if element == nil {
		return
	}
	m.access.Lock()
	defer m.access.Unlock()
	m.updateCallbacks.Remove(element)
}

func (m *Manager) notifyProviderManagerCallbacks() {
	m.access.RLock()
	callbacks := make([]adapter.ProviderManagerUpdateCallback, 0)
	for element := m.updateCallbacks.Front(); element != nil; element = element.Next() {
		callbacks = append(callbacks, element.Value)
	}
	m.access.RUnlock()
	for _, callback := range callbacks {
		callback()
	}
}
