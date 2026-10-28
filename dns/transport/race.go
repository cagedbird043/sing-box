package transport

import (
	"context"
	"errors"
	"fmt"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service"

	mDNS "github.com/miekg/dns"
)

var _ adapter.DNSTransport = (*RaceTransport)(nil)

func RegisterRace(registry *dns.TransportRegistry) {
	dns.RegisterTransport[option.RaceDNSServerOptions](registry, C.DNSTypeRace, NewRace)
}

type RaceTransport struct {
	dns.TransportAdapter
	manager adapter.DNSTransportManager
	servers []adapter.DNSTransport
}

func NewRace(ctx context.Context, _ log.ContextLogger, tag string, options option.RaceDNSServerOptions) (adapter.DNSTransport, error) {
	if len(options.Servers) == 0 {
		return nil, fmt.Errorf("race DNS server[%s]: servers must not be empty", tag)
	}
	for _, server := range options.Servers {
		if server == tag {
			return nil, fmt.Errorf("race DNS server[%s]: self-reference is not allowed", tag)
		}
	}
	return &RaceTransport{TransportAdapter: dns.NewTransportAdapter(C.DNSTypeRace, tag, options.Servers), manager: service.FromContext[adapter.DNSTransportManager](ctx)}, nil
}

func (t *RaceTransport) Start(stage adapter.StartStage, _ *adapter.Scope) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	servers := make([]adapter.DNSTransport, 0, len(t.Dependencies()))
	for _, tag := range t.Dependencies() {
		server, found := t.manager.Transport(tag)
		if !found {
			return fmt.Errorf("race DNS server[%s]: server[%s] not found", t.Tag(), tag)
		}
		servers = append(servers, server)
	}
	t.servers = servers
	return nil
}

func (t *RaceTransport) Close() error { return nil }

// Reset has no work: child connections belong to the transport manager.
func (t *RaceTransport) Reset() {}

func (t *RaceTransport) ExchangeAsync(ctx context.Context, message *mDNS.Msg, callback func(*mDNS.Msg, error)) {
	go func() { callback(t.Exchange(ctx, message)) }()
}

func (t *RaceTransport) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	if len(message.Question) != 1 {
		return nil, errors.New("race DNS: expected exactly one question")
	}
	if len(t.servers) == 0 {
		return nil, errors.New("race DNS: transport not started")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		response *mDNS.Msg
		err      error
	}
	results := make(chan result, len(t.servers))
	for _, server := range t.servers {
		// Existing transports may mutate messages. Each child owns its query copy.
		request := message.Copy()
		server.ExchangeAsync(ctx, request, func(response *mDNS.Msg, err error) {
			if response == nil && err == nil {
				err = errors.New("empty DNS response")
			}
			if err != nil {
				err = fmt.Errorf("server[%s]: %w", server.Tag(), err)
			}
			results <- result{response, err}
		})
	}
	var fallback *mDNS.Msg
	var failures []error
	for range t.servers {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result := <-results:
			if result.err != nil {
				failures = append(failures, result.err)
				continue
			}
			response := result.response
			if response.Rcode == mDNS.RcodeSuccess {
				for _, answer := range response.Answer {
					if answer.Header().Rrtype == message.Question[0].Qtype {
						return response, nil
					}
				}
			}
			if fallback == nil || raceResponseRank(response) > raceResponseRank(fallback) {
				fallback = response
			}
		}
	}
	if fallback != nil {
		return fallback, nil
	}
	return nil, errors.Join(failures...)
}

func raceResponseRank(message *mDNS.Msg) int {
	switch message.Rcode {
	case mDNS.RcodeNameError:
		return 2
	case mDNS.RcodeSuccess:
		return 1
	default:
		return 0
	}
}
