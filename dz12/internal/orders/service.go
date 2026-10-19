package orders

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"marketplace/internal/api"
	"marketplace/internal/fault"
	"marketplace/internal/identity"
	"marketplace/internal/twopc"
)

type Service struct {
	db        *pgxpool.Pool
	rateLimit time.Duration
	catalog   CatalogParticipant
}

type CatalogParticipant interface {
	PrepareReservation(context.Context, string, []api.OrderItemResponse, []api.OrderItemRequest) ([]api.OrderItemResponse, error)
	ResolveReservation(context.Context, string, bool) error
}

func NewService(db *pgxpool.Pool, catalog CatalogParticipant, rateLimit time.Duration) *Service {
	return &Service{db: db, catalog: catalog, rateLimit: rateLimit}
}

type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
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
	if err = tx.Commit(ctx); err != nil {
		return result, err
	}
	return result, nil
}
func sellerRole(p identity.Principal) bool { return p.Role == "SELLER" || p.Role == "ADMIN" }
func orderRole(p identity.Principal) bool  { return p.Role == "USER" || p.Role == "ADMIN" }
func lockActor(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	if _, err := tx.Exec(ctx, "INSERT INTO actor_locks(id) VALUES($1) ON CONFLICT DO NOTHING", id); err != nil {
		return err
	}
	var actor uuid.UUID
	return tx.QueryRow(ctx, "SELECT id FROM actor_locks WHERE id=$1 FOR UPDATE", id).Scan(&actor)
}
func orderOwnership(p identity.Principal, owner uuid.UUID) error {
	if p.Role != "ADMIN" && p.ID != owner {
		return fault.New(403, "ORDER_OWNERSHIP_VIOLATION", "Заказ принадлежит другому пользователю")
	}
	return nil
}
func orderNotFound() error { return fault.New(404, "ORDER_NOT_FOUND", "Заказ не найден") }
func invalidState() error {
	return fault.New(409, "INVALID_STATE_TRANSITION", "Операция недоступна в текущем состоянии заказа")
}
func promoInvalid() error {
	return fault.New(422, "PROMO_CODE_INVALID", "Промокод недоступен")
}
func activeOrder() error {
	return fault.New(409, "ORDER_HAS_ACTIVE", "У пользователя уже есть активный заказ")
}
func uniqueViolation(err error, constraint string) bool {
	var e *pgconn.PgError
	return errors.As(err, &e) && e.Code == "23505" && e.ConstraintName == constraint
}
