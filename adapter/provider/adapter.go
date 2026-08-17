package provider

import (
	"context"
	"reflect"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/batch"
	E "github.com/sagernet/sing/common/exceptions"
	F "github.com/sagernet/sing/common/format"
	"github.com/sagernet/sing/common/x/list"
	"github.com/sagernet/sing/service"
)

type Adapter struct {
	ctx          context.Context
	outbound     adapter.OutboundManager
	endpoint     adapter.EndpointManager
	router       adapter.Router
	logFactory   log.Factory
	logger       log.ContextLogger
	providerType string
	providerTag  string

	stateAccess      sync.RWMutex
	stateRevision    uint64
	outbounds        []adapter.Outbound
	outboundsByTag   map[string]adapter.Outbound
	updatedAt        time.Time
	subscriptionInfo *adapter.SubscriptionInfo
	health           map[string]adapter.ProviderHealth

	actionAccess sync.Mutex
	tickerAccess sync.Mutex
	ticker       *time.Ticker
	history      *urltest.HistoryStorage

	callbackAccess   sync.Mutex
	callbacks        list.List[adapter.ProviderUpdateCallback]
	callbackElements map[*list.Element[adapter.ProviderUpdateCallback]]struct{}
	callbackWait     sync.WaitGroup
	closed           bool

	link     string
	enabled  bool
	timeout  time.Duration
	interval time.Duration
}

func NewAdapter(ctx context.Context, router adapter.Router, outbound adapter.OutboundManager, endpoint adapter.EndpointManager, logFactory log.Factory, logger log.ContextLogger, providerTag string, providerType string, options option.ProviderHealthCheckOptions) Adapter {
	timeout := time.Duration(options.Timeout)
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	interval := time.Duration(options.Interval)
	if interval == 0 {
		interval = 10 * time.Minute
	}
	if interval < time.Minute {
		interval = time.Minute
	}
	return Adapter{
		ctx:              ctx,
		outbound:         outbound,
		endpoint:         endpoint,
		router:           router,
		logFactory:       logFactory,
		logger:           logger,
		providerType:     providerType,
		providerTag:      providerTag,
		callbackElements: make(map[*list.Element[adapter.ProviderUpdateCallback]]struct{}),

		enabled:  options.Enabled,
		link:     options.URL,
		timeout:  timeout,
		interval: interval,
	}
}

func (a *Adapter) Start() error {
	a.history = service.PtrFromContext[urltest.HistoryStorage](a.ctx)
	if a.history == nil {
		a.history = urltest.NewHistoryStorage()
	}
	go a.loopCheck()
	return nil
}

func (a *Adapter) Type() string {
	return a.providerType
}

func (a *Adapter) Tag() string {
	return a.providerTag
}

func (a *Adapter) Outbounds() []adapter.Outbound {
	a.stateAccess.RLock()
	defer a.stateAccess.RUnlock()
	return append([]adapter.Outbound(nil), a.outbounds...)
}

func (a *Adapter) Outbound(tag string) (adapter.Outbound, bool) {
	a.stateAccess.RLock()
	defer a.stateAccess.RUnlock()
	detour, ok := a.outboundsByTag[tag]
	return detour, ok
}

func (a *Adapter) UpdatedAt() time.Time {
	a.stateAccess.RLock()
	defer a.stateAccess.RUnlock()
	return a.updatedAt
}

func (a *Adapter) ProviderSnapshot() adapter.ProviderSnapshot {
	a.stateAccess.RLock()
	defer a.stateAccess.RUnlock()
	snapshot := adapter.ProviderSnapshot{
		Revision:  a.stateRevision,
		Outbounds: append([]adapter.Outbound(nil), a.outbounds...),
		UpdatedAt: a.updatedAt,
		Health:    make(map[string]adapter.ProviderHealth, len(a.health)),
	}
	if a.subscriptionInfo != nil {
		subscriptionInfo := *a.subscriptionInfo
		snapshot.SubscriptionInfo = &subscriptionInfo
	}
	for tag, health := range a.health {
		snapshot.Health[tag] = health
	}
	return snapshot
}

