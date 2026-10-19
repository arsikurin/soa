package orders

import (
	"github.com/shopspring/decimal"
	"marketplace/internal/api"
)

var maxMoney = decimal.RequireFromString("9999999999.99")

func validMoney(value decimal.Decimal) bool {
	return !value.IsNegative() && value.LessThanOrEqual(maxMoney) && value.Equal(value.Round(2))
}

func calculateDiscount(subtotal decimal.Decimal, kind api.DiscountType, value decimal.Decimal) decimal.Decimal {
	if kind == api.DiscountTypePERCENTAGE {
		if value.GreaterThan(decimal.NewFromInt(70)) {
			return decimal.Zero
		}
		discount := subtotal.Mul(value).Div(decimal.NewFromInt(100)).Round(2)
		cap := subtotal.Mul(decimal.NewFromInt(70)).Div(decimal.NewFromInt(100)).Truncate(2)
		return decimal.Min(discount, cap)
	}
	return decimal.Min(value, subtotal)
}

func validTransition(from, to api.OrderStatus) bool {
	switch from {
	case api.OrderStatusCREATED:
		return to == api.OrderStatusPAYMENTPENDING
	case api.OrderStatusPAYMENTPENDING:
		return to == api.OrderStatusPAID
	case api.OrderStatusPAID:
		return to == api.OrderStatusSHIPPED
	case api.OrderStatusSHIPPED:
		return to == api.OrderStatusCOMPLETED
	default:
		return false
	}
}
