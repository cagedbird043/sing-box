package route

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/protocol/group"
	E "github.com/sagernet/sing/common/exceptions"
)

func (m *ConnectionManager) requestURLTestRecheck(ctx context.Context, network string, err error) {
	if ctx.Err() != nil || E.IsCanceled(err) {
		return
	}
	metadata := adapter.ContextFrom(ctx)
	if metadata == nil {
		return
	}
	chain := metadata.OutboundChain
	if len(chain) == 0 {
		return
	}
	leaf := chain[len(chain)-1]
	for _, detour := range chain {
		if test, ok := detour.(*group.URLTest); ok {
			test.RequestRecheck(leaf, network)
		}
	}
}
