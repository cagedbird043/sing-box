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

// probeConn reports a connection through probe groups to their node-probe
// services: bytes carried, and whether it ended by a transport failure.
type probeConn struct {
	N.ExtendedConn
	end    func(failed bool, down, up int64)
	down   atomic.Int64
	up     atomic.Int64
	failed atomic.Bool
	ended  atomic.Bool
}

func newProbeConn(conn net.Conn, end func(failed bool, down, up int64)) *probeConn {
	return &probeConn{ExtendedConn: bufio.NewExtendedConn(conn), end: end}
}

// transportFailure tells a broken path from an orderly end: EOF and our own or
// the caller's close are normal; resets, timeouts and the like are not.
func transportFailure(err error) bool {
	return err != nil &&
		!errors.Is(err, io.EOF) &&
		!errors.Is(err, net.ErrClosed) &&
		!errors.Is(err, os.ErrClosed) &&
		!errors.Is(err, io.ErrClosedPipe) &&
		!errors.Is(err, context.Canceled)
}

func (c *probeConn) observe(err error) error {
	if transportFailure(err) && c.failed.CompareAndSwap(false, true) {
		c.finish()
	}
	return err
}

func (c *probeConn) finish() {
	if c.ended.CompareAndSwap(false, true) {
		c.end(c.failed.Load(), c.down.Load(), c.up.Load())
	}
}

func (c *probeConn) Read(p []byte) (int, error) {
	n, err := c.ExtendedConn.Read(p)
	c.down.Add(int64(n))
	return n, c.observe(err)
}

func (c *probeConn) Write(p []byte) (int, error) {
	n, err := c.ExtendedConn.Write(p)
	c.up.Add(int64(n))
	return n, c.observe(err)
}

func (c *probeConn) ReadBuffer(buffer *buf.Buffer) error {
	err := c.ExtendedConn.ReadBuffer(buffer)
	if err == nil {
		c.down.Add(int64(buffer.Len()))
	}
	return c.observe(err)
}

func (c *probeConn) WriteBuffer(buffer *buf.Buffer) error {
	length := int64(buffer.Len())
	err := c.ExtendedConn.WriteBuffer(buffer)
	if err == nil {
		c.up.Add(length)
	}
	return c.observe(err)
}

func (c *probeConn) Close() error {
	c.finish()
	return c.ExtendedConn.Close()
}

// Copies must not unwrap past this conn, or failures would go unseen.
func (c *probeConn) ReaderReplaceable() bool { return false }
func (c *probeConn) WriterReplaceable() bool { return false }
func (c *probeConn) Upstream() any           { return c.ExtendedConn }
