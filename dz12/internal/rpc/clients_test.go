package rpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"marketplace/internal/api"
	"marketplace/internal/fault"
	"marketplace/internal/identity"
)

func TestApplicationClientsRoundTrip(t *testing.T) {
	catalog := &catalogFixture{}
	cc := testConnection(t, func(s *grpc.Server) {
		RegisterUsersServer(s, usersFixture{})
		RegisterCatalogServer(s, catalog)
		RegisterOrdersServer(s, ordersFixture{})
	})
	users, products, orders := NewUsersClient(cc), NewCatalogClient(cc), NewOrdersClient(cc)
	ctx := context.Background()
	actor := identity.Principal{ID: testID, Role: "ADMIN"}
	user, e := users.Register(ctx, &api.RegisterRequest{Email: "a@example.com", Password: "password", Role: api.NewOptRegisterRequestRole(api.RegisterRequestRoleSELLER)})
	if e != nil || user.ID != testID || user.Role != api.RoleSELLER || !user.CreatedAt.Equal(testTime) {
		t.Fatalf("bad user: %v %v", user, e)
	}
	tokens, e := users.Login(ctx, &api.LoginRequest{Email: "a@example.com", Password: "secret"})
	if e != nil || tokens.AccessToken != "a@example.com" || tokens.TokenType != api.TokenPairTokenTypeBearer {
		t.Fatalf("bad tokens: %v %v", tokens, e)
	}
	tokens, e = users.Refresh(ctx, &api.RefreshRequest{RefreshToken: "rotation"})
	if e != nil || tokens.RefreshToken != "rotation" || tokens.ExpiresIn != 900 {
		t.Fatalf("bad refresh: %v %v", tokens, e)
	}
	role, e := users.LookupRole(ctx, testID)
	if e != nil || role != "SELLER" {
		t.Fatalf("bad role: %s %v", role, e)
	}
	_, e = users.Login(ctx, &api.LoginRequest{Email: "a@example.com", Password: "wrong"})
	var f *fault.Error
	if !errors.As(e, &f) || f.Status != 401 || f.Code != "INVALID_CREDENTIALS" {
		t.Fatalf("lost error: %v", e)
	}
	product, e := products.GetProduct(ctx, testID)
	if e != nil || product.Price.String() != "9999999999.99" || !product.Description.Null {
		t.Fatalf("bad product: %v %v", product, e)
	}
	req := &api.ProductCreate{Name: "created", Description: api.OptNilString{Set: true, Value: ""}, Price: product.Price, Stock: 3, Category: "books", Status: api.ProductStatusACTIVE}
	product, e = products.CreateProduct(ctx, actor, req)
	if e != nil || product.Name != "created" || product.Description.Null || product.Description.Value != "" || product.SellerID != testID {
		t.Fatalf("bad create: %v %v", product, e)
	}
	update := api.ProductUpdate(*req)
	update.Name = "updated"
	product, e = products.UpdateProduct(ctx, actor, testID, &update)
	if e != nil || product.Name != "updated" || product.ID != testID {
		t.Fatalf("bad update: %v %v", product, e)
	}
	page, e := products.ListProducts(ctx, api.ListProductsParams{Page: api.NewOptInt(2), Size: api.NewOptInt(30), Category: api.NewOptString("flowers")})
	if e != nil || page.Page != 2 || page.Size != 30 || page.TotalElements != 3000000000 || page.Items[0].Category != "flowers" {
		t.Fatalf("bad page: %v %v", page, e)
	}
	if e = products.DeleteProduct(ctx, actor, testID); e != nil {
		t.Fatal(e)
	}
	items := []api.OrderItemRequest{{ProductID: testID, Quantity: 2}}
	reserved, e := products.PrepareReservation(ctx, testID.String(), nil, items)
	if e != nil || len(reserved) != 1 || reserved[0].PriceAtOrder.String() != "9999999999.99" || reserved[0].Quantity != 2 {
		t.Fatalf("bad reservation: %v %v", reserved, e)
	}
	if e = products.ResolveReservation(ctx, testID.String(), false); e != nil || catalog.resolved != "abort:"+testID.String() {
		t.Fatalf("bad resolve: %v", e)
	}
	for _, promo := range []api.OptString{{}, api.NewOptString("ABC")} {
		order, e := orders.CreateOrder(ctx, actor, &api.OrderCreate{Items: items, PromoCode: promo})
		if e != nil || order.UserID != testID || order.PromoCodeID.Null == promo.Set || order.TotalAmount.String() != "9999999999.99" || order.DiscountAmount.String() != "0.01" || len(order.Items) != 1 || order.Items[0].Quantity != 2 || !order.CreatedAt.Equal(testTime) {
			t.Fatalf("bad order: %v %v", order, e)
		}
	}
	order, e := orders.GetOrder(ctx, actor, testID)
	if e != nil || order.ID != testID || !order.PromoCodeID.Null {
		t.Fatalf("bad get: %v %v", order, e)
	}
	order, e = orders.UpdateOrder(ctx, actor, testID, &api.OrderUpdate{Items: items})
	if e != nil || len(order.Items) != 1 || order.Items[0].ProductID != testID {
		t.Fatalf("bad update: %v %v", order, e)
	}
	order, e = orders.CancelOrder(ctx, actor, testID)
	if e != nil || order.Status != api.OrderStatusCANCELED {
		t.Fatalf("bad cancel: %v %v", order, e)
	}
	order, e = orders.ChangeOrderStatus(ctx, actor, testID, api.OrderStatusUpdateStatusPAID)
	if e != nil || order.Status != api.OrderStatusPAID {
		t.Fatalf("bad status: %v %v", order, e)
	}
	promo, e := orders.CreatePromoCode(ctx, actor, &api.PromoCodeCreate{Code: "ABC", DiscountType: api.DiscountTypePERCENTAGE, DiscountValue: product.Price, MinOrderAmount: product.Price, MaxUses: 10, ValidFrom: testTime, ValidUntil: testTime.Add(time.Hour), Active: api.NewOptBool(true)})
	if e != nil || promo.Code != "ABC" || promo.CurrentUses != 2 || promo.DiscountValue.String() != "9999999999.99" || promo.MaxUses != 10 || !promo.ValidUntil.Equal(testTime.Add(time.Hour)) {
		t.Fatalf("bad promo: %v %v", promo, e)
	}
}

