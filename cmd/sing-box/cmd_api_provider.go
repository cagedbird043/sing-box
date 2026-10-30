package main

import (
	"strconv"
	"time"

	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing/common/byteformats"

	"github.com/spf13/cobra"
)

var commandAPIProvider = &cobra.Command{
	Use:   "provider",
	Short: "Manage subscription providers",
}

func init() {
	commandAPIRoot.AddCommand(commandAPIProvider)
}

func printAPIProviders(providers []*daemon.Provider) {
	table := tableWriter{
		header:       []string{"TAG", "NODES", "REMAINING", "EXPIRE", "UPDATED"},
		emptyMessage: "no providers",
	}
	for _, provider := range providers {
		remaining := "-"
		expire := "-"
		if subscription := provider.GetSubscription(); subscription != nil {
			if subscription.GetTotalBytes() != 0 {
				remainingBytes := subscription.GetTotalBytes() - subscription.GetUploadBytes() - subscription.GetDownloadBytes()
				if remainingBytes < 0 {
					remaining = "-" + byteformats.FormatBytes(uint64(-remainingBytes))
				} else {
					remaining = byteformats.FormatBytes(uint64(remainingBytes))
				}
			}
			if subscription.GetExpireAtSeconds() != 0 {
				expire = time.Unix(subscription.GetExpireAtSeconds(), 0).Local().Format("2006-01-02")
			}
		}
		updated := "-"
		if provider.GetUpdatedAtMs() != 0 {
			updated = time.UnixMilli(provider.GetUpdatedAtMs()).Local().Format("2006-01-02 15:04:05")
		}
		table.addRow(provider.GetTag(), strconv.Itoa(len(provider.GetNodes())), remaining, expire, updated)
	}
	table.flush()
}
