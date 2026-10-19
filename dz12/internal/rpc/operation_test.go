package rpc

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"marketplace/internal/api"
	"marketplace/internal/fault"
	"marketplace/internal/identity"
	"marketplace/internal/rpc/pb"
	"marketplace/internal/twopc"
)

type observedOperation struct {
	ctx     context.Context
	orderID uuid.UUID
}
type correlatedOrders struct {
	ordersFixture
	calls    atomic.Int32
	observed chan observedOperation
	failure  error
}

func (o *correlatedOrders) record(ctx context.Context, id uuid.UUID) {
	o.calls.Add(1)
	o.observed <- observedOperation{ctx: ctx, orderID: id}
}
func (o *correlatedOrders) CreateOrder(ctx context.Context, p identity.Principal, r *api.OrderCreate) (*api.OrderResponse, error) {
	_, id, _ := twopc.OperationFromContext(ctx)
	o.record(ctx, id)
	v, _ := o.ordersFixture.CreateOrder(ctx, p, r)
	v.ID = id
	return v, o.failure
}
func (o *correlatedOrders) UpdateOrder(ctx context.Context, p identity.Principal, id uuid.UUID, r *api.OrderUpdate) (*api.OrderResponse, error) {
	o.record(ctx, id)
	v, _ := o.ordersFixture.UpdateOrder(ctx, p, id, r)
	return v, o.failure
}
func (o *correlatedOrders) CancelOrder(ctx context.Context, p identity.Principal, id uuid.UUID) (*api.OrderResponse, error) {
	o.record(ctx, id)
	v, _ := o.ordersFixture.CancelOrder(ctx, p, id)
	return v, o.failure
}

func mutationCalls() map[string]func(context.Context, *OrdersClient) (*api.OrderResponse, error) {
	p := identity.Principal{ID: testID, Role: "USER"}
	return map[string]func(context.Context, *OrdersClient) (*api.OrderResponse, error){
		"create": func(ctx context.Context, c *OrdersClient) (*api.OrderResponse, error) {
			return c.CreateOrder(ctx, p, &api.OrderCreate{Items: []api.OrderItemRequest{{ProductID: testID, Quantity: 1}}})
		},
		"update": func(ctx context.Context, c *OrdersClient) (*api.OrderResponse, error) {
			return c.UpdateOrder(ctx, p, testID, &api.OrderUpdate{Items: []api.OrderItemRequest{{ProductID: testID, Quantity: 1}}})
		},
		"cancel": func(ctx context.Context, c *OrdersClient) (*api.OrderResponse, error) {
			return c.CancelOrder(ctx, p, testID)
		},
	}
}
func TestLostOrderMutationReplyRetainsOperationIDs(t *testing.T) {
	for name, call := range mutationCalls() {
		for _, code := range []codes.Code{codes.Unavailable, codes.DeadlineExceeded, codes.Canceled, codes.Unknown, codes.Internal} {
			t.Run(name+"/"+code.String(), func(t *testing.T) {
				backend := &correlatedOrders{observed: make(chan observedOperation, 1)}
				cc := testConnection(t, func(s *grpc.Server) { RegisterOrdersServer(s, backend) }, grpc.UnaryInterceptor(func(ctx context.Context, r any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
					_, err := h(ctx, r)
					if err != nil {
						return nil, err
					}
					return nil, status.Error(code, "lost reply after mutation")
				}))
				_, err := call(context.Background(), NewOrdersClient(cc))
				var f *fault.Error
				if !errors.As(err, &f) || f.Status != 503 || f.Code != "TRANSACTION_PENDING" {
					t.Fatalf("ambiguous mutation lost correlation: %v", err)
				}
				if backend.calls.Load() != 1 {
					t.Fatalf("mutation ran %d times", backend.calls.Load())
				}
				observed := <-backend.observed
				md, _ := metadata.FromIncomingContext(observed.ctx)
				txIDs, orderIDs := md.Get("x-transaction-id"), md.Get("x-order-id")
				if len(txIDs) != 1 || len(orderIDs) != 1 || f.Details["transaction_id"] != txIDs[0] || f.Details["order_id"] != orderIDs[0] || observed.orderID.String() != orderIDs[0] {
					t.Fatalf("operation IDs changed or missing: fault=%+v metadata=%v order=%s", f, md, observed.orderID)
				}
				contextTx, contextOrder, ok := twopc.OperationFromContext(observed.ctx)
				if !ok || contextTx.String() != txIDs[0] || contextOrder.String() != orderIDs[0] {
					t.Fatalf("server did not install the exact operation context: %s %s %t", contextTx, contextOrder, ok)
				}
				tx, txErr := uuid.Parse(txIDs[0])
				order, orderErr := uuid.Parse(orderIDs[0])
				if txErr != nil || orderErr != nil || tx == uuid.Nil || order == uuid.Nil || (name != "create" && order != testID) {
					t.Fatalf("invalid correlation IDs: %v", md)
				}
			})
		}
	}
}
func TestOrderMutationMetadataRejectedBeforeBusiness(t *testing.T) {
	txID := "00000000-0000-4000-8000-000000000abc"
	valid := metadata.Pairs("x-user-id", testID.String(), "x-user-role", "USER", "x-transaction-id", txID, "x-order-id", testID.String())
	cases := map[string]func(metadata.MD){
		"missing transaction":   func(m metadata.MD) { delete(m, "x-transaction-id") },
		"missing order":         func(m metadata.MD) { delete(m, "x-order-id") },
		"invalid transaction":   func(m metadata.MD) { m.Set("x-transaction-id", "bad") },
		"uppercase transaction": func(m metadata.MD) { m.Set("x-transaction-id", strings.ToUpper(txID)) },
		"zero order":            func(m metadata.MD) { m.Set("x-order-id", uuid.Nil.String()) },
		"duplicate transaction": func(m metadata.MD) { m.Append("x-transaction-id", txID) },
		"duplicate order":       func(m metadata.MD) { m.Append("x-order-id", testID.String()) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			backend := &correlatedOrders{observed: make(chan observedOperation, 1)}
			cc := testConnection(t, func(s *grpc.Server) { RegisterOrdersServer(s, backend) })
			md := valid.Copy()
			mutate(md)
			_, err := pb.NewOrdersClient(cc).CreateOrder(metadata.NewOutgoingContext(context.Background(), md), &pb.OrderCreateRequest{Items: []*pb.OrderItemRequest{{ProductId: testID.String(), Quantity: 1}}})
			if status.Code(err) != codes.InvalidArgument || backend.calls.Load() != 0 {
				t.Fatalf("invalid correlation reached business: %v calls%d", err, backend.calls.Load())
			}
		})
	}
	for _, name := range []string{"update", "cancel"} {
		t.Run("mismatched order/"+name, func(t *testing.T) {
			backend := &correlatedOrders{observed: make(chan observedOperation, 1)}
			cc := testConnection(t, func(s *grpc.Server) { RegisterOrdersServer(s, backend) })
			ctx := metadata.NewOutgoingContext(context.Background(), valid)
			client := pb.NewOrdersClient(cc)
			var err error
			if name == "update" {
				_, err = client.UpdateOrder(ctx, &pb.OrderUpdateRequest{Id: uuid.NewString(), Items: []*pb.OrderItemRequest{{ProductId: testID.String(), Quantity: 1}}})
			} else {
				_, err = client.CancelOrder(ctx, &pb.IDRequest{Id: uuid.NewString()})
			}
			if status.Code(err) != codes.InvalidArgument || backend.calls.Load() != 0 {
				t.Fatalf("mismatched order reached business: %v calls%d", err, backend.calls.Load())
			}
		})
	}
}
func TestOrderMutationTypedFaultIsNotChangedToPending(t *testing.T) {
	for name, call := range mutationCalls() {
		t.Run(name, func(t *testing.T) {
			want := fault.Validation("items[0].quantity", "Недостаточный остаток")
			backend := &correlatedOrders{observed: make(chan observedOperation, 1), failure: want}
			cc := testConnection(t, func(s *grpc.Server) { RegisterOrdersServer(s, backend) })
			_, err := call(context.Background(), NewOrdersClient(cc))
			var f *fault.Error
			if !errors.As(err, &f) || f.Code != want.Code || f.Status != 400 || f.Details == nil || backend.calls.Load() != 1 {
				t.Fatalf("typed domain fault changed: %v", err)
			}
		})
	}
}

