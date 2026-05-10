package parser

import (
	"context"
	"testing"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

func TestParseClashSubscriptionShadowsocks(t *testing.T) {
	content := `proxies:
  - name: local-ss
    type: ss
    server: 127.0.0.1
    port: 8388
    cipher: aes-128-gcm
    password: test-password
    udp: true
`
	outbounds, endpoints, err := ParseClashSubscription(context.Background(), content)
	if err != nil {
		t.Fatal(err)
	}
	if len(endpoints) != 0 {
		t.Fatalf("expected no endpoints, got %d", len(endpoints))
	}
	if len(outbounds) != 1 {
		t.Fatalf("expected one outbound, got %d", len(outbounds))
	}
	outbound := outbounds[0]
	if outbound.Tag != "local-ss" {
		t.Fatalf("unexpected tag: %q", outbound.Tag)
	}
	if outbound.Type != C.TypeShadowsocks {
		t.Fatalf("unexpected type: %q", outbound.Type)
	}
	options, ok := outbound.Options.(*option.ShadowsocksOutboundOptions)
	if !ok {
		t.Fatalf("unexpected options type: %T", outbound.Options)
	}
	if options.Server != "127.0.0.1" || options.ServerPort != 8388 {
		t.Fatalf("unexpected server: %s:%d", options.Server, options.ServerPort)
	}
	if options.Method != "aes-128-gcm" || options.Password != "test-password" {
		t.Fatalf("unexpected cipher/password: %s/%s", options.Method, options.Password)
	}
	networks := options.Network.Build()
	if len(networks) != 2 || networks[0] != "tcp" || networks[1] != "udp" {
		t.Fatalf("unexpected network: %v", networks)
	}
}
