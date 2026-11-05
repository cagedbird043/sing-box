package group

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
)

// pair returns a client conn and its accepted server side.
func pair(t *testing.T) (net.Conn, *net.TCPConn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	return client, server.(*net.TCPConn)
}

func TestProbeConnReportsResetNotOrderlyClose(t *testing.T) {
	t.Parallel()
	var reports atomic.Int32
	report := func(error) { reports.Add(1) }

	client, server := pair(t)
	conn := newProbeConn(client, report)
	server.Close()
	if _, err := conn.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("read after orderly close: %v", err)
	}
	conn.Close()
	_, _ = conn.Read(make([]byte, 1))
	if reports.Load() != 0 {
		t.Fatal("orderly or local close reported as a failure")
	}

	client, server = pair(t)
	conn = newProbeConn(client, report)
	_ = server.SetLinger(0)
	server.Close()
	buffer := make([]byte, 1)
	for range 2 {
		if _, err := conn.Read(buffer); err == nil {
			t.Fatal("read after reset succeeded")
		}
	}
	if reports.Load() != 1 {
		t.Fatalf("reset reported %d times, want once", reports.Load())
	}
}
