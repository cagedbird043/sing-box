package daemon

import (
	"github.com/gofrs/uuid/v5"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common/observable"
	"github.com/sagernet/sing/common/x/list"
)

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
	if i.providerServiceClosed {
		i.providerCallbackAccess.Unlock()
		return
	}
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
