package orders

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"marketplace/internal/api"
	"marketplace/internal/fault"
	"marketplace/internal/twopc"
)

func TestCorrelatedOperationPreservesIDsAndRejectsReplay(t *testing.T) {
	f := newFixture(t)
	p := f.product("10", 6)
	tx, oid := uuid.New(), uuid.New()
	ctx := twopc.WithOperation(f.ctx, tx, oid)
	order, err := f.svc.CreateOrder(ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(p, 2)}})
	if err != nil {
		t.Fatal(err)
	}
	if order.ID != oid {
		t.Fatalf("order=%s want contextual=%s", order.ID, oid)
	}
	var stored uuid.UUID
	if err = f.db.QueryRow(f.ctx, "SELECT order_id FROM transaction_journal WHERE id=$1", tx).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != oid {
		t.Fatal("journal correlation changed")
	}
	_, err = f.svc.UpdateOrder(ctx, f.user, oid, &api.OrderUpdate{Items: []api.OrderItemRequest{item(p, 4)}})
	mustCode(t, err, "TRANSACTION_CONFLICT")
	_, err = f.svc.CancelOrder(ctx, f.user, oid)
	mustCode(t, err, "TRANSACTION_CONFLICT")
	f.stock(p.ID, 4)
	got, err := f.svc.GetOrder(f.ctx, f.user, oid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != api.OrderStatusCREATED || got.Items[0].Quantity != 2 {
		t.Fatal("replayed operation mutated order")
	}
	mismatch := twopc.WithOperation(f.ctx, uuid.New(), uuid.New())
	_, err = f.svc.UpdateOrder(mismatch, f.user, oid, &api.OrderUpdate{Items: []api.OrderItemRequest{item(p, 1)}})
	mustCode(t, err, "VALIDATION_ERROR")
}
func TestCorrelatedAbortCannotBeReplayed(t *testing.T) {
	f := newFixture(t)
	p := f.product("10", 4)
	tx, oid := uuid.New(), uuid.New()
	ctx := twopc.WithOperation(f.ctx, tx, oid)
	_, err := f.svc.CreateOrder(ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(p, 2)}, PromoCode: api.NewOptString("MISSING")})
	mustCode(t, err, "PROMO_CODE_INVALID")
	var decision string
	if err = f.db.QueryRow(f.ctx, "SELECT decision FROM transaction_journal WHERE id=$1 AND order_id=$2", tx, oid).Scan(&decision); err != nil {
		t.Fatal(err)
	}
	if decision != "ABORT" {
		t.Fatal("business error not durably aborted")
	}
	_, err = f.svc.CreateOrder(ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(p, 1)}})
	mustCode(t, err, "TRANSACTION_CONFLICT")
	f.stock(p.ID, 4)
}
func TestNilCorrelatedIDsAreRejectedWithoutReplacement(t *testing.T) {
	f := newFixture(t)
	p := f.product("10", 4)
	for _, ids := range [][2]uuid.UUID{{uuid.Nil, uuid.New()}, {uuid.New(), uuid.Nil}} {
		_, err := f.svc.CreateOrder(twopc.WithOperation(f.ctx, ids[0], ids[1]), f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(p, 1)}})
		mustCode(t, err, "VALIDATION_ERROR")
	}
	f.stock(p.ID, 4)
}

type delayedCompletion struct{ CatalogParticipant }

func (p delayedCompletion) ResolveReservation(ctx context.Context, id string, commit bool) error {
	if commit {
		<-ctx.Done()
		return ctx.Err()
	}
	return p.CatalogParticipant.ResolveReservation(ctx, id, commit)
}
func TestPendingCorrelationReturnsBeforeTransportDeadline(t *testing.T) {
	f := newFixture(t)
	p := f.product("10", 4)
	tx, oid := uuid.New(), uuid.New()
	outer, cancel := context.WithTimeout(f.ctx, 900*time.Millisecond)
	defer cancel()
	ctx := twopc.WithOperation(outer, tx, oid)
	svc := NewService(f.db, delayedCompletion{f.svc.productService}, 0)
	started := time.Now()
	_, err := svc.CreateOrder(ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(p, 2)}})
	mustCode(t, err, "TRANSACTION_PENDING")
	if outer.Err() != nil {
		t.Fatalf("pending lost behind outer deadline after %s", time.Since(started))
	}
	var problem *fault.Error
	if !errors.As(err, &problem) {
		t.Fatal(err)
	}
	if problem.Details["transaction_id"] != tx.String() || problem.Details["order_id"] != oid.String() {
		t.Fatalf("pending correlation=%v", problem.Details)
	}
	deadline, _ := outer.Deadline()
	if time.Until(deadline) < 50*time.Millisecond {
		t.Fatalf("reply left only %s", time.Until(deadline))
	}
	var decision string
	if err = f.db.QueryRow(f.ctx, "SELECT decision FROM transaction_journal WHERE id=$1", tx).Scan(&decision); err != nil {
		t.Fatal(err)
	}
	if decision != "COMMIT" {
		t.Fatal("postdecision timeout changed commit")
	}
	if err = f.svc.Recover(f.ctx); err != nil {
		t.Fatal(err)
	}
	order, err := f.svc.GetOrder(f.ctx, f.user, oid)
	if err != nil {
		t.Fatal(err)
	}
	if order.ID != oid {
		t.Fatal("recovery lost correlated order")
	}
	f.stock(p.ID, 2)
}

type delayedPrepare struct{ CatalogParticipant }

func (p delayedPrepare) PrepareReservation(ctx context.Context, id string, old []api.OrderItemResponse, next []api.OrderItemRequest) ([]api.OrderItemResponse, error) {
	if _, err := p.CatalogParticipant.PrepareReservation(ctx, id, old, next); err != nil {
		return nil, err
	}
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestPredecisionDeadlineBoundsBusinessCallback(t *testing.T) {
	f := newFixture(t)
	p := f.product("10", 4)
	tx, oid := uuid.New(), uuid.New()
	outer, cancel := context.WithTimeout(f.ctx, 900*time.Millisecond)
	defer cancel()
	ctx := twopc.WithOperation(outer, tx, oid)
	svc := NewService(f.db, delayedPrepare{f.svc.productService}, 0)
	_, err := svc.CreateOrder(ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(p, 2)}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v want predecision timeout", err)
	}
	if outer.Err() != nil {
		t.Fatal("business callback consumed the transport reply budget")
	}
	var decision string
	if err = f.db.QueryRow(f.ctx, "SELECT decision FROM transaction_journal WHERE id=$1 AND order_id=$2", tx, oid).Scan(&decision); err != nil {
		t.Fatal(err)
	}
	if decision != "ABORT" {
		t.Fatal("predecision timeout was not aborted")
	}
	f.stock(p.ID, 4)
	assertNoPrepared(t, f)
}