func (a *Adapter) resolveOutboundTags(newOpts []option.Outbound) []string {
	tags := make([]string, len(newOpts))
	seen := make(map[string]bool)
	for i, opt := range newOpts {
		var baseTag string
		if opt.Tag != "" {
			baseTag = F.ToString(a.providerTag, "/", opt.Tag)
		} else {
			baseTag = F.ToString(a.providerTag, "/", i)
		}
		tag := baseTag
		for n := 2; seen[tag]; n++ {
			tag = F.ToString(baseTag, " (", n, ")")
		}
		if tag != baseTag {
			a.logger.Warn("duplicate outbound tag ", baseTag, " in provider, renamed to ", tag)
		}
		seen[tag] = true
		tags[i] = tag
	}
	return tags
}

func (a *Adapter) UpdateProvider(oldOutbounds []option.Outbound, newOutbounds []option.Outbound, oldEndpoints []option.Endpoint, newEndpoints []option.Endpoint, updatedAt time.Time, subscriptionInfo *adapter.SubscriptionInfo) {
	newOutboundTags := a.resolveOutboundTags(newOutbounds)
	newEndpointTags := a.resolveEndpointTags(newEndpoints)
	newTags := make(map[string]bool, len(newOutboundTags)+len(newEndpointTags))
	for _, tag := range newOutboundTags {
		newTags[tag] = true
	}
	for _, tag := range newEndpointTags {
		newTags[tag] = true
	}
	for _, outbound := range a.Outbounds() {
		if newTags[outbound.Tag()] {
			continue
		}
		if _, isEndpoint := a.endpoint.Get(outbound.Tag()); isEndpoint {
			if err := a.endpoint.Remove(outbound.Tag()); err != nil {
				a.logger.Error(err, "close endpoint [", outbound.Tag(), "]")
			}
		} else if err := a.outbound.Remove(outbound.Tag()); err != nil {
			a.logger.Error(err, "close outbound [", outbound.Tag(), "]")
		}
	}

	oldOutboundByTag := make(map[string]option.Outbound, len(oldOutbounds))
	for _, outbound := range oldOutbounds {
		oldOutboundByTag[outbound.Tag] = outbound
	}
	outbounds := make([]adapter.Outbound, 0, len(newOutbounds)+len(newEndpoints))
	outboundsByTag := make(map[string]adapter.Outbound, len(newOutbounds)+len(newEndpoints))
	for index, outboundOptions := range newOutbounds {
		tag := newOutboundTags[index]
		outbound, exists := a.outbound.Outbound(tag)
		if !exists || !reflect.DeepEqual(outboundOptions, oldOutboundByTag[outboundOptions.Tag]) {
			err := a.outbound.Create(
				adapter.WithContext(a.ctx, &adapter.InboundContext{Outbound: tag}),
				a.router,
				a.logFactory.NewLogger(F.ToString("outbound/", outboundOptions.Type, "[", tag, "]")),
				tag,
				outboundOptions.Type,
				outboundOptions.Options,
			)
			if err != nil {
				a.logger.Warn(err, " in ", tag, ", skip create this outbound")
				continue
			}
			outbound, _ = a.outbound.Outbound(tag)
		}
		outbounds = append(outbounds, outbound)
		outboundsByTag[tag] = outbound
	}

	oldEndpointByTag := make(map[string]option.Endpoint, len(oldEndpoints))
	for _, endpointOptions := range oldEndpoints {
		oldEndpointByTag[endpointOptions.Tag] = endpointOptions
	}
	for index, endpointOptions := range newEndpoints {
		tag := newEndpointTags[index]
		endpoint, exists := a.endpoint.Get(tag)
		if !exists || !reflect.DeepEqual(endpointOptions, oldEndpointByTag[endpointOptions.Tag]) {
			err := a.endpoint.Create(
				adapter.WithContext(a.ctx, &adapter.InboundContext{Outbound: tag}),
				a.router,
				a.logFactory.NewLogger(F.ToString("endpoint/", endpointOptions.Type, "[", tag, "]")),
				tag,
				endpointOptions.Type,
				endpointOptions.Options,
			)
			if err != nil {
				a.logger.Warn(err, " in ", tag, ", skip create this endpoint")
				continue
			}
			endpoint, _ = a.endpoint.Get(tag)
		}
		outbounds = append(outbounds, endpoint)
		outboundsByTag[tag] = endpoint
	}

	a.stateAccess.Lock()
	health := make(map[string]adapter.ProviderHealth, len(outbounds))
	for _, outbound := range outbounds {
		if item, exists := a.health[outbound.Tag()]; exists {
			health[outbound.Tag()] = item
		}
	}
	a.stateRevision++
	a.outbounds = outbounds
	a.outboundsByTag = outboundsByTag
	a.updatedAt = updatedAt
	a.health = health
	if subscriptionInfo == nil {
		a.subscriptionInfo = nil
	} else {
		infoCopy := *subscriptionInfo
		a.subscriptionInfo = &infoCopy
	}
	a.stateAccess.Unlock()
}

