package adapter

import (
	"context"
	"errors"
	"time"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/x/list"
)

type Provider interface {
	Type() string
	Tag() string
	Outbounds() []Outbound
	Outbound(tag string) (Outbound, bool)
	UpdatedAt() time.Time
	HealthCheck(ctx context.Context) (map[string]uint16, error)
	RegisterCallback(callback ProviderUpdateCallback) *list.Element[ProviderUpdateCallback]
	UnregisterCallback(element *list.Element[ProviderUpdateCallback])
}

var ErrProviderBusy = errors.New("provider is busy")

type ProviderHealth struct {
	Available bool
	Delay     uint16
	CheckedAt time.Time
}

type ProviderSnapshot struct {
	Revision         uint64
	Outbounds        []Outbound
	UpdatedAt        time.Time
	SubscriptionInfo *SubscriptionInfo
	Health           map[string]ProviderHealth
}

type ProviderSnapshotter interface {
	ProviderSnapshot() ProviderSnapshot
}

type ProviderUpdater interface {
	Update() error
}

type ProviderSubscriptionInfo interface {
	SubscriptionInfo() SubscriptionInfo
}

type ProviderRegistry interface {
	option.ProviderOptionsRegistry
	CreateProvider(ctx context.Context, router Router, logFactory log.Factory, tag string, providerType string, options any) (Provider, error)
}

type ProviderManager interface {
	Lifecycle
	Providers() []Provider
	Get(tag string) (Provider, bool)
	Remove(tag string) error
	Create(ctx context.Context, router Router, logFactory log.Factory, tag string, providerType string, options any) error
}

type ProviderManagerObservable interface {
	RegisterProviderManagerCallback(callback ProviderManagerUpdateCallback) *list.Element[ProviderManagerUpdateCallback]
	UnregisterProviderManagerCallback(element *list.Element[ProviderManagerUpdateCallback])
}

type SubscriptionInfo struct {
	Upload   int64
	Download int64
	Total    int64
	Expire   int64
}

type ProviderUpdateCallback = func(tag string) error
type ProviderManagerUpdateCallback = func()
