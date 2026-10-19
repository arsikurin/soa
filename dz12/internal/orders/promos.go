package orders

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
	"marketplace/internal/api"
	"marketplace/internal/fault"
	"marketplace/internal/identity"
)

const promoColumns = "id,code,discount_type,discount_value::text,min_order_amount::text,max_uses,current_uses,valid_from,valid_until,active,created_at,updated_at"

func scanPromo(row pgx.Row) (*api.PromoCodeResponse, error) {
	p := new(api.PromoCodeResponse)
	var kind, value, min string
	if err := row.Scan(&p.ID, &p.Code, &kind, &value, &min, &p.MaxUses, &p.CurrentUses, &p.ValidFrom, &p.ValidUntil, &p.Active, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	var err error
	p.DiscountType = api.DiscountType(kind)
	p.DiscountValue, err = decimal.NewFromString(value)
	if err != nil {
		return nil, err
	}
	p.MinOrderAmount, err = decimal.NewFromString(min)
	return p, err
}
func (s *Service) CreatePromoCode(ctx context.Context, p identity.Principal, r *api.PromoCodeCreate) (*api.PromoCodeResponse, error) {
	if !sellerRole(p) {
		return nil, fault.Denied()
	}
	if !r.ValidUntil.After(r.ValidFrom) {
		return nil, fault.Validation("valid_until", "Дата окончания должна быть после даты начала")
	}
	out, err := scanPromo(s.db.QueryRow(ctx, "INSERT INTO promo_codes(code,discount_type,discount_value,min_order_amount,max_uses,valid_from,valid_until,active) VALUES ($1,$2,$3::text::numeric,$4::text::numeric,$5,$6,$7,$8) RETURNING "+promoColumns, r.Code, string(r.DiscountType), r.DiscountValue.String(), r.MinOrderAmount.String(), r.MaxUses, r.ValidFrom, r.ValidUntil, r.Active.Or(true)))
	if uniqueViolation(err, "promo_codes_code_key") {
		return nil, fault.New(409, "PROMO_CODE_EXISTS", "Промокод с таким кодом уже существует")
	}
	return out, err
}

func lockPromo(ctx context.Context, tx pgx.Tx, selector string, value any) (*api.PromoCodeResponse, error) {
	p, err := scanPromo(tx.QueryRow(ctx, "SELECT "+promoColumns+" FROM promo_codes WHERE "+selector+"=$1 FOR UPDATE", value))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, promoInvalid()
	}
	return p, err
}
func checkPromo(ctx context.Context, tx pgx.Tx, p *api.PromoCodeResponse, ownUse bool) error {
	var now time.Time
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return err
	}
	if !promoValidAt(p, ownUse, now) {
		return promoInvalid()
	}
	return nil
}
func promoValidAt(p *api.PromoCodeResponse, ownUse bool, now time.Time) bool {
	uses := p.CurrentUses
	if ownUse {
		uses--
	}
	return p.Active && !now.Before(p.ValidFrom) && !now.After(p.ValidUntil) && uses < p.MaxUses
}
