package rpc

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"marketplace/internal/identity"
)

func withPrincipal(ctx context.Context, p identity.Principal) context.Context {
	md, _ := metadata.FromOutgoingContext(ctx)
	md = md.Copy()
	md.Set("x-user-id", p.ID.String())
	md.Set("x-user-role", p.Role)
	return metadata.NewOutgoingContext(ctx, md)
}

func principalFromContext(ctx context.Context) (identity.Principal, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	ids, roles := md.Get("x-user-id"), md.Get("x-user-role")
	invalid := status.Error(codes.Unauthenticated, "Некорректная учётная запись")
	if len(ids) != 1 || len(roles) != 1 {
		return identity.Principal{}, invalid
	}
	id, err := uuid.Parse(ids[0])
	if err != nil || id == uuid.Nil || id.String() != ids[0] {
		return identity.Principal{}, invalid
	}
	if roles[0] != "USER" && roles[0] != "SELLER" && roles[0] != "ADMIN" {
		return identity.Principal{}, invalid
	}
	return identity.Principal{ID: id, Role: roles[0]}, nil
}
