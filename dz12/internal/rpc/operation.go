package rpc

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"marketplace/internal/api"
	"marketplace/internal/fault"
	"marketplace/internal/rpc/pb"
	"marketplace/internal/twopc"
)

func newOperation(ctx context.Context, orderID uuid.UUID) (context.Context, uuid.UUID) {
	transactionID := uuid.New()
	md, _ := metadata.FromOutgoingContext(ctx)
	md = md.Copy()
	md.Set("x-transaction-id", transactionID.String())
	md.Set("x-order-id", orderID.String())
	return metadata.NewOutgoingContext(ctx, md), transactionID
}

func operationFromMetadata(ctx context.Context, expectedOrderID string) (context.Context, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	readID := func(key string) (uuid.UUID, error) {
		values := md.Get(key)
		if len(values) == 1 {
			id, err := uuid.Parse(values[0])
			if err == nil && id != uuid.Nil && id.String() == values[0] {
				return id, nil
			}
		}
		return uuid.Nil, toRPCError(fault.Validation(key, "Требуется единственный канонический UUID операции"))
	}
	transactionID, err := readID("x-transaction-id")
	if err != nil {
		return ctx, err
	}
	orderID, err := readID("x-order-id")
	if err != nil {
		return ctx, err
	}
	if expectedOrderID != "" && orderID.String() != expectedOrderID {
		return ctx, toRPCError(fault.Validation("x-order-id", "Идентификатор операции не совпадает с заказом запроса"))
	}
	return twopc.WithOperation(ctx, transactionID, orderID), nil
}

func uncertainOperation(transactionID, orderID uuid.UUID) error {
	return &fault.Error{Status: 503, Code: "TRANSACTION_PENDING", Message: "Результат операции пока не подтверждён", Details: map[string]any{"transaction_id": transactionID.String(), "order_id": orderID.String()}}
}
func decodeOrderMutation(v *pb.Order, err error, transactionID, orderID uuid.UUID) (*api.OrderResponse, error) {
	if err != nil {
		s := status.Convert(err)
		for _, detail := range s.Details() {
			if _, ok := detail.(*pb.BusinessError); ok {
				return nil, fromRPCError(err)
			}
		}
		switch s.Code() {
		case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled, codes.Unknown, codes.Internal:
			return nil, uncertainOperation(transactionID, orderID)
		}
		return nil, fromRPCError(err)
	}
	result, err := orderFromProto(v)
	if err != nil || result.ID != orderID {
		return nil, uncertainOperation(transactionID, orderID)
	}
	return result, nil
}
