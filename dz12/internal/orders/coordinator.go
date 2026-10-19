package orders

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"marketplace/internal/api"
	"marketplace/internal/fault"
	"marketplace/internal/twopc"
)

func pending(id string, orderID uuid.UUID) error {
	e := fault.New(503, "TRANSACTION_PENDING", "Результат транзакции уточняется; проверьте заказ после восстановления")
	e.Details = map[string]any{"transaction_id": id, "order_id": orderID.String()}
	return e
}

func (s *Service) prepareCatalog(ctx context.Context, id string, old []api.OrderItemResponse, next []api.OrderItemRequest) ([]api.OrderItemResponse, error) {
	bounded, cancel := context.WithTimeout(ctx, twopc.Timeout)
	defer cancel()
	return s.catalog.PrepareReservation(bounded, id, old, next)
}

func (s *Service) coordinate(ctx context.Context, orderID uuid.UUID, fn func(context.Context, pgx.Tx, string) (*api.OrderResponse, error)) (*api.OrderResponse, error) {
	budget := newOperationBudget(ctx)
	ctx, cancel := budget.predecisionContext()
	defer cancel()
	id := uuid.NewString()
	if transactionID, requestedOrder, ok := twopc.OperationFromContext(ctx); ok {
		if transactionID == uuid.Nil {
			return nil, fault.Validation("transaction_id", "Идентификатор транзакции обязателен")
		}
		if requestedOrder == uuid.Nil || requestedOrder != orderID {
			return nil, fault.Validation("order_id", "Идентификатор заказа не соответствует операции")
		}
		id = transactionID.String()
	}
	c, err := s.db.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		release, stop := budget.releaseContext()
		defer stop()
		twopc.ReleaseContext(release, c, "orders", id)
	}()
	if _, err = twopc.Lock(ctx, c.Conn(), "orders", id, false); err != nil {
		return nil, err
	}
	if _, err = c.Exec(ctx, "INSERT INTO transaction_journal(id,order_id) VALUES($1,$2)", id, orderID); err != nil {
		if uniqueViolation(err, "transaction_journal_pkey") {
			return nil, fault.New(409, "TRANSACTION_CONFLICT", "Идентификатор операции уже использован")
		}
		return nil, err
	}
	tx, err := twopc.Begin(ctx, c.Conn())
	if err != nil {
		cleanup, stop := budget.completionContext()
		defer stop()
		s.abort(cleanup, c.Conn(), id)
		return nil, err
	}
	out, err := fn(ctx, tx, id)
	if err == nil {
		_, err = tx.Exec(ctx, "INSERT INTO transaction_receipts(id) VALUES($1)", id)
	}
	if err == nil {
		err = twopc.Prepare(ctx, tx, "orders", id)
	}
	if err != nil {
		cleanup, stop := budget.completionContext()
		defer stop()
		twopc.RollbackContext(cleanup, tx)
		s.abort(cleanup, c.Conn(), id)
		return nil, err
	}
	if _, err = c.Exec(ctx, "UPDATE transaction_journal SET decision='COMMIT' WHERE id=$1 AND decision='PREPARING'", id); err != nil {
		return nil, pending(id, orderID)
	}
	cleanup, stop := budget.completionContext()
	defer stop()
	if err = s.complete(cleanup, c.Conn(), id, true); err != nil {
		return nil, pending(id, orderID)
	}
	return out, nil
}

func (s *Service) abort(ctx context.Context, c *pgx.Conn, id string) {
	if _, err := c.Exec(ctx, "UPDATE transaction_journal SET decision='ABORT' WHERE id=$1 AND decision='PREPARING'", id); err != nil {
		slog.Warn("transaction abort awaits recovery", "transaction_id", id, "error", err)
		return
	}
	if err := s.complete(ctx, c, id, false); err != nil {
		slog.Warn("transaction abort awaits recovery", "transaction_id", id, "error", err)
	}
}

func (s *Service) complete(ctx context.Context, c *pgx.Conn, id string, commit bool) error {
	if !commit {
		if err := twopc.Resolve(ctx, c, "orders", id, false); err != nil {
			return err
		}
	}
	rpcCtx, cancel := context.WithTimeout(ctx, twopc.Timeout)
	err := s.catalog.ResolveReservation(rpcCtx, id, commit)
	cancel()
	if err != nil {
		return err
	}
	if commit {
		if err = twopc.Resolve(ctx, c, "orders", id, true); err != nil {
			return err
		}
	}
	_, err = c.Exec(ctx, "UPDATE transaction_journal SET completed=true WHERE id=$1", id)
	return err
}

func (s *Service) Recover(ctx context.Context) error {
	rows, err := s.db.Query(ctx, "SELECT id FROM transaction_journal WHERE NOT completed ORDER BY created_at,id")
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		if err = s.recoverOne(ctx, id); err != nil {
			failures = append(failures, fmt.Errorf("recover %s: %w", id, err))
		}
	}
	return errors.Join(failures...)
}
func (s *Service) recoverOne(parent context.Context, id string) error {
	ctx, cancel := context.WithTimeout(parent, 2*twopc.Timeout)
	defer cancel()
	c, err := s.db.Acquire(ctx)
	if err != nil {
		return err
	}
	locked, err := twopc.Lock(ctx, c.Conn(), "orders", id, true)
	if err != nil || !locked {
		c.Release()
		return err
	}
	defer twopc.Release(c, "orders", id)
	var decision string
	var done bool
	if err = c.QueryRow(ctx, "SELECT decision,completed FROM transaction_journal WHERE id=$1", id).Scan(&decision, &done); err != nil {
		return err
	}
	if done {
		return nil
	}
	if decision == "PREPARING" {
		if _, err = c.Exec(ctx, "UPDATE transaction_journal SET decision='ABORT' WHERE id=$1 AND decision='PREPARING'", id); err != nil {
			return err
		}
		decision = "ABORT"
	}
	return s.complete(ctx, c.Conn(), id, decision == "COMMIT")
}
func (s *Service) RunRecovery(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := s.Recover(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("transaction recovery retry", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
