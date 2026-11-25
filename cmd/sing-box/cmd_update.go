package main

import (
	"bytes"
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
	if err = os.Chdir(definition.dataDir); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	syncer := definition.syncer()
	changed, revision, err := syncer.pull(ctx)
	if err != nil {
		return err
	}
	if changed {
		if err = reloadManagedService(ctx, definition.name); err != nil {
			return fmt.Errorf("reload service: %w", err)
		}
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
	if err = waitConfigRevision(readyCtx, configReadyPath(definition.dataDir, definition.name), revision); err != nil {
		return fmt.Errorf("wait for service to load configuration: %w", err)
	}
	list, err := client.ListProviders(readyCtx, &emptypb.Empty{}, grpc.WaitForReady(true))
	if err != nil {
		return fmt.Errorf("wait for provider API: %w", err)
	}
	cancelReady()
	providers, err := refreshAPIProviders(ctx, client, list.GetProviders(), nil, cmd.ErrOrStderr(), 2*time.Second, 90*time.Second)
	printAPIProviders(providers)
	return err
}

func waitConfigRevision(ctx context.Context, path string, revision [32]byte) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		loaded, err := os.ReadFile(path)
		if err == nil && bytes.Equal(loaded, revision[:]) {
			return nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
