package daemon

import (
	"bytes"
	"context"
	"sync"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/trafficcontrol"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/experimental/deprecated"
	"github.com/sagernet/sing-box/experimental/locale"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/observable"
	"github.com/sagernet/sing/common/x/list"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"

	"github.com/gofrs/uuid/v5"
)

type Instance struct {
	ctx                   context.Context
	cancel                context.CancelFunc
	instance              *box.Box
	connectionManager     adapter.ConnectionManager
	clashServer           adapter.ClashServer
	trafficManager        *trafficcontrol.Manager
	cacheFile             adapter.CacheFile
	pauseManager          pause.Manager
	urlTestHistoryStorage *urltest.HistoryStorage
	outboundManager       adapter.OutboundManager
	endpointManager       adapter.EndpointManager
	providerManager       adapter.ProviderManager
	logFactory            log.Factory

	providerInstanceID       string
	providerStateAccess      sync.Mutex
	providerStateInitialized bool
	providerRevision         uint64
	providerStateRevisions   map[string]uint64
	providerUpdateSubscriber *observable.Subscriber[struct{}]
	providerUpdateObserver   *observable.Observer[struct{}]
	providerCallbackAccess   sync.Mutex
	providerServiceClosed    bool
	providerCallbacks        map[adapter.Provider]*list.Element[adapter.ProviderUpdateCallback]
	providerManagerObserver  adapter.ProviderManagerObservable
	providerManagerCallback  *list.Element[adapter.ProviderManagerUpdateCallback]
}

func (s *StartedService) CheckConfig(ctx context.Context, configContent string) error {
	selectedLocale := locale.FromContext(ctx)
	ctx, _ = locale.ContextWithLocale(s.ctx, selectedLocale.Locale)
	options, err := parseConfig(ctx, configContent)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	instance, err := box.New(box.Options{
		Context: ctx,
		Options: options,
	})
	if err == nil {
		instance.Close()
	}
	return err
}

