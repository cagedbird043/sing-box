package parser

import (
	"testing"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
)

func TestOverrideTLSOptionCertificatePins(t *testing.T) {
	certificateHash := badoption.Listable[[]byte]{make([]byte, 32)}
	options := overrideTLSOption(&option.OutboundTLSOptions{
		Enabled:                    true,
		Certificate:                badoption.Listable[string]{"-----BEGIN CERTIFICATE-----"},
		CertificatePath:            "/etc/ca.pem",
		CertificatePublicKeySHA256: badoption.Listable[[]byte]{{1}},
	}, &option.OverrideTLSOptions{CertificateSHA256: &certificateHash})
	if len(options.CertificateSHA256) != 1 || len(options.CertificateSHA256[0]) != 32 {
		t.Fatalf("certificate pin not applied: %x", options.CertificateSHA256)
	}
	if len(options.Certificate) != 0 || options.CertificatePath != "" || len(options.CertificatePublicKeySHA256) != 0 {
		t.Fatalf("override pin must replace member trust anchors: %+v", options)
	}

	untouched := overrideTLSOption(&option.OutboundTLSOptions{
		Enabled:         true,
		CertificatePath: "/etc/ca.pem",
	}, &option.OverrideTLSOptions{ServerName: new(string)})
	if untouched.CertificatePath != "/etc/ca.pem" {
		t.Fatalf("unrelated override must keep member trust anchors: %+v", untouched)
	}

	empty := badoption.Listable[[]byte]{}
	kept := overrideTLSOption(&option.OutboundTLSOptions{
		Enabled:           true,
		CertificatePath:   "/etc/ca.pem",
		CertificateSHA256: certificateHash,
	}, &option.OverrideTLSOptions{CertificateSHA256: &empty})
	if kept.CertificatePath != "/etc/ca.pem" || len(kept.CertificateSHA256) != 1 {
		t.Fatalf("empty pin override must keep member trust anchors: %+v", kept)
	}
}
