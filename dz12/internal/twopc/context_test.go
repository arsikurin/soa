package twopc

import (
	"context"
	"github.com/google/uuid"
	"testing"
)

func TestOperationPreservesSuppliedIDsAndPresence(t *testing.T) {
	tx, order := uuid.New(), uuid.New()
	gotTx, gotOrder, ok := OperationFromContext(WithOperation(context.Background(), tx, order))
	if !ok || gotTx != tx || gotOrder != order {
		t.Fatal("operation correlation was lost")
	}
	_, _, ok = OperationFromContext(context.Background())
	if ok {
		t.Fatal("operation fabricated")
	}
	_, _, ok = OperationFromContext(WithOperation(context.Background(), uuid.Nil, uuid.Nil))
	if !ok {
		t.Fatal("invalid supplied operation must remain detectable")
	}
}