func (s *StartedService) FormatConfig(ctx context.Context, configContent string) (string, error) {
	selectedLocale := locale.FromContext(ctx)
	ctx, _ = locale.ContextWithLocale(s.ctx, selectedLocale.Locale)
	options, err := parseConfig(ctx, configContent)
	if err != nil {
		return "", err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	err = encoder.Encode(options)
	if err != nil {
		return "", err
	}
	return buffer.String(), nil
}

type OverrideOptions struct {
	AutoRedirect   bool
	IncludePackage []string
	ExcludePackage []string
}

func (s *StartedService) newInstance(ctx context.Context, profileContent string, overrideOptions *OverrideOptions) (*Instance, error) {
	selectedLocale := locale.FromContext(ctx)
	ctx, _ = locale.ContextWithLocale(s.ctx, selectedLocale.Locale)
	ctx = service.ExtendContext(ctx)
	service.MustRegister[deprecated.Manager](ctx, new(deprecatedManager))
	ctx, cancel := context.WithCancel(ctx)
	options, err := parseConfig(ctx, profileContent)
	if err != nil {
		cancel()
		return nil, err
	}
	if overrideOptions != nil {
		for _, inbound := range options.Inbounds {
			if tunInboundOptions, isTUN := inbound.Options.(*option.TunInboundOptions); isTUN {
				tunInboundOptions.AutoRedirect = overrideOptions.AutoRedirect
				tunInboundOptions.IncludePackage = append(tunInboundOptions.IncludePackage, overrideOptions.IncludePackage...)
				tunInboundOptions.ExcludePackage = append(tunInboundOptions.ExcludePackage, overrideOptions.ExcludePackage...)
				break
			}
		}
	}
	if s.oomKillerEnabled {
		if !common.Any(options.Services, func(it option.Service) bool {
			return it.Type == C.TypeOOMKiller
		}) {
			oomOptions := &option.OOMKillerServiceOptions{
				KillerDisabled:      s.oomKillerDisabled,
				MemoryLimitOverride: s.oomMemoryLimit,
			}
			options.Services = append(options.Services, option.Service{
				Type:    C.TypeOOMKiller,
				Options: oomOptions,
			})
		}
	}
	urlTestHistoryStorage := urltest.NewHistoryStorage()
	ctx = service.ContextWithPtr(ctx, urlTestHistoryStorage)
	i := &Instance{
		ctx:                   ctx,
		cancel:                cancel,
		urlTestHistoryStorage: urlTestHistoryStorage,
	}
	boxInstance, err := box.New(box.Options{
		Context:           ctx,
		Options:           options,
		PlatformLogWriter: s,
	})
	if err != nil {
		cancel()
		return nil, err
	}
	i.instance = boxInstance
	i.connectionManager = service.FromContext[adapter.ConnectionManager](ctx)
	i.clashServer = service.FromContext[adapter.ClashServer](ctx)
	i.trafficManager = service.PtrFromContext[trafficcontrol.Manager](ctx)
	i.pauseManager = service.FromContext[pause.Manager](ctx)
	i.cacheFile = service.FromContext[adapter.CacheFile](ctx)
	i.outboundManager = service.FromContext[adapter.OutboundManager](ctx)
	i.endpointManager = service.FromContext[adapter.EndpointManager](ctx)
	i.providerManager = service.FromContext[adapter.ProviderManager](ctx)
	i.logFactory = boxInstance.LogFactory()
	i.initializeProviderService()
	log.SetStdLogger(boxInstance.LogFactory().Logger())
	return i, nil
}

func attachInstance(ctx context.Context) *Instance {
	instance := &Instance{
		ctx:                   ctx,
		connectionManager:     service.FromContext[adapter.ConnectionManager](ctx),
		clashServer:           service.FromContext[adapter.ClashServer](ctx),
		trafficManager:        service.PtrFromContext[trafficcontrol.Manager](ctx),
		pauseManager:          service.FromContext[pause.Manager](ctx),
		cacheFile:             service.FromContext[adapter.CacheFile](ctx),
		urlTestHistoryStorage: service.PtrFromContext[urltest.HistoryStorage](ctx),
		outboundManager:       service.FromContext[adapter.OutboundManager](ctx),
		endpointManager:       service.FromContext[adapter.EndpointManager](ctx),
		providerManager:       service.FromContext[adapter.ProviderManager](ctx),
		logFactory:            service.FromContext[log.Factory](ctx),
	}
	instance.initializeProviderService()
	return instance
}

func (i *Instance) Start() error {
	return i.instance.Start()
}

func (i *Instance) Close() error {
	i.closeProviderService()
	i.cancel()
	i.urlTestHistoryStorage.Close()
	return i.instance.Close()
}

func (i *Instance) initializeProviderService() {
	i.providerInstanceID = uuid.Must(uuid.NewV4()).String()
	i.providerRevision = 1
	i.providerStateRevisions = make(map[string]uint64)
	i.providerUpdateSubscriber = observable.NewSubscriber[struct{}](1)
	i.providerUpdateObserver = observable.NewObserver(i.providerUpdateSubscriber, 1)
	i.providerCallbacks = make(map[adapter.Provider]*list.Element[adapter.ProviderUpdateCallback])
	if i.providerManager == nil {
		return
	}
	if managerObserver, isObservable := i.providerManager.(adapter.ProviderManagerObservable); isObservable {
		i.providerManagerObserver = managerObserver
		i.providerManagerCallback = managerObserver.RegisterProviderManagerCallback(func() {
			i.syncProviderCallbacks()
			i.emitProviderUpdate()
		})
	}
	i.syncProviderCallbacks()
}

func (i *Instance) syncProviderCallbacks() {
	i.providerCallbackAccess.Lock()
	defer i.providerCallbackAccess.Unlock()
	if i.providerServiceClosed {
		return
	}
	currentProviders := make(map[adapter.Provider]bool)
	for _, provider := range i.providerManager.Providers() {
		currentProviders[provider] = true
		if _, exists := i.providerCallbacks[provider]; exists {
			continue
		}
		element := provider.RegisterCallback(func(string) error {
			i.emitProviderUpdate()
			return nil
		})
		if element != nil {
			i.providerCallbacks[provider] = element
		}
	}
	for provider, element := range i.providerCallbacks {
		if currentProviders[provider] {
			continue
		}
		provider.UnregisterCallback(element)
		delete(i.providerCallbacks, provider)
	}
}

func (i *Instance) emitProviderUpdate() {
	i.providerCallbackAccess.Lock()
	closed := i.providerServiceClosed
	i.providerCallbackAccess.Unlock()
	if !closed {
		i.providerUpdateSubscriber.Emit(struct{}{})
	}
}

func (i *Instance) closeProviderService() {
	i.providerCallbackAccess.Lock()
	i.providerServiceClosed = true
	i.providerCallbackAccess.Unlock()
	if i.providerManagerObserver != nil {
		i.providerManagerObserver.UnregisterProviderManagerCallback(i.providerManagerCallback)
		i.providerManagerObserver = nil
		i.providerManagerCallback = nil
	}
	i.providerCallbackAccess.Lock()
	for provider, element := range i.providerCallbacks {
		provider.UnregisterCallback(element)
	}
	clear(i.providerCallbacks)
	i.providerCallbackAccess.Unlock()
	if i.providerUpdateSubscriber != nil {
		i.providerUpdateSubscriber.Close()
	}
}

func (i *Instance) providerStateRevision(revisions map[string]uint64) uint64 {
	i.providerStateAccess.Lock()
	defer i.providerStateAccess.Unlock()
	changed := !i.providerStateInitialized || len(revisions) != len(i.providerStateRevisions)
	if !changed {
		for tag, revision := range revisions {
			if i.providerStateRevisions[tag] != revision {
				changed = true
				break
			}
		}
	}
	if changed {
		if i.providerStateInitialized {
			i.providerRevision++
		}
		i.providerStateInitialized = true
		i.providerStateRevisions = make(map[string]uint64, len(revisions))
		for tag, revision := range revisions {
			i.providerStateRevisions[tag] = revision
		}
	}
	return i.providerRevision
}

func (i *Instance) Box() *box.Box {
	return i.instance
}

func (i *Instance) PauseManager() pause.Manager {
	return i.pauseManager
}

func (i *Instance) TrafficManager() *trafficcontrol.Manager {
	return i.trafficManager
}

func parseConfig(ctx context.Context, configContent string) (option.Options, error) {
	options, err := json.UnmarshalExtendedContext[option.Options](ctx, []byte(configContent))
	if err != nil {
		return option.Options{}, E.Cause(err, "decode config")
	}
	return options, nil
}
