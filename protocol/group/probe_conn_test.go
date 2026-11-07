package group

import (
	"io"
	"net"
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

type ending struct {
	failed   bool
	down, up int64
}

func TestProbeConnEndsOnceAndTellsResetFromOrderlyClose(t *testing.T) {
	t.Parallel()
	var endings []ending
	end := func(failed bool, down, up int64) { endings = append(endings, ending{failed, down, up}) }

	client, server := pair(t)
	conn := newProbeConn(client, end)
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(server, make([]byte, 4)); err != nil {
		t.Fatal(err)
	}
	_, _ = server.Write([]byte("pong!"))
	server.Close()
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatalf("read after orderly close: %v", err)
	}
	conn.Close()
	conn.Close()
	if len(endings) != 1 || endings[0] != (ending{false, 5, 4}) {
		t.Fatalf("orderly close endings: %+v", endings)
	}

	endings = nil
	client, server = pair(t)
	conn = newProbeConn(client, end)
	_ = server.SetLinger(0)
	server.Close()
	buffer := make([]byte, 1)
	for range 2 {
		if _, err := conn.Read(buffer); err == nil {
			t.Fatal("read after reset succeeded")
		}
	}
	conn.Close()
	if len(endings) != 1 || !endings[0].failed {
		t.Fatalf("reset endings: %+v", endings)
	}
}
