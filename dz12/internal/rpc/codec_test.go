package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"marketplace/internal/api"
	"marketplace/internal/fault"
	"marketplace/internal/identity"
	"marketplace/internal/rpc/pb"
)

func TestProductWirePreservesMoneyNullAndTime(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 34, 56, 123456789, time.FixedZone("MSK", 10800))
	for _, desc := range []api.NilString{{Null: true}, {Value: ""}, {Value: "Описание"}} {
		src := &api.ProductResponse{ID: uuid.MustParse("00000000-0000-4000-8000-000000000001"), SellerID: uuid.MustParse("00000000-0000-4000-8000-000000000002"), Name: "Product", Description: desc, Price: decimal.RequireFromString("9999999999.99"), Stock: 5, Category: "test", Status: api.ProductStatusACTIVE, CreatedAt: now, UpdatedAt: now.Add(time.Second)}
		wire := productToProto(src)
		if wire.Price != "9999999999.99" {
			t.Fatalf("price rounded: %q", wire.Price)
		}
		dst, err := productFromProto(wire)
		if err != nil {
			t.Fatal(err)
		}
		if dst.ID != src.ID || dst.SellerID != src.SellerID || dst.Name != "Product" || dst.Description != desc || !dst.Price.Equal(src.Price) || dst.Stock != 5 || dst.Category != "test" || dst.Status != api.ProductStatusACTIVE || !dst.CreatedAt.Equal(now) || !dst.UpdatedAt.Equal(now.Add(time.Second)) {
			t.Fatalf("lost product fields: %+v", dst)
		}
	}
}
func TestProductWritePreservesDescriptionPresence(t *testing.T) {
	for _, desc := range []api.OptNilString{{}, {Set: true, Null: true}, {Set: true, Value: ""}, {Set: true, Value: "test"}} {
		src := &api.ProductCreate{Name: "x", Description: desc, Price: decimal.RequireFromString("0.01"), Category: "c", Status: api.ProductStatusACTIVE, SellerID: api.NewOptUUID(uuid.MustParse("00000000-0000-4000-8000-000000000002"))}
		got, err := productCreateFromProto(productCreateToProto(src))
		if err != nil {
			t.Fatal(err)
		}
		if got.Description != desc || got.SellerID != src.SellerID || !got.Price.Equal(src.Price) {
			t.Fatalf("lost presence: %+v", got)
		}
	}
}
func TestProductRejectsMalformedWireFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*pb.Product)
	}{
		{"uuid", func(p *pb.Product) { p.Id = "bad" }},
		{"decimal", func(p *pb.Product) { p.Price = "1e1000000" }},
		{"timestamp", func(p *pb.Product) { p.CreatedAt = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := productToProto(&api.ProductResponse{ID: uuid.New(), SellerID: uuid.New(), Price: decimal.NewFromInt(1), CreatedAt: time.Now(), UpdatedAt: time.Now()})
			tc.mutate(p)
			if _, err := productFromProto(p); err == nil {
				t.Fatal("malformed upstream response accepted")
			}
		})
	}
}
func TestFaultWirePreservesContractAndDetails(t *testing.T) {
	for _, src := range []*fault.Error{
		fault.Validation("items[0].quantity", "Недостаточный остаток"),
		{Status: 409, Code: "INSUFFICIENT_STOCK", Message: "Недостаточно товара", Details: map[string]any{"products": []map[string]any{{"product_id": "00000000-0000-4000-8000-000000000001", "available": int32(2), "requested": int32(3)}}}},
		{Status: 503, Code: "TRANSACTION_PENDING", Message: "Ожидание фиксации", Details: map[string]any{"transaction_id": "t", "order_id": "o"}},
	} {
		wire := toRPCError(src)
		if len(status.Convert(wire).Details()) != 1 {
			t.Fatalf("missing typed error details: %v", wire)
		}
		var got *fault.Error
		if !errors.As(fromRPCError(wire), &got) {
			t.Fatalf("lost fault: %v", wire)
		}
		wantJSON, _ := json.Marshal(src.Details)
		gotJSON, _ := json.Marshal(got.Details)
		if got.Status != src.Status || got.Code != src.Code || got.Message != src.Message || string(wantJSON) != string(gotJSON) {
			t.Fatalf("changed fault: %+v", got)
		}
	}
}
func TestTransportFailuresHideInternalMessages(t *testing.T) {
	for _, src := range []error{errors.New("password=secret SQL connection"), status.Error(codes.Unavailable, "certificate secret"), status.Error(codes.DeadlineExceeded, "private endpoint")} {
		transported := src
		if status.Code(src) == codes.Unknown {
			transported = toRPCError(src)
		}
		got := fromRPCError(transported)
		var f *fault.Error
		if !errors.As(got, &f) || (f.Status != 500 && f.Status != 503) || strings.Contains(got.Error(), "secret") || strings.Contains(got.Error(), "private") {
			t.Fatalf("unsafe failure: %v", got)
		}
		if status.Code(src) == codes.Unavailable || status.Code(src) == codes.DeadlineExceeded {
			if f.Status != 503 || f.Code != "SERVICE_UNAVAILABLE" {
				t.Fatalf("transport should map to503: %+v", f)
			}
		}
	}
}
func TestPrincipalMetadataValidated(t *testing.T) {
	id := "00000000-0000-4000-8000-000000000abc"
	for _, md := range []metadata.MD{nil, metadata.Pairs("x-user-id", id), metadata.Pairs("x-user-id", "bad", "x-user-role", "USER"), metadata.Pairs("x-user-id", strings.ToUpper(id), "x-user-role", "USER"), metadata.Pairs("x-user-id", uuid.Nil.String(), "x-user-role", "USER"), metadata.Pairs("x-user-id", id, "x-user-role", "ROOT"), metadata.Pairs("x-user-id", id, "x-user-role", "USER", "x-user-role", "ADMIN")} {
		_, err := principalFromContext(metadata.NewIncomingContext(context.Background(), md))
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("accepted invalid identity %v: %v", md, err)
		}
	}
	for _, role := range []string{"USER", "SELLER", "ADMIN"} {
		want := identity.Principal{ID: uuid.MustParse(id), Role: role}
		ctx := withPrincipal(metadata.NewOutgoingContext(context.Background(), metadata.Pairs("x-user-id", "spoof", "x-request-id", "keep")), want)
		md, _ := metadata.FromOutgoingContext(ctx)
		got, err := principalFromContext(metadata.NewIncomingContext(context.Background(), md))
		if err != nil || got != want || !reflect.DeepEqual(md.Get("x-request-id"), []string{"keep"}) {
			t.Fatalf("principal propagation failed: %+v %v", got, err)
		}
	}
}

func TestMalformedBusinessDetailsAreServiceUnavailable(t *testing.T) {
	s, err := status.New(codes.Unavailable, "private backend details").WithDetails(&pb.BusinessError{ErrorCode: "TRANSACTION_PENDING", HttpStatus: 503, Message: "Ожидание", DetailsJson: []byte("invalid-json")})
	if err != nil {
		t.Fatal(err)
	}
	var f *fault.Error
	if err = fromRPCError(s.Err()); !errors.As(err, &f) || f.Status != 503 || f.Code != "SERVICE_UNAVAILABLE" {
		t.Fatalf("invalid details broke HTTP contract: %v", err)
	}
}
