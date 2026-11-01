package route

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/group"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

type routeRecheckOutbound struct {
	adapter.Outbound
	started chan struct{}
}

func (o *routeRecheckOutbound) Tag() string       { return "leaf" }
func (o *routeRecheckOutbound) Network() []string { return []string{N.NetworkTCP, N.NetworkUDP} }
func (o *routeRecheckOutbound) DialContext(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
	o.started <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}

type routeRecheckManager struct {
	adapter.OutboundManager
	nodes   map[string]adapter.Outbound
	lookups atomic.Int32
}

func (m *routeRecheckManager) Outbound(tag string) (adapter.Outbound, bool) {
	m.lookups.Add(1)
	node, ok := m.nodes[tag]
	return node, ok
}

type routeRecheckFixture struct {
	ctx       context.Context
	cancel    context.CancelFunc
	manager   *ConnectionManager
	outbounds *routeRecheckManager
	leaf      *routeRecheckOutbound
}

func newRouteRecheckFixture(t *testing.T) *routeRecheckFixture {
	t.Helper()
	leaf := &routeRecheckOutbound{started: make(chan struct{}, 4)}
	manager := &routeRecheckManager{nodes: map[string]adapter.Outbound{leaf.Tag(): leaf}}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = service.ContextWith[adapter.OutboundManager](ctx, manager)
	history := urltest.NewHistoryStorage()
	ctx = service.ContextWithPtr(ctx, history)
	logger := log.NewNOPFactory().Logger()
	scope := adapter.NewScope(ctx, logger)
	t.Cleanup(func() { cancel(); scope.Close(); history.Close() })
	var chain []adapter.Outbound
	for _, tag := range []string{"inner", "outer"} {
		member := leaf.Tag()
		if tag == "outer" {
			member = "inner"
		}
		o, err := group.NewURLTest(ctx, nil, logger, tag, option.URLTestOutboundOptions{
			GroupCommonOption: option.GroupCommonOption{Outbounds: []string{member}},
			URL:               "http://probe.invalid/check",
		})
		if err != nil {
			t.Fatal(err)
		}
		test := o.(*group.URLTest)
		manager.nodes[tag] = test
		if err = test.Start(adapter.StartStateStart, scope); err != nil {
			t.Fatal(err)
		}
		chain = append([]adapter.Outbound{test}, chain...)
	}
	chain = append(chain, leaf)
	ctx = adapter.WithContext(ctx, &adapter.InboundContext{OutboundChain: chain})
	manager.lookups.Store(0)
	return &routeRecheckFixture{ctx: ctx, cancel: cancel, manager: NewConnectionManager(ctx, logger), outbounds: manager, leaf: leaf}
}

func (f *routeRecheckFixture) assertRequested(t *testing.T, want bool) {
	t.Helper()
	if !want {
		if f.outbounds.lookups.Load() != 0 {
			t.Fatal("ignored connection failure reached URL-test identity checks")
		}
		return
	}
	// Both the outer and inner groups must receive the selected leaf.
	for range 2 {
		select {
		case <-f.leaf.started:
		case <-time.After(5 * time.Second):
			t.Fatal("connection failure did not confirm every URL-test group in the chain")
		}
	}
}

type routeRecheckConn struct {
	net.Conn
	readData  []byte
	readErr   error
	writeErr  error
	handshake bool
	payload   []byte
	written   int
}

func (c *routeRecheckConn) Read(p []byte) (int, error) {
	if len(c.readData) > 0 {
		n := copy(p, c.readData)
		c.readData = c.readData[n:]
		return n, nil
	}
	return 0, c.readErr
}
func (c *routeRecheckConn) Write(p []byte) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	c.written += len(p)
	return len(p), nil
}
func (c *routeRecheckConn) Close() error                     { return nil }
func (c *routeRecheckConn) SetWriteDeadline(time.Time) error { return nil }
func (c *routeRecheckConn) NeedHandshakeForWrite() bool      { return c.handshake }
func (c *routeRecheckConn) ReadCached() *buf.Buffer {
	if len(c.payload) == 0 {
		return nil
	}
	payload := buf.As(c.payload)
	c.payload = nil
	return payload
}
func (c *routeRecheckConn) HandshakeFailure(error) error { return nil }

