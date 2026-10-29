package transport

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func AuthInterceptor(apiKey string) grpc.UnaryServerInterceptor {
	expected := sha256.Sum256([]byte(apiKey))
	return func(ctx context.Context, request any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		keys := md.Get("x-api-key")
		if apiKey == "" || len(keys) != 1 {
			return nil, status.Error(codes.Unauthenticated, "valid API key required")
		}
		provided := sha256.Sum256([]byte(keys[0]))
		if subtle.ConstantTimeCompare(expected[:], provided[:]) != 1 {
			return nil, status.Error(codes.Unauthenticated, "valid API key required")
		}
		return handler(ctx, request)
	}
}
