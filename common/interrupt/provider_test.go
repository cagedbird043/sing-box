package interrupt

import (
	"net"
	"testing"
	"time"
)

func TestProviderConnectionSurvivesGroupInterrupt(t *testing.T) {
	group := NewGroup()
	tracked, peer := net.Pipe()
	defer peer.Close()
	defer tracked.Close()
	provider := group.NewConn(tracked, false, true)
	regular, regularPeer := net.Pipe()
	defer regularPeer.Close()
	group.NewConn(regular, false, false)
	group.Interrupt(true)
	if _, err := regularPeer.Write([]byte{1}); err == nil {
		t.Fatal("ordinary connection was not interrupted")
	}
	if err := provider.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = peer.Write([]byte{1}) }()
	var data [1]byte
	if _, err := provider.Read(data[:]); err != nil {
		t.Fatal("provider download was interrupted: ", err)
	}
}
