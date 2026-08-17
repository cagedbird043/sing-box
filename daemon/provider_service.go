package daemon

import (
	"context"
	"errors"
	"sort"

	"github.com/sagernet/sing-box/adapter"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const ProviderServiceProtocolVersion = 1

var _ ProviderServiceServer = (*NativeProviderService)(nil)

type NativeProviderService struct {
	UnimplementedProviderServiceServer
	startedService *StartedService
}

func NewProviderService(startedService *StartedService) *NativeProviderService {
	return &NativeProviderService{startedService: startedService}
}

func (s *NativeProviderService) GetProviderServiceInfo(context.Context, *emptypb.Empty) (*ProviderServiceInfo, error) {
	return &ProviderServiceInfo{
		ProtocolVersion: ProviderServiceProtocolVersion,
		Capabilities: []ProviderServiceCapability{
			ProviderServiceCapability_PROVIDER_SERVICE_CAPABILITY_LIST,
			ProviderServiceCapability_PROVIDER_SERVICE_CAPABILITY_SUBSCRIBE,
			ProviderServiceCapability_PROVIDER_SERVICE_CAPABILITY_REFRESH,
			ProviderServiceCapability_PROVIDER_SERVICE_CAPABILITY_HEALTH_CHECK,
		},
	}, nil
}

func (s *NativeProviderService) ListProviders(ctx context.Context, _ *emptypb.Empty) (*ProviderList, error) {
	instance, err := s.instance(ctx)
	if err != nil {
		return nil, err
	}
	return buildProviderList(instance), nil
}

func (s *NativeProviderService) GetProvider(ctx context.Context, request *GetProviderRequest) (*Provider, error) {
	if request.GetProviderTag() == "" {
		return nil, status.Error(codes.InvalidArgument, "provider tag is required")
	}
	instance, err := s.instance(ctx)
	if err != nil {
		return nil, err
	}
	list := buildProviderList(instance)
	provider := findProvider(list, request.ProviderTag)
	if provider == nil {
		return nil, status.Error(codes.NotFound, "provider not found: "+request.ProviderTag)
	}
	return provider, nil
}

func (s *NativeProviderService) SubscribeProviders(_ *emptypb.Empty, server grpc.ServerStreamingServer[ProviderList]) error {
	instance, err := s.instance(server.Context())
	if err != nil {
		return err
	}
	updates, updatesDone, err := instance.providerUpdateObserver.Subscribe()
	if err != nil {
		return providerUnavailable(err)
	}
	defer instance.providerUpdateObserver.UnSubscribe(updates)
	statuses, statusesDone, err := s.startedService.serviceStatusObserver.Subscribe()
	if err != nil {
		return providerUnavailable(err)
	}
	defer s.startedService.serviceStatusObserver.UnSubscribe(statuses)
	for {
		if err := server.Send(buildProviderList(instance)); err != nil {
			return err
		}
		select {
		case <-updates:
		case serviceStatus := <-statuses:
			if serviceStatus.Status != ServiceStatus_STARTED || s.startedService.Instance() != instance {
				return status.Error(codes.Unavailable, "provider instance changed")
			}
		case <-updatesDone:
			return status.Error(codes.Unavailable, "provider instance closed")
		case <-statusesDone:
			return status.Error(codes.Unavailable, "service status closed")
		case <-s.startedService.ctx.Done():
			return status.FromContextError(s.startedService.ctx.Err()).Err()
		case <-server.Context().Done():
			return status.FromContextError(server.Context().Err()).Err()
		}
	}
}

func (s *NativeProviderService) RefreshProvider(ctx context.Context, request *ProviderActionRequest) (*ProviderActionResult, error) {
	instance, provider, err := s.providerForAction(ctx, request)
	if err != nil {
		return nil, err
	}
	updater, supported := provider.(adapter.ProviderUpdater)
	if !supported {
		return nil, status.Error(codes.FailedPrecondition, "provider does not support refresh")
	}
	if err := updater.Update(); err != nil {
		return nil, providerActionError("refresh", err)
	}
	list := buildProviderList(instance)
	result := findProvider(list, provider.Tag())
	if result == nil {
		return nil, status.Error(codes.Unavailable, "provider disappeared after refresh")
	}
	return &ProviderActionResult{InstanceId: list.InstanceId, Revision: list.Revision, Provider: result}, nil
}

func (s *NativeProviderService) HealthCheckProvider(ctx context.Context, request *ProviderActionRequest) (*ProviderHealthCheckResult, error) {
	instance, provider, err := s.providerForAction(ctx, request)
	if err != nil {
		return nil, err
	}
	if _, err := provider.HealthCheck(ctx); err != nil {
		return nil, providerActionError("health check", err)
	}
	list := buildProviderList(instance)
	resultProvider := findProvider(list, provider.Tag())
	if resultProvider == nil {
		return nil, status.Error(codes.Unavailable, "provider disappeared after health check")
	}
	results := make([]*ProviderNodeResult, 0, len(resultProvider.Nodes))
	for _, node := range resultProvider.Nodes {
		results = append(results, &ProviderNodeResult{NodeTag: node.Tag, Health: node.Health})
	}
	return &ProviderHealthCheckResult{
		InstanceId: list.InstanceId,
		Revision:   list.Revision,
		Provider:   resultProvider,
		Results:    results,
	}, nil
}

func (s *NativeProviderService) instance(ctx context.Context) (*Instance, error) {
	if err := s.startedService.waitForStarted(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		return nil, providerUnavailable(err)
	}
	s.startedService.serviceAccess.RLock()
	defer s.startedService.serviceAccess.RUnlock()
	if s.startedService.serviceStatus.Status != ServiceStatus_STARTED || s.startedService.instance == nil {
		return nil, status.Error(codes.Unavailable, "provider instance is not started")
	}
	return s.startedService.instance, nil
}

func (s *NativeProviderService) providerForAction(ctx context.Context, request *ProviderActionRequest) (*Instance, adapter.Provider, error) {
	if request.GetProviderTag() == "" {
		return nil, nil, status.Error(codes.InvalidArgument, "provider tag is required")
	}
	instance, err := s.instance(ctx)
	if err != nil {
		return nil, nil, err
	}
	if instance.providerManager == nil {
		return nil, nil, status.Error(codes.NotFound, "provider not found: "+request.ProviderTag)
	}
	provider, exists := instance.providerManager.Get(request.ProviderTag)
	if !exists {
		return nil, nil, status.Error(codes.NotFound, "provider not found: "+request.ProviderTag)
	}
	return instance, provider, nil
}

func buildProviderList(instance *Instance) *ProviderList {
	if instance.providerManager == nil {
		return &ProviderList{InstanceId: instance.providerInstanceID, Revision: instance.providerStateRevision(nil)}
	}
	providers := instance.providerManager.Providers()
	sort.SliceStable(providers, func(left, right int) bool {
		return providers[left].Tag() < providers[right].Tag()
	})
	items := make([]*Provider, 0, len(providers))
	revisions := make(map[string]uint64, len(providers))
	for _, provider := range providers {
		snapshot := readProviderSnapshot(provider)
		revisions[provider.Tag()] = snapshot.Revision
		items = append(items, buildProvider(provider, snapshot, instance))
	}
	return &ProviderList{
		InstanceId: instance.providerInstanceID,
		Revision:   instance.providerStateRevision(revisions),
		Providers:  items,
	}
}

func readProviderSnapshot(provider adapter.Provider) adapter.ProviderSnapshot {
	if snapshotter, isSnapshotter := provider.(adapter.ProviderSnapshotter); isSnapshotter {
		return snapshotter.ProviderSnapshot()
	}
	snapshot := adapter.ProviderSnapshot{
		Outbounds: provider.Outbounds(),
		UpdatedAt: provider.UpdatedAt(),
	}
	if subscriptionProvider, hasSubscription := provider.(adapter.ProviderSubscriptionInfo); hasSubscription {
		info := subscriptionProvider.SubscriptionInfo()
		snapshot.SubscriptionInfo = &info
	}
	return snapshot
}

func buildProvider(provider adapter.Provider, snapshot adapter.ProviderSnapshot, instance *Instance) *Provider {
	item := &Provider{
		Tag:          provider.Tag(),
		Type:         provider.Type(),
		Capabilities: &ProviderCapabilities{CanHealthCheck: true},
		Nodes:        make([]*ProviderNode, 0, len(snapshot.Outbounds)),
	}
	if !snapshot.UpdatedAt.IsZero() {
		item.UpdatedAtMs = snapshot.UpdatedAt.UnixMilli()
	}
	_, item.Capabilities.CanRefresh = provider.(adapter.ProviderUpdater)
	if snapshot.SubscriptionInfo != nil {
		item.Capabilities.HasSubscriptionInfo = true
		item.Subscription = &SubscriptionInfo{
			UploadBytes:     snapshot.SubscriptionInfo.Upload,
			DownloadBytes:   snapshot.SubscriptionInfo.Download,
			TotalBytes:      snapshot.SubscriptionInfo.Total,
			ExpireAtSeconds: snapshot.SubscriptionInfo.Expire,
		}
	}
	for _, outbound := range snapshot.Outbounds {
		health := snapshot.Health[outbound.Tag()]
		if health.CheckedAt.IsZero() && instance.urlTestHistoryStorage != nil {
			if history := instance.urlTestHistoryStorage.LoadURLTestHistory(adapter.OutboundTag(outbound)); history != nil {
				health = adapter.ProviderHealth{Available: true, Delay: history.Delay, CheckedAt: history.Time}
			}
		}
		item.Nodes = append(item.Nodes, &ProviderNode{
			Tag:      outbound.Tag(),
			Type:     outbound.Type(),
			Networks: append([]string(nil), outbound.Network()...),
			Health:   buildProviderHealth(health),
		})
	}
	return item
}

func buildProviderHealth(health adapter.ProviderHealth) *ProviderNodeHealth {
	if health.CheckedAt.IsZero() {
		return &ProviderNodeHealth{State: ProviderHealthState_PROVIDER_HEALTH_STATE_UNKNOWN}
	}
	state := ProviderHealthState_PROVIDER_HEALTH_STATE_UNREACHABLE
	if health.Available {
		state = ProviderHealthState_PROVIDER_HEALTH_STATE_REACHABLE
	}
	return &ProviderNodeHealth{State: state, DelayMs: uint32(health.Delay), CheckedAtMs: health.CheckedAt.UnixMilli()}
}

func findProvider(list *ProviderList, tag string) *Provider {
	for _, provider := range list.Providers {
		if provider.Tag == tag {
			return provider
		}
	}
	return nil
}

func providerActionError(operation string, err error) error {
	if errors.Is(err, adapter.ErrProviderBusy) {
		return status.Error(codes.ResourceExhausted, "provider action already in progress")
	}
	return status.Error(codes.Unavailable, "provider "+operation+" failed")
}

func providerUnavailable(error) error {
	return status.Error(codes.Unavailable, "provider instance is unavailable")
}
