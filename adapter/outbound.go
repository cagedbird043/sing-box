package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/netip"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/json"
	N "github.com/sagernet/sing/common/network"
)

// Note: for proxy protocols, outbound creates early connections by default.

type Outbound interface {
	Type() string
	Tag() string
	Network() []string
	Dependencies() []string
	N.Dialer
}

type OutboundWithPreferredRoutes interface {
	Outbound
	PreferredDomain(metadata *InboundContext, domain string) bool
	PreferredAddress(metadata *InboundContext, address netip.Addr) bool
}

type OutboundWithMultiplex interface {
	Outbound
	MultiplexEnabled() bool
}

type FlowOutbound interface {
	Outbound
	tun.Port
	PreMatchFlow(network string, destination netip.Addr) PreMatchAction
}

type OutboundRegistry interface {
	option.OutboundOptionsRegistry
	CreateOutbound(ctx context.Context, router Router, logger log.ContextLogger, tag string, outboundType string, options any) (Outbound, error)
}

type OutboundManager interface {
	Lifecycle
	Outbounds() []Outbound
	Outbound(tag string) (Outbound, bool)
	Default() Outbound
	Create(ctx context.Context, router Router, logger log.ContextLogger, tag string, outboundType string, options any) error
}

// ProviderOutboundManager publishes nodes whose scopes are owned by providers.
// fingerprints maps node tags to OutboundFingerprint values.
type ProviderOutboundManager interface {
	OutboundManager
	PublishProvider(namespace string, nodes []Outbound, fingerprints map[string]string) error
}

// OutboundFingerprinter reports the fingerprint of the options an outbound or
// endpoint was created from.
type OutboundFingerprinter interface {
	OutboundFingerprint(tag string) (string, bool)
}

// OutboundFingerprint identifies a node by its type and options, excluding
// its tag: renaming keeps the fingerprint, changing any connection parameter
// replaces it. Options that cannot be encoded yield an empty fingerprint.
func OutboundFingerprint(outboundType string, options any) string {
	content, err := json.Marshal(struct {
		Type    string `json:"type"`
		Options any    `json:"options"`
	}{outboundType, options})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:16])
}

type IdleConnectionKeeper interface {
	SetKeepIdleConnections(keep bool)
	CloseIdleConnections()
}

type Referrer interface {
	References() []string
}
