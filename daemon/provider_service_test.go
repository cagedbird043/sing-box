package daemon

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/common/x/list"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

type providerServiceTestOutbound struct {
	adapter.Outbound
	tag      string
	protocol string
	networks []string
}

func (o *providerServiceTestOutbound) Tag() string       { return o.tag }
func (o *providerServiceTestOutbound) Type() string      { return o.protocol }
func (o *providerServiceTestOutbound) Network() []string { return o.networks }

type providerServiceTestProvider struct {
	access       sync.Mutex
	tag          string
	providerType string
	snapshot     adapter.ProviderSnapshot
	callbacks    list.List[adapter.ProviderUpdateCallback]
	updateErr    error
	healthErr    error
}

func (p *providerServiceTestProvider) Type() string { return p.providerType }
func (p *providerServiceTestProvider) Tag() string  { return p.tag }
func (p *providerServiceTestProvider) Outbounds() []adapter.Outbound {
	return p.ProviderSnapshot().Outbounds
}
func (p *providerServiceTestProvider) Outbound(tag string) (adapter.Outbound, bool) {
	for _, outbound := range p.ProviderSnapshot().Outbounds {
		if outbound.Tag() == tag {
			return outbound, true
		}
	}
	return nil, false
}
func (p *providerServiceTestProvider) UpdatedAt() time.Time {
	return p.ProviderSnapshot().UpdatedAt
}
func (p *providerServiceTestProvider) ProviderSnapshot() adapter.ProviderSnapshot {
	p.access.Lock()
	defer p.access.Unlock()
	snapshot := p.snapshot
	snapshot.Outbounds = append([]adapter.Outbound(nil), p.snapshot.Outbounds...)
	snapshot.Health = make(map[string]adapter.ProviderHealth, len(p.snapshot.Health))
	for tag, health := range p.snapshot.Health {
		snapshot.Health[tag] = health
	}
	if p.snapshot.SubscriptionInfo != nil {
		info := *p.snapshot.SubscriptionInfo
		snapshot.SubscriptionInfo = &info
	}
	return snapshot
}
func (p *providerServiceTestProvider) HealthCheck(context.Context) (map[string]uint16, error) {
	if p.healthErr != nil {
		return nil, p.healthErr
	}
	checkedAt := time.Unix(300, 0)
	p.access.Lock()
	p.snapshot.Revision++
	p.snapshot.Health = map[string]adapter.ProviderHealth{
		p.snapshot.Outbounds[0].Tag(): {Available: true, Delay: 25, CheckedAt: checkedAt},
	}
	p.access.Unlock()
	p.emit()
	return map[string]uint16{p.snapshot.Outbounds[0].Tag(): 25}, nil
}
func (p *providerServiceTestProvider) RegisterCallback(callback adapter.ProviderUpdateCallback) *list.Element[adapter.ProviderUpdateCallback] {
	p.access.Lock()
	defer p.access.Unlock()
	return p.callbacks.PushBack(callback)
}
func (p *providerServiceTestProvider) UnregisterCallback(element *list.Element[adapter.ProviderUpdateCallback]) {
	p.access.Lock()
	defer p.access.Unlock()
	p.callbacks.Remove(element)
}
func (p *providerServiceTestProvider) emit() {
	p.access.Lock()
	callbacks := make([]adapter.ProviderUpdateCallback, 0, p.callbacks.Len())
	for element := p.callbacks.Front(); element != nil; element = element.Next() {
		callbacks = append(callbacks, element.Value)
	}
	p.access.Unlock()
	for _, callback := range callbacks {
		_ = callback(p.tag)
	}
}

type providerServiceTestUpdatableProvider struct {
	*providerServiceTestProvider
}

func (p *providerServiceTestUpdatableProvider) Update() error {
	if p.updateErr != nil {
		return p.updateErr
	}
	p.access.Lock()
	p.snapshot.Revision++
	p.snapshot.UpdatedAt = time.Unix(200, 0)
	p.access.Unlock()
	p.emit()
	return nil
}

type providerServiceTestManager struct {
	access    sync.Mutex
	providers []adapter.Provider
	callbacks list.List[adapter.ProviderManagerUpdateCallback]
}

