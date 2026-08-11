//go:build darwin

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/sagernet/sing-box/common/darwindns"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-tun"
	"github.com/spf13/cobra"
)

const darwinDNSReadyTimeout = 10 * time.Second

var (
	commandDarwinDNSServers []string
	commandDarwinDNSState   string
)

var commandDarwinDNSSupervisor = &cobra.Command{
	Use:    "darwin-dns-supervisor",
	Args:   cobra.NoArgs,
	Hidden: true,
	Run: func(cmd *cobra.Command, args []string) {
		ready := os.NewFile(3, "darwin-dns-ready")
		if ready == nil {
			log.Fatal("missing Darwin DNS readiness pipe")
		}
		defer ready.Close()
		supervisor, err := darwindns.NewSupervisor(commandDarwinDNSServers, commandDarwinDNSState)
		if err != nil {
			log.Fatal(err)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := supervisor.Run(ctx, os.Stdin, ready); err != nil {
			log.Fatal(err)
		}
	},
}

func init() {
	commandDarwinDNSSupervisor.Flags().StringSliceVar(&commandDarwinDNSServers, "server", nil, "Set TUN DNS server")
	commandDarwinDNSSupervisor.Flags().StringVar(&commandDarwinDNSState, "state", darwindns.DefaultStatePath, "Set recovery state path")
	mainCommand.AddCommand(commandDarwinDNSSupervisor)
}

type darwinDNSController struct {
	owner   *os.File
	command *exec.Cmd
	wait    chan error
}

func (c *darwinDNSController) Close() error {
	if c == nil {
		return nil
	}
	if c.owner != nil {
		_ = c.owner.Close()
	}
	select {
	case err := <-c.wait:
		return err
	case <-time.After(darwinDNSReadyTimeout):
		_ = c.command.Process.Kill()
		return fmt.Errorf("Darwin DNS supervisor did not exit")
	}
}

func startPlatformDNS(options option.Options) (io.Closer, error) {
	servers, err := platformDNSServers(options)
	if err != nil || len(servers) == 0 {
		return nil, err
	}

	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate sing-box executable: %w", err)
	}
	ownerReader, ownerWriter, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create Darwin DNS ownership pipe: %w", err)
	}
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		_ = ownerReader.Close()
		_ = ownerWriter.Close()
		return nil, fmt.Errorf("create Darwin DNS readiness pipe: %w", err)
	}

	arguments := []string{commandDarwinDNSSupervisor.Use, "--state", darwindns.DefaultStatePath}
	for _, server := range servers {
		arguments = append(arguments, "--server", server)
	}
	command := exec.Command(executable, arguments...)
	command.Stdin = ownerReader
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.ExtraFiles = []*os.File{readyWriter}
	if err := command.Start(); err != nil {
		_ = ownerReader.Close()
		_ = ownerWriter.Close()
		_ = readyReader.Close()
		_ = readyWriter.Close()
		return nil, fmt.Errorf("start Darwin DNS supervisor: %w", err)
	}
	_ = ownerReader.Close()
	_ = readyWriter.Close()

	controller := &darwinDNSController{owner: ownerWriter, command: command, wait: make(chan error, 1)}
	go func() { controller.wait <- command.Wait() }()
	readyResult := make(chan error, 1)
	go func() {
		line, readErr := bufio.NewReader(readyReader).ReadString('\n')
		_ = readyReader.Close()
		if readErr != nil {
			readyResult <- readErr
			return
		}
		if line != "ready\n" {
			readyResult <- fmt.Errorf("unexpected Darwin DNS readiness response %q", line)
			return
		}
		readyResult <- nil
	}()

	select {
	case err := <-readyResult:
		if err != nil {
			_ = controller.Close()
			return nil, fmt.Errorf("start Darwin DNS supervisor: %w", err)
		}
		return controller, nil
	case err := <-controller.wait:
		_ = ownerWriter.Close()
		return nil, fmt.Errorf("Darwin DNS supervisor exited before ready: %w", err)
	case <-time.After(darwinDNSReadyTimeout):
		_ = controller.Close()
		return nil, fmt.Errorf("Darwin DNS supervisor readiness timed out")
	}
}

func platformDNSServers(options option.Options) ([]string, error) {
	var servers []string
	for _, inbound := range options.Inbounds {
		if inbound.Type != "tun" {
			continue
		}
		tunOptions, loaded := inbound.Options.(*option.TunInboundOptions)
		if !loaded || !tunOptions.AutoRoute || tunOptions.DNSMode == tun.DNSModeDisabled {
			continue
		}
		var inet4Address, inet6Address []netip.Prefix
		for _, prefix := range tunOptions.Address {
			if prefix.Addr().Is4() {
				inet4Address = append(inet4Address, prefix)
			} else {
				inet6Address = append(inet6Address, prefix)
			}
		}
		derived := tun.Options{
			Inet4Address: inet4Address,
			Inet6Address: inet6Address,
			DNSMode:      tunOptions.DNSMode,
			DNSAddress:   tunOptions.DNSAddress,
		}
		addresses, err := derived.DNSServerAddress()
		if err != nil {
			return nil, fmt.Errorf("derive Darwin TUN DNS servers: %w", err)
		}
		for _, address := range addresses {
			server := address.String()
			if !slices.Contains(servers, server) {
				servers = append(servers, server)
			}
		}
	}
	return servers, nil
}
