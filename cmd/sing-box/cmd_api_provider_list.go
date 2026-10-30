package main

import (
	"github.com/sagernet/sing-box/daemon"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/emptypb"
)

var commandAPIProviderList = &cobra.Command{
	Use:   "list",
	Short: "List subscription providers",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAPIProviderList()
	},
}

func init() {
	commandAPIProvider.AddCommand(commandAPIProviderList)
}

func runAPIProviderList() error {
	clientConn, _, err := createAPIClient()
	if err != nil {
		return err
	}
	defer clientConn.Close()
	client := daemon.NewProviderServiceClient(clientConn)
	providers, err := client.ListProviders(globalCtx, &emptypb.Empty{})
	if err != nil {
		return err
	}
	printAPIProviders(providers.GetProviders())
	return nil
}
