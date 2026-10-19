package orders

import (
	"github.com/shopspring/decimal"
	"marketplace/internal/api"
	"testing"
)

func TestDiscountArithmetic(t *testing.T) {
	for _, tt := range []struct {
		name, subtotal, value, want string
		kind                        api.DiscountType
	}{
		{"seventy percent", "100.00", "70", "70", api.DiscountTypePERCENTAGE},
		{"one cent stays below seventy percent", "0.01", "70", "0", api.DiscountTypePERCENTAGE},
		{"two cents stay below seventy percent", "0.02", "70", "0.01", api.DiscountTypePERCENTAGE},
		{"rounding cannot exceed seventy percent", "1.01", "70", "0.70", api.DiscountTypePERCENTAGE},
		{"over seventy ignored", "100.00", "70.01", "0", api.DiscountTypePERCENTAGE},
		{"round half cent", "1.05", "10", "0.11", api.DiscountTypePERCENTAGE},
		{"fixed capped", "12.34", "20", "12.34", api.DiscountTypeFIXEDAMOUNT},
		{"fixed exact", "99.99", "1.23", "1.23", api.DiscountTypeFIXEDAMOUNT},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateDiscount(decimal.RequireFromString(tt.subtotal), tt.kind, decimal.RequireFromString(tt.value))
			if !got.Equal(decimal.RequireFromString(tt.want)) {
				t.Fatalf("discount = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestMoneyRange(t *testing.T) {
	for _, tt := range []struct {
		value string
		valid bool
	}{{"9999999999.99", true}, {"10000000000", false}, {"-0.01", false}, {"0", true}, {"1.001", false}} {
		if got := validMoney(decimal.RequireFromString(tt.value)); got != tt.valid {
			t.Errorf("validMoney(%s)=%t", tt.value, got)
		}
	}
}
func TestOrderTransitions(t *testing.T) {
	states := []api.OrderStatus{api.OrderStatusCREATED, api.OrderStatusPAYMENTPENDING, api.OrderStatusPAID, api.OrderStatusSHIPPED, api.OrderStatusCOMPLETED, api.OrderStatusCANCELED}
	for i, from := range states {
		for j, to := range states {
			want := i < 4 && j == i+1
			if got := validTransition(from, to); got != want {
				t.Errorf("transition %s -> %s=%t, want %t", from, to, got, want)
			}
		}
	}
}