func (a *Adapter) UpdateMetadata(updatedAt time.Time, subscriptionInfo *adapter.SubscriptionInfo) {
	a.stateAccess.Lock()
	a.stateRevision++
	a.updatedAt = updatedAt
	if subscriptionInfo == nil {
		a.subscriptionInfo = nil
	} else {
		infoCopy := *subscriptionInfo
		a.subscriptionInfo = &infoCopy
	}
	a.stateAccess.Unlock()
}

func (a *Adapter) TryStartUpdate() error {
	if !a.actionAccess.TryLock() {
		return adapter.ErrProviderBusy
	}
	return nil
}

func (a *Adapter) StartUpdate() {
	a.actionAccess.Lock()
}

func (a *Adapter) FinishUpdate() {
	a.actionAccess.Unlock()
	if a.enabled && a.history != nil {
		go func() {
			_, _ = a.HealthCheck(a.ctx)
		}()
	}
}

func (a *Adapter) HealthCheck(ctx context.Context) (map[string]uint16, error) {
	if err := a.TryStartUpdate(); err != nil {
		return nil, err
	}
	defer a.actionAccess.Unlock()
	a.tickerAccess.Lock()
	if a.ticker != nil {
		a.ticker.Reset(a.interval)
	}
	a.tickerAccess.Unlock()
	result := make(map[string]uint16)
	health := make(map[string]adapter.ProviderHealth)
	b, _ := batch.New(ctx, batch.WithConcurrencyNum[any](10))
	var resultAccess sync.Mutex
	checked := make(map[string]bool)
	for _, detour := range a.Outbounds() {
		tag := detour.Tag()
		if checked[tag] {
			continue
		}
		checked[tag] = true
		b.Go(tag, func() (any, error) {
			checkContext, cancel := context.WithTimeout(ctx, a.timeout)
			defer cancel()
			delay, err := urltest.URLTest(checkContext, a.link, detour)
			checkedAt := time.Now()
			item := adapter.ProviderHealth{CheckedAt: checkedAt}
			if err != nil {
				a.logger.Debug("outbound ", tag, " unavailable: ", err)
				a.history.DeleteURLTestHistory(tag)
			} else {
				a.logger.Debug("outbound ", tag, " available: ", delay, "ms")
				a.history.StoreURLTestHistory(tag, &adapter.URLTestHistory{Time: checkedAt, Delay: delay})
				item.Available = true
				item.Delay = delay
			}
			resultAccess.Lock()
			health[tag] = item
			if item.Available {
				result[tag] = delay
			}
			resultAccess.Unlock()
			return nil, nil
		})
	}
	b.Wait()
	a.stateAccess.Lock()
	a.stateRevision++
	a.health = health
	a.stateAccess.Unlock()
	a.UpdateGroups()
	return result, nil
}

func (a *Adapter) RegisterCallback(callback adapter.ProviderUpdateCallback) *list.Element[adapter.ProviderUpdateCallback] {
	a.callbackAccess.Lock()
	defer a.callbackAccess.Unlock()
	if a.closed {
		return nil
	}
	element := a.callbacks.PushBack(callback)
	a.callbackElements[element] = struct{}{}
	return element
}

func (a *Adapter) UnregisterCallback(element *list.Element[adapter.ProviderUpdateCallback]) {
	if element == nil {
		return
	}
	a.callbackAccess.Lock()
	defer a.callbackAccess.Unlock()
	if _, exists := a.callbackElements[element]; !exists {
		return
	}
	delete(a.callbackElements, element)
	a.callbacks.Remove(element)
}

