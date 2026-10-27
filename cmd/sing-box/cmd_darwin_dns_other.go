//go:build !darwin

package main

import (
	"io"

	"github.com/sagernet/sing-box/option"
)

func startPlatformDNS(option.Options) (io.Closer, error) {
	return nil, nil
}
