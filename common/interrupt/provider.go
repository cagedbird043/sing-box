package interrupt

import "context"

type providerConnectionKey struct{}

func ContextWithIsProviderConnection(ctx context.Context) context.Context {
	return context.WithValue(ctx, providerConnectionKey{}, true)
}

func IsProviderConnectionFromContext(ctx context.Context) bool {
	value, _ := ctx.Value(providerConnectionKey{}).(bool)
	return value
}
