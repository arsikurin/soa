package orders

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
	"marketplace/internal/api"
	"marketplace/internal/fault"
	"marketplace/internal/identity"
	"marketplace/internal/twopc"
)

const orderColumns = "id,user_id,status,total_amount::text,discount_amount::text,promo_code_id,created_at,updated_at"

func readOrder(ctx context.Context, q querier, id uuid.UUID, lock bool) (*api.OrderResponse, error) {
	out := &api.OrderResponse{Items: []api.OrderItemResponse{}}
	var status, total, discount string
	var promo *uuid.UUID
	sql := "SELECT " + orderColumns + " FROM orders WHERE id=$1"
	if lock {
		sql += " FOR UPDATE"
	}
	err := q.QueryRow(ctx, sql, id).Scan(&out.ID, &out.UserID, &status, &total, &discount, &promo, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, orderNotFound()
	}
	if err != nil {
		return nil, err
	}
	out.Status = api.OrderStatus(status)
	out.TotalAmount, err = decimal.NewFromString(total)
	if err != nil {
		return nil, err
	}
	out.DiscountAmount, err = decimal.NewFromString(discount)
	if err != nil {
		return nil, err
	}
	out.PromoCodeID.Null = promo == nil
	if promo != nil {
		out.PromoCodeID.Value = *promo
	}
	rows, err := q.Query(ctx, "SELECT id,product_id,quantity,price_at_order::text FROM order_items WHERE order_id=$1 ORDER BY id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item api.OrderItemResponse
		var price string
		if err = rows.Scan(&item.ID, &item.ProductID, &item.Quantity, &price); err != nil {
			return nil, err
		}
		item.PriceAtOrder, err = decimal.NewFromString(price)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, item)
	}
	return out, rows.Err()
}
func (s *Service) GetOrder(ctx context.Context, p identity.Principal, id uuid.UUID) (*api.OrderResponse, error) {
	if !orderRole(p) {
		return nil, fault.Denied()
	}
	return transact(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) (*api.OrderResponse, error) {
		out, err := readOrder(ctx, tx, id, false)
		if err != nil {
			return nil, err
		}
		if err = orderOwnership(p, out.UserID); err != nil {
			return nil, err
		}
		return out, nil
	})
}
func (s *Service) checkRate(ctx context.Context, tx pgx.Tx, user uuid.UUID, operation string) error {
	if s.rateLimit <= 0 {
		return nil
	}
	var limited bool
	err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM user_operations WHERE user_id=$1 AND operation_type=$2 AND created_at > clock_timestamp()-($3::double precision * interval '1 second'))", user, operation, s.rateLimit.Seconds()).Scan(&limited)
	if err != nil {
		return err
	}
	if limited {
		return fault.New(429, "ORDER_LIMIT_EXCEEDED", "Операция выполняется слишком часто")
	}
	return nil
}
func recordOperation(ctx context.Context, tx pgx.Tx, user uuid.UUID, operation string) error {
	_, err := tx.Exec(ctx, "INSERT INTO user_operations(user_id,operation_type,created_at) VALUES($1,$2,clock_timestamp())", user, operation)
	return err
}
func validateItems(items []api.OrderItemRequest) error {
	if len(items) < 1 || len(items) > 50 {
		return fault.Validation("items", "Количество позиций должно быть от 1 до 50")
	}
	seen := make(map[uuid.UUID]bool, len(items))
	for _, item := range items {
		if seen[item.ProductID] {
			return fault.Validation("items", "Товар не должен повторяться")
		}
		seen[item.ProductID] = true
		if item.Quantity < 1 || item.Quantity > 999 {
			return fault.Validation("items.quantity", "Количество должно быть от 1 до 999")
		}
	}
	return nil
}

