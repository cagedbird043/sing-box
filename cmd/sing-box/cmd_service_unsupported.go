//go:build (!linux || android) && (!darwin || ios) && !windows

package main

import (
	"context"
	"errors"
)

var errUnsupportedServicePlatform = errors.New("system service management is only available on Linux, macOS and Windows")

func managedServiceDefaults() (string, string, string, error) {
	return "", "", "", errUnsupportedServicePlatform
}

func managedServiceLog(s managedService) string { return "" }

func installManagedService(s managedService) error { return errUnsupportedServicePlatform }

func uninstallManagedService(name string) error { return errUnsupportedServicePlatform }

func lookupManagedService(name string) (*managedService, bool, error) {
	return nil, false, errUnsupportedServicePlatform
}

func runManagedService(ctx context.Context, s managedService) error {
	return errUnsupportedServicePlatform
}
