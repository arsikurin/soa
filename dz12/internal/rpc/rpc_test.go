package rpc

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
	"marketplace/internal/api"
	"marketplace/internal/fault"
	"marketplace/internal/identity"
	"marketplace/internal/platform"
	"marketplace/internal/rpc/pb"
	"marketplace/internal/twopc"
)

var testID = uuid.MustParse("00000000-0000-4000-8000-000000000001")
var testTime = time.Date(2026, 10, 10, 12, 34, 56, 123456000, time.UTC)

func testConnection(t *testing.T, register func(*grpc.Server), options ...grpc.ServerOption) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	options = append([]grpc.ServerOption{grpc.MaxRecvMsgSize(platform.MaxRequestBytes), grpc.MaxSendMsgSize(platform.MaxResponseBytes)}, options...)
	s := grpc.NewServer(options...)
	register(s)
	go func() { _ = s.Serve(lis) }()
	cc, err := grpc.NewClient("passthrough:///bufconn", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(platform.MaxResponseBytes), grpc.MaxCallSendMsgSize(platform.MaxRequestBytes)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close(); s.Stop(); lis.Close() })
	return cc
}

type usersFixture struct{ UsersBackend }

func (usersFixture) Register(_ context.Context, r *api.RegisterRequest) (*api.UserResponse, error) {
	return &api.UserResponse{ID: testID, Email: r.Email, Role: api.Role(r.Role.Or(api.RegisterRequestRoleUSER)), CreatedAt: testTime, UpdatedAt: testTime}, nil
}
func (usersFixture) Login(_ context.Context, r *api.LoginRequest) (*api.TokenPair, error) {
	if r.Password != "secret" {
		return nil, fault.New(401, "INVALID_CREDENTIALS", "Неверный пароль")
	}
	return &api.TokenPair{AccessToken: r.Email, RefreshToken: "refresh", TokenType: api.TokenPairTokenTypeBearer, ExpiresIn: 900}, nil
}
func (usersFixture) Refresh(_ context.Context, r *api.RefreshRequest) (*api.TokenPair, error) {
	return &api.TokenPair{AccessToken: "access", RefreshToken: r.RefreshToken, TokenType: api.TokenPairTokenTypeBearer, ExpiresIn: 900}, nil
}
func (usersFixture) LookupRole(_ context.Context, id uuid.UUID) (string, error) {
	if id != testID {
		return "", errors.New("SQL password=secret")
	}
	return "SELLER", nil
}
func TestUsersRPCDispatchAndFaults(t *testing.T) {
	cc := testConnection(t, func(s *grpc.Server) { RegisterUsersServer(s, usersFixture{}) })
	client := pb.NewUsersClient(cc)
	ctx := context.Background()
	u, err := client.Register(ctx, &pb.RegisterRequest{Email: "a@example.com", Password: "longpassword", Role: ptr("SELLER")})
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "a@example.com" || u.Role != "SELLER" || u.Id != testID.String() || !u.CreatedAt.AsTime().Equal(testTime) {
		t.Fatalf("bad user: %v", u)
	}
	tokens, err := client.Login(ctx, &pb.LoginRequest{Email: "a@example.com", Password: "secret"})
	if err != nil || tokens.AccessToken != "a@example.com" || tokens.ExpiresIn != 900 {
		t.Fatalf("bad tokens: %v %v", tokens, err)
	}
	tokens, err = client.Refresh(ctx, &pb.RefreshRequest{RefreshToken: "new-refresh"})
	if err != nil || tokens.RefreshToken != "new-refresh" {
		t.Fatalf("bad refresh: %v %v", tokens, err)
	}
	role, err := client.LookupRole(ctx, &pb.IDRequest{Id: testID.String()})
	if err != nil || role.Role != "SELLER" {
		t.Fatalf("bad lookup: %v %v", role, err)
	}
	_, err = client.Login(ctx, &pb.LoginRequest{Email: "a@example.com", Password: "wrong"})
	var f *fault.Error
	if !errors.As(fromRPCError(err), &f) || f.Code != "INVALID_CREDENTIALS" || f.Status != 401 {
		t.Fatalf("bad business error: %v", err)
	}
	_, err = client.LookupRole(ctx, &pb.IDRequest{Id: "bad"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad UUID accepted: %v", err)
	}
	_, err = client.LookupRole(ctx, &pb.IDRequest{Id: uuid.NewString()})
	if status.Code(err) != codes.Internal || status.Convert(err).Message() == "SQL password=secret" {
		t.Fatalf("internal failure leaked: %v", err)
	}
}

type catalogFixture struct {
	CatalogBackend
	prepared []api.OrderItemResponse
	resolved string
}

func (c *catalogFixture) CreateProduct(_ context.Context, p identity.Principal, r *api.ProductCreate) (*api.ProductResponse, error) {
	description := api.NilString{Null: true}
	if r.Description.Set && !r.Description.Null {
		description.SetTo(r.Description.Value)
	}
	return &api.ProductResponse{ID: testID, Name: r.Name, Description: description, Price: r.Price, Stock: r.Stock, Category: r.Category, Status: r.Status, SellerID: p.ID, CreatedAt: testTime, UpdatedAt: testTime}, nil
}
func (c *catalogFixture) GetProduct(ctx context.Context, id uuid.UUID) (*api.ProductResponse, error) {
	return c.CreateProduct(ctx, identity.Principal{ID: id}, &api.ProductCreate{Name: "get", Price: decimal.RequireFromString("9999999999.99"), Category: "c", Status: api.ProductStatusACTIVE})
}
func (c *catalogFixture) ListProducts(ctx context.Context, p api.ListProductsParams) (*api.ProductPage, error) {
	v, _ := c.GetProduct(ctx, testID)
	v.Category = p.Category.Or("default")
	return &api.ProductPage{Items: []api.ProductResponse{*v}, Page: p.Page.Or(0), Size: p.Size.Or(20), TotalElements: 3000000000}, nil
}
func (c *catalogFixture) UpdateProduct(ctx context.Context, p identity.Principal, id uuid.UUID, r *api.ProductUpdate) (*api.ProductResponse, error) {
	v, e := c.CreateProduct(ctx, p, (*api.ProductCreate)(r))
	v.ID = id
	return v, e
}
func (c *catalogFixture) DeleteProduct(_ context.Context, p identity.Principal, id uuid.UUID) error {
	if p.ID != id {
		return fault.Denied()
	}
	return nil
}
func (c *catalogFixture) PrepareReservation(_ context.Context, gid string, old []api.OrderItemResponse, items []api.OrderItemRequest) ([]api.OrderItemResponse, error) {
	if gid != testID.String() {
		return nil, fault.New(409, "TRANSACTION_CONFLICT", "Конфликт")
	}
	c.prepared = append([]api.OrderItemResponse{}, old...)
	for _, item := range items {
		c.prepared = append(c.prepared, api.OrderItemResponse{ID: testID, ProductID: item.ProductID, Quantity: item.Quantity, PriceAtOrder: decimal.RequireFromString("9999999999.99")})
	}
	return c.prepared, nil
}
func (c *catalogFixture) ResolveReservation(_ context.Context, gid string, commit bool) error {
	if commit {
		c.resolved = gid
	} else {
		c.resolved = "abort:" + gid
	}
	return nil
}
func TestCatalogRPCDispatchAndIdentity(t *testing.T) {
	backend := &catalogFixture{}
	cc := testConnection(t, func(s *grpc.Server) { RegisterCatalogServer(s, backend) })
	c := pb.NewCatalogClient(cc)
	ctx := withPrincipal(context.Background(), identity.Principal{ID: testID, Role: "SELLER"})
	write := &pb.ProductWrite{Name: "x", Description: &pb.NullableString{Value: ptr("")}, Price: "9999999999.99", Stock: 2, Category: "c", Status: "ACTIVE"}
	_, err := c.CreateProduct(context.Background(), write)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing principal accepted: %v", err)
	}
	v, err := c.CreateProduct(ctx, write)
	if err != nil || v.Price != "9999999999.99" || v.SellerId != testID.String() || v.Description == nil || *v.Description != "" {
		t.Fatalf("bad create: %v %v", v, err)
	}
	v, err = c.GetProduct(ctx, &pb.IDRequest{Id: testID.String()})
	if err != nil || v.Name != "get" {
		t.Fatalf("bad get: %v %v", v, err)
	}
	page, err := c.ListProducts(ctx, &pb.ListProductsRequest{Page: ptr(int64(2)), Size: ptr(int64(30)), Category: ptr("books")})
	if err != nil || page.Page != 2 || page.Size != 30 || page.TotalElements != 3000000000 || page.Items[0].Category != "books" {
		t.Fatalf("bad list: %v %v", page, err)
	}
	updateID := uuid.NewString()
	v, err = c.UpdateProduct(ctx, &pb.ProductUpdateRequest{Id: updateID, Product: write})
	if err != nil || v.Id != updateID {
		t.Fatalf("bad update: %v %v", v, err)
	}
	if _, err = c.DeleteProduct(ctx, &pb.IDRequest{Id: testID.String()}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.DeleteProduct(ctx, &pb.IDRequest{Id: updateID}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("delete error lost: %v", err)
	}
	reserved, err := c.PrepareReservation(context.Background(), &pb.PrepareReservationRequest{TransactionId: testID.String(), OldItems: []*pb.OrderItem{{Id: testID.String(), ProductId: testID.String(), Quantity: 1, PriceAtOrder: "0.01"}}, NewItems: []*pb.OrderItemRequest{{ProductId: testID.String(), Quantity: 2}}})
	if err != nil || len(reserved.Items) != 2 || reserved.Items[0].PriceAtOrder != "0.01" || reserved.Items[1].PriceAtOrder != "9999999999.99" || reserved.Items[1].Quantity != 2 {
		t.Fatalf("reservation mapping lost: %v %v", reserved, err)
	}
	if _, err = c.ResolveReservation(context.Background(), &pb.ResolveReservationRequest{TransactionId: testID.String(), Commit: true}); err != nil || backend.resolved != testID.String() {
		t.Fatalf("resolve failed: %v", err)
	}
}

type ordersFixture struct{ OrdersBackend }

func (ordersFixture) CreateOrder(ctx context.Context, p identity.Principal, r *api.OrderCreate) (*api.OrderResponse, error) {
	items := make([]api.OrderItemResponse, len(r.Items))
	for i, item := range r.Items {
		items[i] = api.OrderItemResponse{ID: testID, ProductID: item.ProductID, Quantity: item.Quantity, PriceAtOrder: decimal.RequireFromString("0.01")}
	}
	promo := api.NilUUID{Null: true}
	if r.PromoCode.Set {
		promo.SetTo(testID)
	}
	id := testID
	if _, operationID, ok := twopc.OperationFromContext(ctx); ok {
		id = operationID
	}
	return &api.OrderResponse{ID: id, UserID: p.ID, Status: api.OrderStatusCREATED, TotalAmount: decimal.RequireFromString("9999999999.99"), DiscountAmount: decimal.RequireFromString("0.01"), PromoCodeID: promo, Items: items, CreatedAt: testTime, UpdatedAt: testTime}, nil
}
func (o ordersFixture) GetOrder(ctx context.Context, p identity.Principal, id uuid.UUID) (*api.OrderResponse, error) {
	v, e := o.CreateOrder(ctx, p, &api.OrderCreate{})
	v.ID = id
	return v, e
}
func (o ordersFixture) UpdateOrder(ctx context.Context, p identity.Principal, id uuid.UUID, r *api.OrderUpdate) (*api.OrderResponse, error) {
	v, e := o.CreateOrder(ctx, p, &api.OrderCreate{Items: r.Items})
	v.ID = id
	return v, e
}
func (o ordersFixture) CancelOrder(ctx context.Context, p identity.Principal, id uuid.UUID) (*api.OrderResponse, error) {
	v, e := o.GetOrder(ctx, p, id)
	v.Status = api.OrderStatusCANCELED
	return v, e
}
func (o ordersFixture) ChangeOrderStatus(ctx context.Context, p identity.Principal, id uuid.UUID, s api.OrderStatusUpdateStatus) (*api.OrderResponse, error) {
	v, e := o.GetOrder(ctx, p, id)
	v.Status = api.OrderStatus(s)
	return v, e
}
func (ordersFixture) CreatePromoCode(_ context.Context, _ identity.Principal, r *api.PromoCodeCreate) (*api.PromoCodeResponse, error) {
	return &api.PromoCodeResponse{ID: testID, Code: r.Code, DiscountType: r.DiscountType, DiscountValue: r.DiscountValue, MinOrderAmount: r.MinOrderAmount, MaxUses: r.MaxUses, ValidFrom: r.ValidFrom, ValidUntil: r.ValidUntil, Active: r.Active.Or(true), CurrentUses: 2, CreatedAt: testTime, UpdatedAt: testTime}, nil
}
func TestOrdersRPCDispatchAndNullablePromo(t *testing.T) {
	cc := testConnection(t, func(s *grpc.Server) { RegisterOrdersServer(s, ordersFixture{}) })
	c := pb.NewOrdersClient(cc)
	ctx := withPrincipal(context.Background(), identity.Principal{ID: testID, Role: "USER"})
	ctx = metadata.AppendToOutgoingContext(ctx, "x-transaction-id", uuid.NewString(), "x-order-id", testID.String())
	items := []*pb.OrderItemRequest{{ProductId: testID.String(), Quantity: 3}}
	for _, promo := range []*string{nil, ptr("PROMO")} {
		v, err := c.CreateOrder(ctx, &pb.OrderCreateRequest{Items: items, PromoCode: promo})
		if err != nil || v.UserId != testID.String() || v.TotalAmount != "9999999999.99" || v.DiscountAmount != "0.01" || len(v.Items) != 1 || v.Items[0].Quantity != 3 || (v.PromoCodeId == nil) != (promo == nil) {
			t.Fatalf("bad order: %v %v", v, err)
		}
	}
	v, err := c.GetOrder(ctx, &pb.IDRequest{Id: testID.String()})
	if err != nil || v.Id != testID.String() {
		t.Fatalf("bad get: %v %v", v, err)
	}
	v, err = c.UpdateOrder(ctx, &pb.OrderUpdateRequest{Id: testID.String(), Items: items})
	if err != nil || len(v.Items) != 1 || v.Items[0].PriceAtOrder != "0.01" {
		t.Fatalf("bad update: %v %v", v, err)
	}
	v, err = c.CancelOrder(ctx, &pb.IDRequest{Id: testID.String()})
	if err != nil || v.Status != "CANCELED" {
		t.Fatalf("bad cancel: %v %v", v, err)
	}
	v, err = c.ChangeOrderStatus(ctx, &pb.OrderStatusRequest{Id: testID.String(), Status: "PAYMENT_PENDING"})
	if err != nil || v.Status != "PAYMENT_PENDING" {
		t.Fatalf("bad status: %v %v", v, err)
	}
	promo, err := c.CreatePromoCode(ctx, &pb.PromoCodeCreateRequest{Code: "ABC", DiscountType: "PERCENTAGE", DiscountValue: "33.33", MinOrderAmount: "1.01", MaxUses: 5, ValidFrom: timestamppb.New(testTime), ValidUntil: timestamppb.New(testTime.Add(time.Hour)), Active: true})
	if err != nil || promo.DiscountValue != "33.33" || promo.MinOrderAmount != "1.01" || promo.CurrentUses != 2 || !promo.ValidUntil.AsTime().Equal(testTime.Add(time.Hour)) {
		t.Fatalf("bad promo: %v %v", promo, err)
	}
	_, err = c.GetOrder(metadata.NewOutgoingContext(context.Background(), metadata.Pairs("x-user-id", testID.String(), "x-user-role", "ROOT")), &pb.IDRequest{Id: testID.String()})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("invalid principal accepted: %v", err)
	}
}