func (a *Adapter) UpdateGroups() {
	a.callbackAccess.Lock()
	if a.closed {
		a.callbackAccess.Unlock()
		return
	}
	callbacks := make([]adapter.ProviderUpdateCallback, 0, len(a.callbackElements))
	for element := a.callbacks.Front(); element != nil; element = element.Next() {
		callbacks = append(callbacks, element.Value)
	}
	a.callbackWait.Add(1)
	a.callbackAccess.Unlock()
	defer a.callbackWait.Done()
	for _, callback := range callbacks {
		if err := callback(a.providerTag); err != nil {
			a.logger.Error("update provider group: ", err)
		}
	}
}

func (a *Adapter) Close() error {
	a.actionAccess.Lock()
	defer a.actionAccess.Unlock()
	a.tickerAccess.Lock()
	if a.ticker != nil {
		a.ticker.Stop()
	}
	a.tickerAccess.Unlock()
	a.callbackAccess.Lock()
	a.closed = true
	a.callbacks = list.List[adapter.ProviderUpdateCallback]{}
	clear(a.callbackElements)
	a.callbackAccess.Unlock()
	a.callbackWait.Wait()
	a.stateAccess.Lock()
	outbounds := a.outbounds
	a.outbounds = nil
	a.outboundsByTag = nil
	a.stateAccess.Unlock()
	var err error
	for _, outbound := range outbounds {
		if _, isEndpoint := a.endpoint.Get(outbound.Tag()); isEndpoint {
			if closeErr := a.endpoint.Remove(outbound.Tag()); closeErr != nil {
				err = E.Append(err, closeErr, func(err error) error {
					return E.Cause(err, "close endpoint [", outbound.Tag(), "]")
				})
			}
		} else if closeErr := a.outbound.Remove(outbound.Tag()); closeErr != nil {
			err = E.Append(err, closeErr, func(err error) error {
				return E.Cause(err, "close outbound [", outbound.Tag(), "]")
			})
		}
	}
	return err
}

func (a *Adapter) loopCheck() {
	if !a.enabled {
		return
	}
	a.tickerAccess.Lock()
	a.ticker = time.NewTicker(a.interval)
	ticker := a.ticker
	a.tickerAccess.Unlock()
	_, _ = a.HealthCheck(a.ctx)
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			_, _ = a.HealthCheck(a.ctx)
		}
	}
}

func (a *Adapter) RewriteDetourForProvider(opts []option.Outbound) {
	tagMapping := make(map[string]string)
	for _, opt := range opts {
		if opt.Tag != "" {
			tagMapping[opt.Tag] = F.ToString(a.providerTag, "/", opt.Tag)
		}
	}
	for _, opt := range opts {
		if dialerWrapper, ok := opt.Options.(option.DialerOptionsWrapper); ok {
			dialerOptions := dialerWrapper.TakeDialerOptions()
			if newDetour, found := tagMapping[dialerOptions.Detour]; found {
				dialerOptions.Detour = newDetour
				dialerWrapper.ReplaceDialerOptions(dialerOptions)
			}
		}
	}
}

func (a *Adapter) RewriteDetourForProviderEndpoints(opts []option.Endpoint) {
	tagMapping := make(map[string]string)
	for _, opt := range opts {
		if opt.Tag != "" {
			tagMapping[opt.Tag] = F.ToString(a.providerTag, "/", opt.Tag)
		}
	}
	for _, opt := range opts {
		if dialerWrapper, ok := opt.Options.(option.DialerOptionsWrapper); ok {
			dialerOptions := dialerWrapper.TakeDialerOptions()
			if newDetour, found := tagMapping[dialerOptions.Detour]; found {
				dialerOptions.Detour = newDetour
				dialerWrapper.ReplaceDialerOptions(dialerOptions)
			}
		}
	}
}

func (a *Adapter) resolveEndpointTags(newOpts []option.Endpoint) []string {
	tags := make([]string, len(newOpts))
	seen := make(map[string]bool)
	for i, opt := range newOpts {
		var baseTag string
		if opt.Tag != "" {
			baseTag = F.ToString(a.providerTag, "/", opt.Tag)
		} else {
			baseTag = F.ToString(a.providerTag, "/endpoint-", i)
		}
		tag := baseTag
		for n := 2; seen[tag]; n++ {
			tag = F.ToString(baseTag, " (", n, ")")
		}
		if tag != baseTag {
			a.logger.Warn("duplicate endpoint tag ", baseTag, " in provider, renamed to ", tag)
		}
		seen[tag] = true
		tags[i] = tag
	}
	return tags
}
