package hosts

import (
	"context"
	"net/netip"
	"os"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service/filemanager"

	mDNS "github.com/miekg/dns"
)

func RegisterTransport(registry *dns.TransportRegistry) {
	dns.RegisterTransport[option.HostsDNSServerOptions](registry, C.DNSTypeHosts, NewTransport)
}

var (
	_ adapter.DNSTransport                    = (*Transport)(nil)
	_ adapter.DNSTransportWithPreferredDomain = (*Transport)(nil)
)

type Transport struct {
	dns.TransportAdapter
	files      []*File
	predefined map[string][]netip.Addr
	providers  []*RemoteProvider
}

func NewTransport(ctx context.Context, logger log.ContextLogger, tag string, options option.HostsDNSServerOptions) (adapter.DNSTransport, error) {
	var (
		files      []*File
		predefined = make(map[string][]netip.Addr)
		providers  []*RemoteProvider
	)
	if len(options.Path) == 0 {
		defaultFile, err := NewDefault()
		if err != nil {
			return nil, err
		}
		files = append(files, defaultFile)
	} else {
		for _, path := range options.Path {
			files = append(files, NewFile(ctx, filemanager.BasePath(ctx, os.ExpandEnv(path))))
		}
	}
	for index, providerOptions := range options.Providers {
		provider, err := NewRemoteProvider(ctx, logger, index, providerOptions)
		if err != nil {
			return nil, err
		}
		providers = append(providers, provider)
		files = append(files, NewFile(ctx, provider.Path()))
	}
	if options.Predefined != nil {
		for _, entry := range options.Predefined.Entries() {
			predefined[mDNS.CanonicalName(entry.Key)] = entry.Value
		}
	}
	return &Transport{
		TransportAdapter: dns.NewTransportAdapter(C.DNSTypeHosts, tag, nil),
		files:            files,
		predefined:       predefined,
		providers:        providers,
	}, nil
}

func (t *Transport) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	for _, provider := range t.providers {
		err := provider.Start()
		if err != nil {
			return err
		}
	}
	return nil
}

func (t *Transport) Close() error {
	for _, provider := range t.providers {
		_ = provider.Close()
	}
	return nil
}

func (t *Transport) Reset() {
}

func (t *Transport) PreferredDomain(domain string) bool {
	if _, loaded := t.predefined[domain]; loaded {
		return true
	}
	for _, file := range t.files {
		if len(file.Lookup(domain)) > 0 {
			return true
		}
	}
	return false
}

func (t *Transport) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	question := message.Question[0]
	domain := mDNS.CanonicalName(question.Name)
	if question.Qtype == mDNS.TypeA || question.Qtype == mDNS.TypeAAAA {
		if addresses, ok := t.predefined[domain]; ok {
			return dns.FixedResponse(message.Id, question, addresses, C.DefaultDNSTTL), nil
		}
		for _, file := range t.files {
			addresses := file.Lookup(domain)
			if len(addresses) > 0 {
				return dns.FixedResponse(message.Id, question, addresses, C.DefaultDNSTTL), nil
			}
		}
	}
	return &mDNS.Msg{
		MsgHdr: mDNS.MsgHdr{
			Id:       message.Id,
			Rcode:    mDNS.RcodeNameError,
			Response: true,
		},
		Question: []mDNS.Question{question},
	}, nil
}

func (t *Transport) ExchangeAsync(ctx context.Context, message *mDNS.Msg, callback func(response *mDNS.Msg, err error)) {
	callback(t.Exchange(ctx, message))
}
