package rpc

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"marketplace/internal/auth"
	"marketplace/internal/httpapi"
	"marketplace/internal/identity"
)

func TestGatewayPromoActiveDefaultAcrossGRPC(t *testing.T) {
	cc := testConnection(t, func(s *grpc.Server) {
		RegisterOrdersServer(s, ordersFixture{})
	})
	tokens, err := auth.NewTokens("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	bearer, _, err := tokens.Issue(identity.Principal{ID: testID, Role: "SELLER"}, "access", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	h, err := httpapi.New(nil, NewOrdersClient(cc), nil, tokens, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, active string
		status       int
		want         bool
	}{
		{"omitted enables promo", "", 201, true},
		{"explicit true", `,"active":true`, 201, true},
		{"explicit false", `,"active":false`, 201, false},
		{"null is invalid", `,"active":null`, 400, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"code":"DEFAULT_TEST","discount_type":"PERCENTAGE","discount_value":10,"min_order_amount":0,"max_uses":5,"valid_from":"2026-01-01T00:00:00Z","valid_until":"2027-01-01T00:00:00Z"` + tc.active + `}`
			r := httptest.NewRequest("POST", "/promo-codes", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+bearer)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s; want %d", w.Code, w.Body.String(), tc.status)
			}
			var response struct {
				Active    *bool  `json:"active"`
				ErrorCode string `json:"error_code"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if tc.status == 201 && (response.Active == nil || *response.Active != tc.want) {
				t.Fatalf("active in %s; want %t", w.Body.String(), tc.want)
			}
			if tc.status == 400 && response.ErrorCode != "VALIDATION_ERROR" {
				t.Fatalf("unexpected validation response: %s", w.Body.String())
			}
		})
	}
}

func TestGatewayRetainsHTTPContractAcrossGRPC(t *testing.T) {
	cc := testConnection(t, func(s *grpc.Server) {
		RegisterUsersServer(s, usersFixture{})
		RegisterCatalogServer(s, &catalogFixture{})
		RegisterOrdersServer(s, &failingOrders{})
	})
	tokens, err := auth.NewTokens("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	bearer, _, err := tokens.Issue(identity.Principal{ID: testID, Role: "USER"}, "access", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	h, err := httpapi.New(NewCatalogClient(cc), NewOrdersClient(cc), NewUsersClient(cc), tokens, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, body string
		status             int
		fragments          []string
	}{
		{"GET", "/products/" + testID.String(), "", 200, []string{`"price":9999999999.99`, `"description":null`, `"id":"` + testID.String() + `"`}},
		{"POST", "/orders", `{"items":[{"product_id":"` + testID.String() + `","quantity":1}]}`, 503, []string{`"error_code":"TRANSACTION_PENDING"`, `"transaction_id":"` + testID.String() + `"`, `"order_id":"` + testID.String() + `"`}},
		{"POST", "/auth/login", `{"email":"a@example.com","password":"wrong-password"}`, 401, []string{`"error_code":"INVALID_CREDENTIALS"`}},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+bearer)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("got%d: %s", w.Code, w.Body.String())
			}
			for _, text := range tc.fragments {
				if !strings.Contains(w.Body.String(), text) {
					t.Fatalf("missing %s in %s", text, w.Body.String())
				}
			}
		})
	}
}
