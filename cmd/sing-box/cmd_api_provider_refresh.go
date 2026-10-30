package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sagernet/sing-box/daemon"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

var commandAPIProviderRefresh = &cobra.Command{
	Use:   "refresh [tag...]",
	Short: "Refresh subscription providers",
	Long:  "Refresh the specified providers, or every refreshable provider when no tags are given. Busy providers are retried every 2 seconds for up to 90 seconds each.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAPIProviderRefresh(args)
	},
}

func init() {
	commandAPIProvider.AddCommand(commandAPIProviderRefresh)
}

func runAPIProviderRefresh(tags []string) error {
	clientConn, _, err := createAPIClient()
	if err != nil {
		return err
	}
	defer clientConn.Close()
	client := daemon.NewProviderServiceClient(clientConn)
	ctx, cancel := signal.NotifyContext(globalCtx, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	list, err := client.ListProviders(ctx, &emptypb.Empty{})
	if err != nil {
		return err
	}
	providers, err := refreshAPIProviders(ctx, client, list.GetProviders(), tags, os.Stderr, 2*time.Second, 90*time.Second)
	printAPIProviders(providers)
	return err
}

type apiProviderRefreshClient interface {
	RefreshProvider(context.Context, *daemon.ProviderActionRequest, ...grpc.CallOption) (*daemon.ProviderActionResult, error)
}

func refreshAPIProviders(ctx context.Context, client apiProviderRefreshClient, listed []*daemon.Provider, tags []string, stderr io.Writer, retryInterval, waitTimeout time.Duration) ([]*daemon.Provider, error) {
	providersByTag := make(map[string]*daemon.Provider, len(listed))
	refreshAll := len(tags) == 0
	for _, provider := range listed {
		providersByTag[provider.GetTag()] = provider
		if refreshAll && provider.GetCapabilities().GetCanRefresh() {
			tags = append(tags, provider.GetTag())
		}
	}
	providers := make([]*daemon.Provider, 0, len(tags))
	var failures []string
	for _, tag := range tags {
		if err := ctx.Err(); err != nil {
			return providers, err
		}
		result, refreshErr := refreshAPIProvider(ctx, client, tag, stderr, retryInterval, waitTimeout)
		if err := ctx.Err(); err != nil {
			return providers, err
		}
		if refreshErr != nil {
			message := status.Convert(refreshErr).Message()
			if status.Code(refreshErr) == codes.ResourceExhausted {
				message = "already in progress"
			}
			failures = append(failures, tag+": "+message)
			if provider := providersByTag[tag]; provider != nil {
				providers = append(providers, provider)
			}
			continue
		}
		providers = append(providers, result.GetProvider())
	}
	if len(failures) > 0 {
		return providers, fmt.Errorf("failed to refresh providers: %s", strings.Join(failures, "; "))
	}
	return providers, nil
}

func refreshAPIProvider(ctx context.Context, client apiProviderRefreshClient, tag string, stderr io.Writer, retryInterval, waitTimeout time.Duration) (*daemon.ProviderActionResult, error) {
	request := &daemon.ProviderActionRequest{ProviderTag: tag}
	result, err := client.RefreshProvider(ctx, request)
	if status.Code(err) != codes.ResourceExhausted {
		return result, err
	}
	fmt.Fprintf(stderr, "%s: already in progress, waiting...\n", tag)
	deadline := time.Now().Add(waitTimeout)
	timer := time.NewTimer(min(retryInterval, waitTimeout))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !time.Now().Before(deadline) {
			return nil, err
		}
		result, err = client.RefreshProvider(ctx, request)
		if status.Code(err) != codes.ResourceExhausted {
			return result, err
		}
		timer.Reset(min(retryInterval, time.Until(deadline)))
	}
}
