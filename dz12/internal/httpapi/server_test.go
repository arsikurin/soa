package httpapi

import (
	"github.com/google/uuid"
	"io"
	"log/slog"
	"marketplace/internal/auth"
	"marketplace/internal/identity"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestContractRejectsInputBeforeBusiness(t *testing.T) {
	tokens, _ := auth.NewTokens("0123456789abcdef0123456789abcdef")
	seller, _, _ := tokens.Issue(identity.Principal{ID: uuid.New(), Role: "SELLER"}, "access", time.Minute)
	user, _, _ := tokens.Issue(identity.Principal{ID: uuid.New(), Role: "USER"}, "access", time.Minute)
	expired, _, _ := tokens.Issue(identity.Principal{ID: uuid.New(), Role: "USER"}, "access", -time.Minute)
	refresh, _, _ := tokens.Issue(identity.Principal{ID: uuid.New(), Role: "USER"}, "refresh", time.Minute)
	h, err := New(nil, nil, nil, tokens, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, path, body, token, code, field string
		status                                       int
	}{
		{"missing token", "GET", "/products", "", "", "TOKEN_INVALID", "", 401},
		{"expired token", "GET", "/products", "", expired, "TOKEN_EXPIRED", "", 401},
		{"refresh as access", "GET", "/products", "", refresh, "TOKEN_INVALID", "", 401},
		{"name and stock", "POST", "/products", `{"name":"","price":1,"stock":-1,"category":"x","status":"ACTIVE"}`, seller, "VALIDATION_ERROR", "name", 400},
		{"decimal minimum", "POST", "/products", `{"name":"x","price":0,"stock":0,"category":"x","status":"ACTIVE"}`, seller, "VALIDATION_ERROR", "price", 400},
		{"decimal precision", "POST", "/products", `{"name":"x","price":0.011,"stock":0,"category":"x","status":"ACTIVE"}`, seller, "VALIDATION_ERROR", "price", 400},
		{"huge exponent", "POST", "/products", `{"name":"x","price":1e100000,"stock":0,"category":"x","status":"ACTIVE"}`, seller, "VALIDATION_ERROR", "price", 400},
		{"tiny exponent", "POST", "/products", `{"name":"x","price":1e-100000,"stock":0,"category":"x","status":"ACTIVE"}`, seller, "VALIDATION_ERROR", "price", 400},
		{"duplicate with huge exponent", "POST", "/products", `{"name":"x","price":1e100000,"price":1,"stock":0,"category":"x","status":"ACTIVE"}`, seller, "VALIDATION_ERROR", "price", 400},
		{"NUL name", "POST", "/products", `{"name":"x\u0000","price":1,"stock":0,"category":"x","status":"ACTIVE"}`, seller, "VALIDATION_ERROR", "name", 400},
		{"NUL description", "POST", "/products", `{"name":"x","description":"\u0000","price":1,"stock":0,"category":"x","status":"ACTIVE"}`, seller, "VALIDATION_ERROR", "description", 400},
		{"NUL category", "GET", "/products?category=%00", "", user, "VALIDATION_ERROR", "category", 400},
		{"zero promo interval", "POST", "/promo-codes", `{"code":"TEST","discount_type":"PERCENTAGE","discount_value":10,"min_order_amount":0,"max_uses":1,"valid_from":"2026-01-01T00:00:00Z","valid_until":"2026-01-01T00:00:00Z","active":true}`, seller, "VALIDATION_ERROR", "valid_until", 400},
		{"missing name", "POST", "/products", `{"price":1,"stock":0,"category":"x","status":"ACTIVE"}`, seller, "VALIDATION_ERROR", "name", 400},
		{"wrong type", "POST", "/products", `{"name":123,"price":1,"stock":0,"category":"x","status":"ACTIVE"}`, seller, "VALIDATION_ERROR", "name", 400},
		{"unknown property", "POST", "/products", `{"name":"x","price":1,"stock":0,"category":"x","status":"ACTIVE","oops":true}`, seller, "VALIDATION_ERROR", "", 400},
		{"negative page", "GET", "/products?page=-1", "", user, "VALIDATION_ERROR", "page", 400},
		{"zero size", "GET", "/products?size=0", "", user, "VALIDATION_ERROR", "size", 400},
		{"bad id", "GET", "/products/invalid", "", user, "VALIDATION_ERROR", "id", 400},
		{"empty items", "POST", "/orders", `{"items":[]}`, user, "VALIDATION_ERROR", "items", 400},
		{"invalid quantity", "POST", "/orders", `{"items":[{"product_id":"00000000-0000-4000-8000-000000000001","quantity":0}]}`, user, "VALIDATION_ERROR", "quantity", 400},
		{"invalid promo", "POST", "/orders", `{"items":[{"product_id":"00000000-0000-4000-8000-000000000001","quantity":1}],"promo_code":"lower"}`, user, "VALIDATION_ERROR", "promo_code", 400},
		{"duplicate ids", "POST", "/orders", `{"items":[{"product_id":"00000000-0000-4000-8000-000000000001","quantity":1},{"product_id":"00000000-0000-4000-8000-000000000001","quantity":2}]}`, user, "VALIDATION_ERROR", "items", 400},
		{"admin registration", "POST", "/auth/register", `{"email":"x@example.com","password":"goodpassword","role":"ADMIN"}`, "", "VALIDATION_ERROR", "role", 400},
		{"bcrypt bytes", "POST", "/auth/register", `{"email":"x@example.com","password":"` + strings.Repeat("я", 40) + `"}`, "", "VALIDATION_ERROR", "password", 400},
		{"unknown route", "GET", "/missing", "", "", "NOT_FOUND", "", 404},
		{"unsupported method", "DELETE", "/health", "", "", "METHOD_NOT_ALLOWED", "", 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) || tc.field != "" && !strings.Contains(w.Body.String(), tc.field) {
				t.Fatalf("got %d %s; want%d %s field%s", w.Code, w.Body.String(), tc.status, tc.code, tc.field)
			}
			if w.Body.Len() > 4096 {
				t.Fatalf("validation error expanded input to %d bytes", w.Body.Len())
			}
			if w.Header().Get("X-Request-Id") == "" {
				t.Fatal("missing request id")
			}
		})
	}
	health := httptest.NewRecorder()
	h.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != 200 {
		t.Fatal(health.Code)
	}
}
