package transport

import (
	"context"
	"errors"
	"testing"
	"time"

	mDNS "github.com/miekg/dns"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/dns"
)

type raceFake struct {
	dns.TransportAdapter
	exchange func(context.Context, *mDNS.Msg) (*mDNS.Msg, error)
}

func (f *raceFake) Start(adapter.StartStage, *adapter.Scope) error { return nil }
func (f *raceFake) Close() error                                   { return nil }
func (f *raceFake) Reset()                                         {}
func (f *raceFake) Exchange(ctx context.Context, msg *mDNS.Msg) (*mDNS.Msg, error) {
	return f.exchange(ctx, msg)
}

func (f *raceFake) ExchangeAsync(ctx context.Context, msg *mDNS.Msg, callback func(*mDNS.Msg, error)) {
	go func() { callback(f.Exchange(ctx, msg)) }()
}

func raceReply(rcode int, rrtype uint16) *mDNS.Msg {
	msg := &mDNS.Msg{MsgHdr: mDNS.MsgHdr{Rcode: rcode}}
	if rrtype != 0 {
		msg.Answer = []mDNS.RR{&mDNS.RFC3597{Hdr: mDNS.RR_Header{Name: "example.org.", Rrtype: rrtype, Class: mDNS.ClassINET}}}
	}
	return msg
}
func raceServer(delay time.Duration, response *mDNS.Msg, err error) adapter.DNSTransport {
	return &raceFake{exchange: func(ctx context.Context, _ *mDNS.Msg) (*mDNS.Msg, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
			return response, err
		}
	}}
}
func TestRaceExchange(t *testing.T) {
	valid := raceReply(mDNS.RcodeSuccess, mDNS.TypeA)
	nxdomain := raceReply(mDNS.RcodeNameError, 0)
	empty := raceReply(mDNS.RcodeSuccess, 0)
	failed := errors.New("upstream failed")
	tests := []struct {
		name    string
		servers []adapter.DNSTransport
		want    *mDNS.Msg
		wantErr bool
	}{
		{"first valid wins", []adapter.DNSTransport{raceServer(0, valid, nil), raceServer(time.Second, raceReply(0, mDNS.TypeA), nil)}, valid, false},
		{"CNAME only skipped", []adapter.DNSTransport{raceServer(0, raceReply(0, mDNS.TypeCNAME), nil), raceServer(10*time.Millisecond, valid, nil)}, valid, false},
		{"error skipped", []adapter.DNSTransport{raceServer(0, nil, failed), raceServer(10*time.Millisecond, valid, nil)}, valid, false},
		{"slow valid beats invalid", []adapter.DNSTransport{raceServer(0, nxdomain, nil), raceServer(10*time.Millisecond, valid, nil)}, valid, false},
		{"all errors", []adapter.DNSTransport{raceServer(0, nil, failed), raceServer(0, nil, failed)}, nil, true},
		{"NXDOMAIN fallback", []adapter.DNSTransport{raceServer(0, empty, nil), raceServer(10*time.Millisecond, nxdomain, nil), raceServer(0, raceReply(mDNS.RcodeServerFailure, 0), nil)}, nxdomain, false},
		{"NOERROR fallback", []adapter.DNSTransport{raceServer(0, raceReply(mDNS.RcodeRefused, 0), nil), raceServer(0, empty, nil)}, empty, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &RaceTransport{servers: tt.servers}
			query := new(mDNS.Msg)
			query.SetQuestion("example.org.", mDNS.TypeA)
			got, err := transport.Exchange(context.Background(), query)
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Fatalf("got %p, %v; want %p, error=%v", got, err, tt.want, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, failed) {
				t.Fatalf("joined error lost cause: %v", err)
			}
		})
	}
}
func TestRaceOtherQuestionType(t *testing.T) {
	txt := raceReply(0, mDNS.TypeTXT)
	transport := &RaceTransport{servers: []adapter.DNSTransport{raceServer(0, raceReply(0, mDNS.TypeA), nil), raceServer(10*time.Millisecond, txt, nil)}}
	query := new(mDNS.Msg)
	query.SetQuestion("example.org.", mDNS.TypeTXT)
	got, err := transport.Exchange(context.Background(), query)
	if got != txt || err != nil {
		t.Fatalf("got %v, %v", got, err)
	}
}
func TestRaceCancelsLosers(t *testing.T) {
	canceled := make(chan struct{})
	transport := &RaceTransport{servers: []adapter.DNSTransport{
		&raceFake{exchange: func(ctx context.Context, _ *mDNS.Msg) (*mDNS.Msg, error) {
			<-ctx.Done()
			close(canceled)
			return nil, ctx.Err()
		}},
		raceServer(10*time.Millisecond, raceReply(0, mDNS.TypeA), nil),
	}}
	query := new(mDNS.Msg)
	query.SetQuestion("example.org.", mDNS.TypeA)
	if _, err := transport.Exchange(context.Background(), query); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("loser not canceled")
	}
}
func TestRaceParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport := &RaceTransport{servers: []adapter.DNSTransport{raceServer(time.Hour, nil, nil)}}
	query := new(mDNS.Msg)
	query.SetQuestion("example.org.", mDNS.TypeA)
	if _, err := transport.Exchange(ctx, query); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