func insertItems(ctx context.Context, tx pgx.Tx, id uuid.UUID, items []api.OrderItemResponse) error {
	for _, item := range items {
		if _, err := tx.Exec(ctx, "INSERT INTO order_items(id,order_id,product_id,quantity,price_at_order) VALUES($1,$2,$3,$4,$5::text::numeric)", item.ID, id, item.ProductID, item.Quantity, item.PriceAtOrder.String()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) CreateOrder(ctx context.Context, p identity.Principal, r *api.OrderCreate) (*api.OrderResponse, error) {
	if !orderRole(p) {
		return nil, fault.Denied()
	}
	if err := validateItems(r.Items); err != nil {
		return nil, err
	}
	id := uuid.New()
	if _, requested, ok := twopc.OperationFromContext(ctx); ok {
		id = requested
	}
	out, err := s.coordinate(ctx, id, func(ctx context.Context, tx pgx.Tx, gid string) (*api.OrderResponse, error) {
		if err := lockActor(ctx, tx, p.ID); err != nil {
			return nil, err
		}
		if err := s.checkRate(ctx, tx, p.ID, "CREATE_ORDER"); err != nil {
			return nil, err
		}
		var active bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM orders WHERE user_id=$1 AND status IN ('CREATED','PAYMENT_PENDING'))", p.ID).Scan(&active); err != nil {
			return nil, err
		}
		if active {
			return nil, activeOrder()
		}
		items, err := s.prepareCatalog(ctx, gid, nil, r.Items)
		if err != nil {
			return nil, err
		}
		subtotal := subtotalOf(items)
		var promoID any
		discount := decimal.Zero
		if r.PromoCode.Set {
			promo, err := lockPromo(ctx, tx, "code", r.PromoCode.Value)
			if err != nil {
				return nil, err
			}
			if err = checkPromo(ctx, tx, promo, false); err != nil {
				return nil, err
			}
			if subtotal.LessThan(promo.MinOrderAmount) {
				return nil, fault.New(422, "PROMO_CODE_MIN_AMOUNT", "Сумма заказа ниже минимальной для промокода")
			}
			discount = calculateDiscount(subtotal, promo.DiscountType, promo.DiscountValue)
			promoID = promo.ID
			if _, err = tx.Exec(ctx, "UPDATE promo_codes SET current_uses=current_uses+1 WHERE id=$1", promo.ID); err != nil {
				return nil, err
			}
		}
		if _, err = tx.Exec(ctx, "INSERT INTO orders(id,user_id,status,total_amount,discount_amount,promo_code_id) VALUES($1,$2,'CREATED',$3::text::numeric,$4::text::numeric,$5)", id, p.ID, subtotal.Sub(discount).String(), discount.String(), promoID); err != nil {
			return nil, err
		}
		if err = insertItems(ctx, tx, id, items); err != nil {
			return nil, err
		}
		if err = recordOperation(ctx, tx, p.ID, "CREATE_ORDER"); err != nil {
			return nil, err
		}
		return readOrder(ctx, tx, id, false)
	})
	if uniqueViolation(err, "orders_one_active_per_user_idx") {
		return nil, activeOrder()
	}
	return out, err
}
func (s *Service) UpdateOrder(ctx context.Context, p identity.Principal, id uuid.UUID, r *api.OrderUpdate) (*api.OrderResponse, error) {
	if !orderRole(p) {
		return nil, fault.Denied()
	}
	if err := validateItems(r.Items); err != nil {
		return nil, err
	}
	return s.coordinate(ctx, id, func(ctx context.Context, tx pgx.Tx, gid string) (*api.OrderResponse, error) {
		if err := lockActor(ctx, tx, p.ID); err != nil {
			return nil, err
		}
		old, err := readOrder(ctx, tx, id, true)
		if err != nil {
			return nil, err
		}
		if err = orderOwnership(p, old.UserID); err != nil {
			return nil, err
		}
		if old.Status != api.OrderStatusCREATED {
			return nil, invalidState()
		}
		if err = s.checkRate(ctx, tx, p.ID, "UPDATE_ORDER"); err != nil {
			return nil, err
		}
		items, err := s.prepareCatalog(ctx, gid, old.Items, r.Items)
		if err != nil {
			return nil, err
		}
		subtotal := subtotalOf(items)
		var promoID any
		discount := decimal.Zero
		if !old.PromoCodeID.Null {
			promo, err := lockPromo(ctx, tx, "id", old.PromoCodeID.Value)
			if err != nil {
				return nil, err
			}
			if err = checkPromo(ctx, tx, promo, true); err != nil {
				return nil, err
			}
			if subtotal.LessThan(promo.MinOrderAmount) {
				if _, err = tx.Exec(ctx, "UPDATE promo_codes SET current_uses=current_uses-1 WHERE id=$1", promo.ID); err != nil {
					return nil, err
				}
			} else {
				promoID = promo.ID
				discount = calculateDiscount(subtotal, promo.DiscountType, promo.DiscountValue)
			}
		}
		if _, err = tx.Exec(ctx, "UPDATE orders SET total_amount=$2::text::numeric,discount_amount=$3::text::numeric,promo_code_id=$4 WHERE id=$1", id, subtotal.Sub(discount).String(), discount.String(), promoID); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, "DELETE FROM order_items WHERE order_id=$1", id); err != nil {
			return nil, err
		}
		if err = insertItems(ctx, tx, id, items); err != nil {
			return nil, err
		}
		if err = recordOperation(ctx, tx, p.ID, "UPDATE_ORDER"); err != nil {
			return nil, err
		}
		return readOrder(ctx, tx, id, false)
	})
}
func (s *Service) CancelOrder(ctx context.Context, p identity.Principal, id uuid.UUID) (*api.OrderResponse, error) {
	if !orderRole(p) {
		return nil, fault.Denied()
	}
	return s.coordinate(ctx, id, func(ctx context.Context, tx pgx.Tx, gid string) (*api.OrderResponse, error) {
		if err := lockActor(ctx, tx, p.ID); err != nil {
			return nil, err
		}
		old, err := readOrder(ctx, tx, id, true)
		if err != nil {
			return nil, err
		}
		if err = orderOwnership(p, old.UserID); err != nil {
			return nil, err
		}
		if old.Status != api.OrderStatusCREATED && old.Status != api.OrderStatusPAYMENTPENDING {
			return nil, invalidState()
		}
		if _, err = s.prepareCatalog(ctx, gid, old.Items, nil); err != nil {
			return nil, err
		}
		if !old.PromoCodeID.Null {
			promo, err := lockPromo(ctx, tx, "id", old.PromoCodeID.Value)
			if err != nil {
				return nil, err
			}
			if _, err = tx.Exec(ctx, "UPDATE promo_codes SET current_uses=current_uses-1 WHERE id=$1", promo.ID); err != nil {
				return nil, err
			}
		}
		if _, err = tx.Exec(ctx, "UPDATE orders SET status='CANCELED' WHERE id=$1", id); err != nil {
			return nil, err
		}
		return readOrder(ctx, tx, id, false)
	})
}
func (s *Service) ChangeOrderStatus(ctx context.Context, p identity.Principal, id uuid.UUID, status api.OrderStatusUpdateStatus) (*api.OrderResponse, error) {
	if p.Role != "ADMIN" {
		return nil, fault.Denied()
	}
	return transact(ctx, s.db, pgx.TxOptions{}, func(tx pgx.Tx) (*api.OrderResponse, error) {
		if err := lockActor(ctx, tx, p.ID); err != nil {
			return nil, err
		}
		old, err := readOrder(ctx, tx, id, true)
		if err != nil {
			return nil, err
		}
		if !validTransition(old.Status, api.OrderStatus(status)) {
			return nil, invalidState()
		}
		if _, err = tx.Exec(ctx, "UPDATE orders SET status=$2 WHERE id=$1", id, string(status)); err != nil {
			return nil, err
		}
		return readOrder(ctx, tx, id, false)
	})
}

func subtotalOf(items []api.OrderItemResponse) decimal.Decimal {
	total := decimal.Zero
	for _, item := range items {
		total = total.Add(item.PriceAtOrder.Mul(decimal.NewFromInt(int64(item.Quantity))))
	}
	return total
}
