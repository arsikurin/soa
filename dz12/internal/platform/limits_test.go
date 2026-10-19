package platform

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConcurrentRequestsAreBoundedAndHealthRemainsAvailable(t *testing.T) {
	gate := BoundRequests(1)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	method := &grpc.UnaryServerInfo{FullMethod: "/marketplace.v1.Orders/CreateOrder"}
	go func() {
		_, err := gate(context.Background(), nil, method, func(ctx context.Context, _ any) (any, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 30*time.Second {
				t.Error("request has no bounded deadline")
			}
			close(entered)
			<-release
			return nil, nil
		})
		done <- err
	}()
	<-entered
	_, err := gate(context.Background(), nil, method, func(context.Context, any) (any, error) { t.Error("busy request reached business"); return nil, nil })
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("unexpected overload result: %v", err)
	}
	_, err = gate(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/grpc.health.v1.Health/Check"}, func(context.Context, any) (any, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	_, err = gate(context.Background(), nil, method, func(context.Context, any) (any, error) { return nil, nil })
	if err != nil {
		t.Fatal("slot was not returned", err)
	}
}