func (m *providerServiceTestManager) Start(adapter.StartStage) error { return nil }
func (m *providerServiceTestManager) Close() error                   { return nil }
func (m *providerServiceTestManager) Providers() []adapter.Provider {
	m.access.Lock()
	defer m.access.Unlock()
	return append([]adapter.Provider(nil), m.providers...)
}
func (m *providerServiceTestManager) Get(tag string) (adapter.Provider, bool) {
	m.access.Lock()
	defer m.access.Unlock()
	for _, provider := range m.providers {
		if provider.Tag() == tag {
			return provider, true
		}
	}
	return nil, false
}
func (m *providerServiceTestManager) Remove(string) error { return errors.New("unsupported") }
func (m *providerServiceTestManager) Create(context.Context, adapter.Router, log.Factory, string, string, any) error {
	return errors.New("unsupported")
}
func (m *providerServiceTestManager) RegisterProviderManagerCallback(callback adapter.ProviderManagerUpdateCallback) *list.Element[adapter.ProviderManagerUpdateCallback] {
	m.access.Lock()
	defer m.access.Unlock()
	return m.callbacks.PushBack(callback)
}
func (m *providerServiceTestManager) UnregisterProviderManagerCallback(element *list.Element[adapter.ProviderManagerUpdateCallback]) {
	m.access.Lock()
	defer m.access.Unlock()
	m.callbacks.Remove(element)
}
func (m *providerServiceTestManager) replace(providers []adapter.Provider) {
	m.access.Lock()
	m.providers = append([]adapter.Provider(nil), providers...)
	callbacks := make([]adapter.ProviderManagerUpdateCallback, 0)
	for element := m.callbacks.Front(); element != nil; element = element.Next() {
		callbacks = append(callbacks, element.Value)
	}
	m.access.Unlock()
	for _, callback := range callbacks {
		callback()
	}
}

func newProviderServiceTest(t *testing.T) (*StartedService, *Instance, *providerServiceTestUpdatableProvider) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	started := NewStartedService(ServiceOptions{Context: ctx})
	base := &providerServiceTestProvider{
		tag:          "sub",
		providerType: "remote",
		snapshot: adapter.ProviderSnapshot{
			Revision:         1,
			UpdatedAt:        time.Unix(100, 0),
			SubscriptionInfo: &adapter.SubscriptionInfo{Upload: 1, Download: 2, Total: 3, Expire: 4},
			Outbounds: []adapter.Outbound{
				&providerServiceTestOutbound{tag: "sub/node", protocol: "shadowsocks", networks: []string{"tcp", "udp"}},
			},
		},
	}
	provider := &providerServiceTestUpdatableProvider{providerServiceTestProvider: base}
	instance := &Instance{
		providerManager:       &providerServiceTestManager{providers: []adapter.Provider{provider}},
		urlTestHistoryStorage: urltest.NewHistoryStorage(),
	}
	instance.initializeProviderService()
	started.serviceAccess.Lock()
	started.instance = instance
	started.serviceStatus = &ServiceStatus{Status: ServiceStatus_STARTED}
	started.serviceAccess.Unlock()
	t.Cleanup(func() {
		instance.closeProviderService()
		instance.urlTestHistoryStorage.Close()
		started.Close()
		cancel()
	})
	return started, instance, provider
}

