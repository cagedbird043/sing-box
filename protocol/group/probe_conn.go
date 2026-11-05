package group

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync/atomic"

	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	N "github.com/sagernet/sing/common/network"
)

// probeConn reports the first transport failure of a connection through a
// probe group, so a node that dies mid-stream is checked at once rather than at
// the next scheduled check.
type probeConn struct {
	N.ExtendedConn
	report   func(error)
	reported atomic.Bool
}

func newProbeConn(conn net.Conn, report func(error)) *probeConn {
	return &probeConn{ExtendedConn: bufio.NewExtendedConn(conn), report: report}
}

// transportFailure tells a broken path from an orderly end: EOF and our own or
// the caller's close are normal; resets, timeouts and the like are not. A
// destination that resets is reported too, and the check it triggers clears the node.
func transportFailure(err error) bool {
	return err != nil &&
		!errors.Is(err, io.EOF) &&
		!errors.Is(err, net.ErrClosed) &&
		!errors.Is(err, os.ErrClosed) &&
		!errors.Is(err, io.ErrClosedPipe) &&
		!errors.Is(err, context.Canceled)
}

func (c *probeConn) observe(err error) error {
	if transportFailure(err) && c.reported.CompareAndSwap(false, true) {
		c.report(err)
	}
	return err
}

func (c *probeConn) Read(p []byte) (int, error) {
	n, err := c.ExtendedConn.Read(p)
	return n, c.observe(err)
}

func (c *probeConn) Write(p []byte) (int, error) {
	n, err := c.ExtendedConn.Write(p)
	return n, c.observe(err)
}

func (c *probeConn) ReadBuffer(buffer *buf.Buffer) error {
	return c.observe(c.ExtendedConn.ReadBuffer(buffer))
}

func (c *probeConn) WriteBuffer(buffer *buf.Buffer) error {
	return c.observe(c.ExtendedConn.WriteBuffer(buffer))
}

// Copies must not unwrap past this conn, or failures would go unseen.
func (c *probeConn) ReaderReplaceable() bool { return false }
func (c *probeConn) WriterReplaceable() bool { return false }
func (c *probeConn) Upstream() any           { return c.ExtendedConn }
