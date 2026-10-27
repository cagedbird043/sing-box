package parser

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

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

func TestParseClashSubscriptionAnyTLSFingerprintAndDurations(t *testing.T) {
	content := `proxies:
  - name: pinned
    type: anytls
    server: example.com
    port: 443
    password: test-password
    sni: www.example.org
    ca-str: "-----BEGIN CERTIFICATE-----"
    fingerprint: FA:C2:6F:65:C0:34:82:9D:A4:2D:74:0D:23:C4:A7:20:24:75:A3:83:4F:0E:BA:EC:AE:5F:93:4A:DB:BF:D6:40
    idle-session-check-interval: 30
    idle-session-timeout: 45
  - name: invalid-fingerprint
    type: anytls
    server: example.com
    port: 443
    password: test-password
    fingerprint: chrome
`
	outbounds, _, err := ParseClashSubscription(context.Background(), content)
	if err != nil {
		t.Fatal(err)
	}
	if len(outbounds) != 2 {
		t.Fatalf("expected two outbounds, got %d", len(outbounds))
	}
	pinned := outbounds[0].Options.(*option.AnyTLSOutboundOptions)
	if time.Duration(pinned.IdleSessionCheckInterval) != 30*time.Second || time.Duration(pinned.IdleSessionTimeout) != 45*time.Second {
		t.Fatalf("unexpected idle durations: %v/%v", pinned.IdleSessionCheckInterval, pinned.IdleSessionTimeout)
	}
	tls := pinned.TLS
	if len(tls.CertificateSHA256) != 1 || hex.EncodeToString(tls.CertificateSHA256[0]) != "fac26f65c034829da42d740d23c4a7202475a3834f0ebaecae5f934adbbfd640" {
		t.Fatalf("unexpected certificate pin: %x", tls.CertificateSHA256)
	}
	if len(tls.Certificate) != 0 || tls.CertificatePath != "" || tls.Insecure {
		t.Fatalf("pin must replace custom CA without disabling verification: %+v", tls)
	}
	invalid := outbounds[1].Options.(*option.AnyTLSOutboundOptions).TLS
	if len(invalid.CertificateSHA256) != 0 || invalid.Insecure {
		t.Fatalf("invalid fingerprint must fall back to normal verification: %+v", invalid)
	}
}

func TestParseClashSubscriptionDurationUnits(t *testing.T) {
	content := `proxies:
  - name: tuic
    type: tuic
    server: example.com
    port: 443
    uuid: 00000000-0000-0000-0000-000000000000
    password: test-password
    heartbeat-interval: 10000
  - name: hy2
    type: hysteria2
    server: example.com
    port: 443
    password: test-password
    hop-interval: 30
`
	outbounds, _, err := ParseClashSubscription(context.Background(), content)
	if err != nil {
		t.Fatal(err)
	}
	if heartbeat := time.Duration(outbounds[0].Options.(*option.TUICOutboundOptions).Heartbeat); heartbeat != 10*time.Second {
		t.Fatalf("unexpected tuic heartbeat: %v", heartbeat)
	}
	if hop := time.Duration(outbounds[1].Options.(*option.Hysteria2OutboundOptions).HopInterval); hop != 30*time.Second {
		t.Fatalf("unexpected hysteria2 hop interval: %v", hop)
	}
}
