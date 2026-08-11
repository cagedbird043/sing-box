//go:build darwin

package main

import (
	"net/netip"
	"reflect"
	"testing"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-tun"
)

func TestPlatformDNSServersUsesEnabledAutoRouteTUN(t *testing.T) {
	options := option.Options{Inbounds: []option.Inbound{
		{
			Type: C.TypeTun,
			Options: &option.TunInboundOptions{
				Address:   []netip.Prefix{netip.MustParsePrefix("172.19.0.1/30"), netip.MustParsePrefix("fdfe:dcba:9876::1/126")},
				AutoRoute: true,
			},
		},
	}}
	servers, err := platformDNSServers(options)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"172.19.0.2", "fdfe:dcba:9876::2"}
	if !reflect.DeepEqual(servers, want) {
		t.Fatalf("servers = %v, want %v", servers, want)
	}
}

func TestPlatformDNSServersSkipsDisabledAndNonRoutingTUN(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		options option.TunInboundOptions
	}{
		{name: "auto route disabled", options: option.TunInboundOptions{Address: []netip.Prefix{netip.MustParsePrefix("172.19.0.1/30")}}},
		{name: "DNS disabled", options: option.TunInboundOptions{Address: []netip.Prefix{netip.MustParsePrefix("172.19.0.1/30")}, AutoRoute: true, DNSMode: tun.DNSModeDisabled}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			options := option.Options{Inbounds: []option.Inbound{{Type: C.TypeTun, Options: &testCase.options}}}
			servers, err := platformDNSServers(options)
			if err != nil {
				t.Fatal(err)
			}
			if len(servers) != 0 {
				t.Fatalf("servers = %v, want none", servers)
			}
		})
	}
}

func TestPlatformDNSServersHonorsExplicitAddress(t *testing.T) {
	options := option.Options{Inbounds: []option.Inbound{{
		Type: C.TypeTun,
		Options: &option.TunInboundOptions{
			Address:    []netip.Prefix{netip.MustParsePrefix("172.19.0.1/30")},
			DNSAddress: []netip.Addr{netip.MustParseAddr("172.19.0.3")},
			AutoRoute:  true,
		},
	}}}
	servers, err := platformDNSServers(options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(servers, []string{"172.19.0.3"}) {
		t.Fatalf("servers = %v", servers)
	}
}