func TestActualCancellationAfterMutationRetainsOperationIDs(t *testing.T) {
	backend := &correlatedOrders{observed: make(chan observedOperation, 1)}
	cc := testConnection(t, func(s *grpc.Server) { RegisterOrdersServer(s, backend) }, grpc.UnaryInterceptor(func(ctx context.Context, r any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		_, err := h(ctx, r)
		if err != nil {
			return nil, err
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := mutationCalls()["create"](ctx, NewOrdersClient(cc)); done <- err }()
	var observed observedOperation
	select {
	case observed = <-backend.observed:
	case <-time.After(5 * time.Second):
		t.Fatal("backend was not reached before cancellation")
	}
	cancel()
	err := <-done
	var f *fault.Error
	tx, order, ok := twopc.OperationFromContext(observed.ctx)
	if !errors.As(err, &f) || f.Status != 503 || f.Code != "TRANSACTION_PENDING" || !ok || f.Details["transaction_id"] != tx.String() || f.Details["order_id"] != order.String() || backend.calls.Load() != 1 {
		t.Fatalf("actual canceled reply lost operation IDs: %v", err)
	}
}

func TestOperationMetadataRequiresValidPrincipalFirst(t *testing.T) {
	backend := &correlatedOrders{observed: make(chan observedOperation, 1)}
	cc := testConnection(t, func(s *grpc.Server) { RegisterOrdersServer(s, backend) })
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("x-user-id", testID.String(), "x-user-role", "ROOT", "x-transaction-id", uuid.NewString(), "x-order-id", uuid.NewString()))
	_, err := pb.NewOrdersClient(cc).CreateOrder(ctx, &pb.OrderCreateRequest{Items: []*pb.OrderItemRequest{{ProductId: testID.String(), Quantity: 1}}})
	if status.Code(err) != codes.Unauthenticated || backend.calls.Load() != 0 {
		t.Fatalf("operation context bypassed principal: %v", err)
	}
}

func TestOrderAdmissionCapacityRemainsServiceUnavailable(t *testing.T) {
	backend := &correlatedOrders{observed: make(chan observedOperation, 1)}
	cc := testConnection(t, func(s *grpc.Server) { RegisterOrdersServer(s, backend) }, grpc.UnaryInterceptor(func(context.Context, any, *grpc.UnaryServerInfo, grpc.UnaryHandler) (any, error) {
		return nil, status.Error(codes.ResourceExhausted, "admission full")
	}))
	_, err := mutationCalls()["create"](context.Background(), NewOrdersClient(cc))
	var f *fault.Error
	if !errors.As(err, &f) || f.Status != 503 || f.Code != "SERVICE_UNAVAILABLE" || backend.calls.Load() != 0 {
		t.Fatalf("admission failure incorrectly pending: %v", err)
	}
}
