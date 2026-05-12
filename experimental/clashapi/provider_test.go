package clashapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/x/list"
)

func TestProxyProviderRouterListsAndUpdatesProvider(t *testing.T) {
	provider := &fakeClashAPIProvider{
		providerType: C.ProviderTypeRemote,
		tag:          "justmysocks",
		updatedAt:    time.Unix(1700000000, 0).UTC(),
		outbounds: []adapter.Outbound{
			fakeClashAPIOutbound{tag: "justmysocks/jp", outboundType: C.TypeShadowsocks},
		},
		healthCheck: map[string]uint16{
			"justmysocks/jp": 123,
		},
	}
	server := &Server{
		urlTestHistory: urltest.NewHistoryStorage(),
		provider: &fakeClashAPIProviderManager{
			providers: map[string]adapter.Provider{
				provider.Tag(): provider,
			},
		},
	}
	router := proxyProviderRouter(server)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET providers status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var providersResponse map[string]map[string]map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &providersResponse); err != nil {
		t.Fatal(err)
	}
	providerInfo := providersResponse["providers"]["justmysocks"]
	if providerInfo["type"] != "Proxy" {
		t.Fatalf("provider type = %v", providerInfo["type"])
	}
	if providerInfo["vehicleType"] != "HTTP" {
		t.Fatalf("provider vehicleType = %v", providerInfo["vehicleType"])
	}
	if len(providerInfo["proxies"].([]any)) != 1 {
		t.Fatalf("provider proxies = %v", providerInfo["proxies"])
	}
	subscriptionInfo := providerInfo["subscriptionInfo"].(map[string]any)
	if subscriptionInfo["Upload"] != float64(1) || subscriptionInfo["Download"] != float64(2) ||
		subscriptionInfo["Total"] != float64(3) || subscriptionInfo["Expire"] != float64(4) {
		t.Fatalf("subscriptionInfo = %v", subscriptionInfo)
	}

	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/justmysocks", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("PUT provider status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if provider.updateCount != 1 {
		t.Fatalf("update count = %d", provider.updateCount)
	}

	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/justmysocks/healthcheck", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET provider healthcheck status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var health map[string]uint16
	if err := json.Unmarshal(recorder.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health["justmysocks/jp"] != 123 {
		t.Fatalf("healthcheck result = %v", health)
	}
}

type fakeClashAPIProviderManager struct {
	providers map[string]adapter.Provider
}

func (m *fakeClashAPIProviderManager) Start(adapter.StartStage) error { return nil }

func (m *fakeClashAPIProviderManager) Close() error { return nil }

func (m *fakeClashAPIProviderManager) Providers() []adapter.Provider {
	providers := make([]adapter.Provider, 0, len(m.providers))
	for _, provider := range m.providers {
		providers = append(providers, provider)
	}
	return providers
}

func (m *fakeClashAPIProviderManager) Get(tag string) (adapter.Provider, bool) {
	provider, found := m.providers[tag]
	return provider, found
}

func (m *fakeClashAPIProviderManager) Remove(string) error { return nil }

func (m *fakeClashAPIProviderManager) Create(context.Context, adapter.Router, log.Factory, string, string, any) error {
	return nil
}

type fakeClashAPIProvider struct {
	providerType string
	tag          string
	updatedAt    time.Time
	outbounds    []adapter.Outbound
	healthCheck  map[string]uint16
	updateCount  int
}

func (p *fakeClashAPIProvider) Type() string { return p.providerType }

func (p *fakeClashAPIProvider) Tag() string { return p.tag }

func (p *fakeClashAPIProvider) Outbounds() []adapter.Outbound { return p.outbounds }

func (p *fakeClashAPIProvider) Outbound(tag string) (adapter.Outbound, bool) {
	for _, outbound := range p.outbounds {
		if outbound.Tag() == tag {
			return outbound, true
		}
	}
	return nil, false
}

func (p *fakeClashAPIProvider) UpdatedAt() time.Time { return p.updatedAt }

func (p *fakeClashAPIProvider) HealthCheck(context.Context) (map[string]uint16, error) {
	return p.healthCheck, nil
}

func (p *fakeClashAPIProvider) RegisterCallback(adapter.ProviderUpdateCallback) *list.Element[adapter.ProviderUpdateCallback] {
	return nil
}

func (p *fakeClashAPIProvider) UnregisterCallback(*list.Element[adapter.ProviderUpdateCallback]) {}

func (p *fakeClashAPIProvider) Update() error {
	p.updateCount++
	return nil
}

func (p *fakeClashAPIProvider) SubscriptionInfo() adapter.SubscriptionInfo {
	return adapter.SubscriptionInfo{
		Upload:   1,
		Download: 2,
		Total:    3,
		Expire:   4,
	}
}

type fakeClashAPIOutbound struct {
	tag          string
	outboundType string
}

func (o fakeClashAPIOutbound) Type() string { return o.outboundType }

func (o fakeClashAPIOutbound) Tag() string { return o.tag }

func (o fakeClashAPIOutbound) Network() []string { return []string{N.NetworkTCP, N.NetworkUDP} }

func (o fakeClashAPIOutbound) Dependencies() []string { return nil }

func (o fakeClashAPIOutbound) DialContext(context.Context, string, metadata.Socksaddr) (net.Conn, error) {
	return nil, nil
}

func (o fakeClashAPIOutbound) ListenPacket(context.Context, metadata.Socksaddr) (net.PacketConn, error) {
	return nil, nil
}
