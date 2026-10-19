package catalog

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"marketplace/internal/fault"
	"marketplace/internal/identity"
	"marketplace/internal/twopc"
)

type UsersLookup interface {
	LookupRole(context.Context, uuid.UUID) (string, error)
}
type Service struct {
	db    *pgxpool.Pool
	users UsersLookup
}

func NewService(db *pgxpool.Pool, users UsersLookup) *Service { return &Service{db: db, users: users} }
func sellerRole(p identity.Principal) bool                    { return p.Role == "SELLER" || p.Role == "ADMIN" }
func productNotFound() error {
	return fault.New(404, "PRODUCT_NOT_FOUND", "Товар не найден")
}
func transact[T any](ctx context.Context, db *pgxpool.Pool, opts pgx.TxOptions, fn func(pgx.Tx) (T, error)) (result T, err error) {
	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return result, err
	}
	defer twopc.Rollback(tx)
	result, err = fn(tx)
	if err != nil {
		return result, err
	}
	err = tx.Commit(ctx)
	return result, err
}
func missingUser(err error) bool {
	var e *fault.Error
	return errors.Is(err, pgx.ErrNoRows) || (errors.As(err, &e) && e.Status == 404)
}
