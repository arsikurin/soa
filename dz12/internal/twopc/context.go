package twopc

import (
	"context"
	"github.com/google/uuid"
)

type operationKey struct{}
type operation struct{ transactionID, orderID uuid.UUID }

func WithOperation(ctx context.Context, transactionID uuid.UUID, orderID uuid.UUID) context.Context {
	return context.WithValue(ctx, operationKey{}, operation{transactionID: transactionID, orderID: orderID})
}
func OperationFromContext(ctx context.Context) (transactionID uuid.UUID, orderID uuid.UUID, ok bool) {
	op, ok := ctx.Value(operationKey{}).(operation)
	return op.transactionID, op.orderID, ok
}
