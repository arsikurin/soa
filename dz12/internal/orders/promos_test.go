package orders

import (
	"marketplace/internal/api"
	"testing"
	"time"
)

func TestPromoValidityIncludesBothTimeBounds(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	promo := &api.PromoCodeResponse{Active: true, MaxUses: 1, ValidFrom: start, ValidUntil: end}
	for _, tc := range []struct {
		now  time.Time
		want bool
	}{{start, true}, {end, true}, {start.Add(-time.Nanosecond), false}, {end.Add(time.Nanosecond), false}} {
		if got := promoValidAt(promo, false, tc.now); got != tc.want {
			t.Errorf("valid at %s=%t,want%t", tc.now, got, tc.want)
		}
	}
}