func TestProviderServiceListActionsAndRedaction(t *testing.T) {
	started, instance, provider := newProviderServiceTest(t)
	service := NewProviderService(started)
	list, err := service.ListProviders(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if list.Revision != 1 || len(list.Providers) != 1 || list.Providers[0].Subscription.UploadBytes != 1 {
		t.Fatalf("unexpected initial provider list: %+v", list)
	}
	if list.Providers[0].Nodes[0].Health.State != ProviderHealthState_PROVIDER_HEALTH_STATE_UNKNOWN {
		t.Fatal("expected unknown initial health")
	}

	refresh, err := service.RefreshProvider(context.Background(), &ProviderActionRequest{ProviderTag: "sub"})
	if err != nil {
		t.Fatal(err)
	}
	if refresh.Revision != 2 || refresh.Provider.UpdatedAtMs != time.Unix(200, 0).UnixMilli() {
		t.Fatalf("refresh did not return committed state: %+v", refresh)
	}
	health, err := service.HealthCheckProvider(context.Background(), &ProviderActionRequest{ProviderTag: "sub"})
	if err != nil {
		t.Fatal(err)
	}
	if health.Revision != 3 || len(health.Results) != 1 || health.Results[0].Health.DelayMs != 25 {
		t.Fatalf("health check did not return committed state: %+v", health)
	}

	provider.updateErr = errors.New("download https://user:secret@example.invalid/private?token=secret failed")
	_, err = service.RefreshProvider(context.Background(), &ProviderActionRequest{ProviderTag: "sub"})
	if status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "example.invalid") {
		t.Fatalf("provider error was not sanitized: %v", err)
	}
	provider.updateErr = adapter.ErrProviderBusy
	_, err = service.RefreshProvider(context.Background(), &ProviderActionRequest{ProviderTag: "sub"})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("unexpected busy status: %v", err)
	}
	_, err = service.GetProvider(context.Background(), &GetProviderRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unexpected empty-tag status: %v", err)
	}
	_, err = service.GetProvider(context.Background(), &GetProviderRequest{ProviderTag: "missing"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("unexpected missing-provider status: %v", err)
	}
	provider.updateErr = nil
	instance.providerManager = &providerServiceTestManager{providers: []adapter.Provider{provider.providerServiceTestProvider}}
	_, err = service.RefreshProvider(context.Background(), &ProviderActionRequest{ProviderTag: "sub"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unexpected unsupported-refresh status: %v", err)
	}
}

func TestProviderServiceAuthenticationAndSubscription(t *testing.T) {
	started, instance, _ := newProviderServiceTest(t)
	listener := bufconn.Listen(1024 * 1024)
	server := NewServer(started, "test-secret")
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	connection, err := grpc.NewClient(
		"passthrough:///provider-test",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	client := NewProviderServiceClient(connection)
	if _, err = client.GetProviderServiceInfo(context.Background(), &emptypb.Empty{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected unauthenticated probe, got %v", err)
	}
	if _, err = client.RefreshProvider(context.Background(), &ProviderActionRequest{ProviderTag: "sub"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected unauthenticated action, got %v", err)
	}
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer test-secret")
	info, err := client.GetProviderServiceInfo(ctx, &emptypb.Empty{})
	if err != nil || info.ProtocolVersion != ProviderServiceProtocolVersion {
		t.Fatalf("authenticated probe failed: %+v, %v", info, err)
	}
	healthStatus, err := grpc_health_v1.NewHealthClient(connection).Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: ProviderService_ServiceDesc.ServiceName})
	if err != nil || healthStatus.Status != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("provider health registration failed: %+v, %v", healthStatus, err)
	}
	stream, err := client.SubscribeProviders(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := stream.Recv()
	if err != nil || initial.Revision != 1 {
		t.Fatalf("unexpected initial snapshot: %+v, %v", initial, err)
	}
	refresh, err := client.RefreshProvider(ctx, &ProviderActionRequest{ProviderTag: "sub"})
	if err != nil || refresh.Revision != 2 {
		t.Fatalf("authenticated action failed: %+v, %v", refresh, err)
	}
	updated, err := stream.Recv()
	if err != nil || updated.Revision != 2 || updated.Providers[0].UpdatedAtMs != time.Unix(200, 0).UnixMilli() {
		t.Fatalf("unexpected updated snapshot: %+v, %v", updated, err)
	}
	manager := instance.providerManager.(*providerServiceTestManager)
	manager.replace(nil)
	removed, err := stream.Recv()
	if err != nil || removed.Revision != 3 || len(removed.Providers) != 0 {
		t.Fatalf("unexpected provider removal snapshot: %+v, %v", removed, err)
	}
	stopped := &ServiceStatus{Status: ServiceStatus_STOPPING}
	started.serviceAccess.Lock()
	started.serviceStatus = stopped
	started.serviceAccess.Unlock()
	started.serviceStatusSubscriber.Emit(stopped)
	if _, err = stream.Recv(); status.Code(err) != codes.Unavailable {
		t.Fatalf("expected stream shutdown status, got %v", err)
	}
	startedStatus := &ServiceStatus{Status: ServiceStatus_STARTED}
	started.serviceAccess.Lock()
	started.serviceStatus = startedStatus
	started.serviceAccess.Unlock()
	started.serviceStatusSubscriber.Emit(startedStatus)
	reconnected, err := client.SubscribeProviders(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	reconnectedSnapshot, err := reconnected.Recv()
	if err != nil || reconnectedSnapshot.InstanceId != removed.InstanceId || reconnectedSnapshot.Revision != removed.Revision {
		t.Fatalf("unexpected reconnect snapshot: %+v, %v", reconnectedSnapshot, err)
	}
}
