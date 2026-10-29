package transport

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestAuthProtectsEveryMethod(t *testing.T) {
	for _, method := range []string{"SearchFlights", "GetFlight", "ReserveSeats", "ReleaseReservation", "UpdateFlight", "OtherMethod"} {
		for _, tc := range []struct {
			name string
			keys []string
			want codes.Code
		}{
			{"missing", nil, codes.Unauthenticated},
			{"invalid", []string{"wrong"}, codes.Unauthenticated},
			{"empty", []string{""}, codes.Unauthenticated},
			{"duplicate", []string{"secret", "secret"}, codes.Unauthenticated},
			{"valid", []string{"secret"}, codes.OK},
		} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{"x-api-key": tc.keys})
				called := false
				response, err := AuthInterceptor("secret")(ctx, "request", &grpc.UnaryServerInfo{FullMethod: "/flight.v1.FlightService/" + method}, func(context.Context, any) (any, error) {
					called = true
					return "response", nil
				})
				if status.Code(err) != tc.want || called != (tc.want == codes.OK) {
					t.Fatalf("got response=%v error=%v handler=%t", response, err, called)
				}
			})
		}
	}
}

func TestAuthRejectsEmptyServerKey(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-api-key", ""))
	_, err := AuthInterceptor("")(ctx, nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
		t.Fatal("handler reached with empty server credentials")
		return nil, nil
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("got %v", err)
	}
}
