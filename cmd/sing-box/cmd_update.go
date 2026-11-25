package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sagernet/sing-box/daemon"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

var (
	updateNameFlag string
	updateURLFlag  string
	commandUpdate  = &cobra.Command{
		Use:              "update",
		Short:            "Pull remote configuration and refresh every provider",
		PersistentPreRun: prepareServiceCommand,
		Args:             cobra.NoArgs,
		RunE:             runUpdate,
	}
)

func init() {
	mainCommand.AddCommand(commandUpdate)
	commandUpdate.Flags().StringVar(&updateNameFlag, "name", defaultManagedServiceName, "System service name")
	commandUpdate.Flags().StringVar(&updateURLFlag, "url", "", "API service URL (default: $BOX_API_URL or 127.0.0.1:9090)")
}

func runUpdate(cmd *cobra.Command, args []string) error {
	if err := requireServiceAdmin(); err != nil {
		return err
	}
	if err := validateServiceName(updateNameFlag); err != nil {
		return err
	}
	definition, running, err := lookupManagedService(updateNameFlag)
	if err != nil {
		return err
	}
	if definition == nil || !running {
		return errors.New("managed system service is not running")
	}
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	changed, err := requestServiceSync(ctx, *definition)
	if err != nil {
		return err
	}
	if changed {
		fmt.Fprintln(cmd.OutOrStdout(), "Configuration replaced; the child stayed up through the early-start observation window.")
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "Configuration unchanged.")
	}
	commandAPIFlagURL = updateURLFlag
	if commandAPIFlagURL == "" && os.Getenv("BOX_API_URL") == "" {
		commandAPIFlagURL = "127.0.0.1:9090"
	}
	conn, _, err := createAPIClient()
	if err != nil {
		return err
	}
	defer conn.Close()
	client := daemon.NewProviderServiceClient(conn)
	readyCtx, cancelReady := context.WithTimeout(ctx, 30*time.Second)
	defer cancelReady()
	list, err := client.ListProviders(readyCtx, &emptypb.Empty{}, grpc.WaitForReady(true))
	if err != nil {
		return fmt.Errorf("wait for provider API: %w", err)
	}
	cancelReady()
	providers, err := refreshAPIProviders(ctx, client, list.GetProviders(), nil, cmd.ErrOrStderr(), 2*time.Second, 90*time.Second)
	printAPIProviders(providers)
	return err
}