type failingOrders struct {
	OrdersBackend
	calls int
}

func (f *failingOrders) CreateOrder(context.Context, identity.Principal, *api.OrderCreate) (*api.OrderResponse, error) {
	f.calls++
	return nil, &fault.Error{Status: 503, Code: "TRANSACTION_PENDING", Message: "Ожидание фиксации", Details: map[string]any{"transaction_id": testID.String(), "order_id": testID.String()}}
}
func TestClientPreservesPendingAndDoesNotRetryMutation(t *testing.T) {
	backend := &failingOrders{}
	cc := testConnection(t, func(s *grpc.Server) { RegisterOrdersServer(s, backend) })
	_, err := NewOrdersClient(cc).CreateOrder(context.Background(), identity.Principal{ID: testID, Role: "USER"}, &api.OrderCreate{Items: []api.OrderItemRequest{{ProductID: testID, Quantity: 1}}})
	var f *fault.Error
	if !errors.As(err, &f) || f.Code != "TRANSACTION_PENDING" || f.Status != 503 || f.Details["transaction_id"] != testID.String() || f.Details["order_id"] != testID.String() || backend.calls != 1 {
		t.Fatalf("pending failure changed or repeated: %v, calls%d", err, backend.calls)
	}
}
func TestClientDeadlineAndCapacityMapToUnavailable(t *testing.T) {
	cc := testConnection(t, func(s *grpc.Server) { RegisterUsersServer(s, usersFixture{}) })
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := NewUsersClient(cc).LookupRole(ctx, testID)
	var f *fault.Error
	if !errors.As(err, &f) || f.Status != 503 || f.Code != "SERVICE_UNAVAILABLE" {
		t.Fatalf("deadline not mapped: %v", err)
	}
	err = fromRPCError(status.Error(codes.ResourceExhausted, "capacity private details"))
	if !errors.As(err, &f) || f.Status != 503 || f.Code != "SERVICE_UNAVAILABLE" {
		t.Fatalf("capacity not mapped: %v", err)
	}
}
