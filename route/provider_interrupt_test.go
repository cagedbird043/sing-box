package route

import (
	"context"
	"io"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/interrupt"
)

type attachmentTestGroup struct {
	adapter.OutboundGroup
	attachments int
	detachments int
}

func (g *attachmentTestGroup) AttachConnection(io.Closer) func() {
	g.attachments++
	return func() { g.detachments++ }
}

func TestProviderDownloadSkipsNativeConnectionAttachment(t *testing.T) {
	group := &attachmentTestGroup{}
	chain := []adapter.Outbound{group}
	ctx := interrupt.ContextWithIsProviderConnection(context.Background())
	if handler := registerInterrupt(ctx, chain, nil, nil); handler != nil {
		t.Fatal("provider connection unexpectedly registered")
	}
	if group.attachments != 0 {
		t.Fatal("provider connection attached to interrupt group")
	}
	handler := registerInterrupt(context.Background(), chain, nil, nil)
	if handler == nil || group.attachments != 1 {
		t.Fatal("ordinary native attachment changed")
	}
	handler(nil)
	if group.detachments != 1 {
		t.Fatal("ordinary native attachment did not detach")
	}
}
