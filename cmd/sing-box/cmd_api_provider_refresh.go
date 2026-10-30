package main

import (
	"fmt"
	"strings"

	"github.com/sagernet/sing-box/daemon"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

var commandAPIProviderRefresh = &cobra.Command{
	Use:   "refresh [tag...]",
	Short: "Refresh subscription providers",
	Long:  "Refresh the specified providers, or every refreshable provider when no tags are given.",
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
	list, err := client.ListProviders(globalCtx, &emptypb.Empty{})
	if err != nil {
		return err
	}
	providersByTag := make(map[string]*daemon.Provider, len(list.GetProviders()))
	refreshAll := len(tags) == 0
	for _, provider := range list.GetProviders() {
		providersByTag[provider.GetTag()] = provider
		if refreshAll && provider.GetCapabilities().GetCanRefresh() {
			tags = append(tags, provider.GetTag())
		}
	}
	providers := make([]*daemon.Provider, 0, len(tags))
	var failures []string
	for _, tag := range tags {
		result, refreshErr := client.RefreshProvider(globalCtx, &daemon.ProviderActionRequest{ProviderTag: tag})
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
	printAPIProviders(providers)
	if len(failures) > 0 {
		return fmt.Errorf("failed to refresh providers: %s", strings.Join(failures, "; "))
	}
	return nil
}
