package auth

import "context"

type identityContext struct{}

func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, identityContext{}, id)
}

func IdentityFrom(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(identityContext{}).(*Identity)
	return id, ok
}