func TestConnectionHandshakeRequestsURLTestRecheck(t *testing.T) {
	for _, tc := range []struct {
		name        string
		payload     bool
		serverFirst bool
		direction   bool
		err         error
		canceled    bool
		emptyChain  bool
		want        bool
	}{
		{name: "server-first", serverFirst: true, err: io.ErrUnexpectedEOF, want: true},
		{name: "payload", payload: true, err: io.ErrUnexpectedEOF, want: true},
		{name: "download-handshake", serverFirst: true, direction: true, err: io.ErrUnexpectedEOF},
		{name: "canceled-error", serverFirst: true, err: context.Canceled},
		{name: "canceled-context", serverFirst: true, err: io.ErrUnexpectedEOF, canceled: true},
		{name: "empty-chain", serverFirst: true, err: io.ErrUnexpectedEOF, emptyChain: true},
		{name: "ignored-eof", serverFirst: true, err: io.EOF},
		{name: "ignored-deadline", serverFirst: true, err: context.DeadlineExceeded},
		{name: "ignored-invalid", serverFirst: true, err: os.ErrInvalid},
		{name: "ignored-timeout", serverFirst: true, err: &net.DNSError{IsTimeout: true}},
		{name: "payload-eof", payload: true, err: io.EOF, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRouteRecheckFixture(t)
			ctx := f.ctx
			if tc.emptyChain {
				ctx = adapter.WithContext(ctx, &adapter.InboundContext{})
			}
			if tc.canceled {
				f.cancel()
			}
			source := &routeRecheckConn{}
			if tc.payload {
				source.payload = []byte("request")
			}
			destination := &routeRecheckConn{handshake: true, writeErr: tc.err}
			var done atomic.Bool
			f.manager.kickWriteHandshake(ctx, source, destination, tc.serverFirst, tc.direction, &done, nil)
			f.assertRequested(t, tc.want)
		})
	}
}

func TestConnectionDownloadRequestsURLTestRecheck(t *testing.T) {
	for _, tc := range []struct {
		name       string
		data       string
		err        error
		canceled   bool
		emptyChain bool
		upload     bool
		writeErr   error
		want       bool
	}{
		{name: "zero-byte-error", err: io.ErrUnexpectedEOF, want: true},
		{name: "forwarded-response", data: "response", err: io.ErrUnexpectedEOF},
		{name: "inbound-write-failure", data: "response", err: io.ErrUnexpectedEOF, writeErr: io.ErrClosedPipe, want: true},
		{name: "canceled-error", err: context.Canceled},
		{name: "wrapped-canceled", err: errors.Join(io.ErrUnexpectedEOF, context.Canceled)},
		{name: "canceled-context", err: io.ErrUnexpectedEOF, canceled: true},
		{name: "empty-chain", err: io.ErrUnexpectedEOF, emptyChain: true},
		{name: "upload-copy", err: io.ErrUnexpectedEOF, upload: true},
		{name: "clean-eof", err: io.EOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRouteRecheckFixture(t)
			ctx := f.ctx
			if tc.emptyChain {
				ctx = adapter.WithContext(ctx, &adapter.InboundContext{})
			}
			if tc.canceled {
				f.cancel()
			}
			var done atomic.Bool
			source := &routeRecheckConn{readData: []byte(tc.data), readErr: tc.err}
			destination := &routeRecheckConn{writeErr: tc.writeErr}
			f.manager.connectionCopy(ctx, source, destination, !tc.upload, &done, nil)
			f.assertRequested(t, tc.want)
			if len(tc.data) > 0 && tc.writeErr == nil && destination.written != len(tc.data) {
				t.Fatal("response was not forwarded")
			}
		})
	}
}

func TestConnectionDownloadRechecksAfterUploadDone(t *testing.T) {
	f := newRouteRecheckFixture(t)
	var done atomic.Bool
	source := &routeRecheckConn{payload: []byte("request"), readData: []byte("more request"), readErr: io.ErrUnexpectedEOF}
	destination := &routeRecheckConn{handshake: true}
	if f.manager.kickWriteHandshake(f.ctx, source, destination, false, false, &done, nil) {
		t.Fatal("initial handshake failed")
	}
	destination.handshake = false
	destination.writeErr = io.ErrClosedPipe
	f.manager.connectionCopy(f.ctx, source, destination, false, &done, nil)
	if !done.Load() {
		t.Fatal("upload did not set done")
	}
	f.assertRequested(t, false)
	f.manager.connectionCopy(f.ctx, &routeRecheckConn{readErr: io.ErrUnexpectedEOF}, &routeRecheckConn{}, true, &done, nil)
	f.assertRequested(t, true)
}

func TestConnectionRecheckWithoutMetadata(t *testing.T) {
	f := newRouteRecheckFixture(t)
	f.manager.requestURLTestRecheck(context.Background(), N.NetworkTCP, io.ErrUnexpectedEOF)
	f.assertRequested(t, false)
}
