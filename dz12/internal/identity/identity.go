package identity

import (
	"context"
	"github.com/google/uuid"
)

type Principal struct {
	ID   uuid.UUID
	Role string
}
type contextKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}
func FromContext(ctx context.Context) Principal {
	p, _ := ctx.Value(contextKey{}).(Principal)
	return p
}
