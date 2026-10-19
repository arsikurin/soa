package rpc

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc"

	"marketplace/internal/api"
	"marketplace/internal/fault"
	"marketplace/internal/identity"
	"marketplace/internal/rpc/pb"
)

type UsersClient struct{ client pb.UsersClient }
type CatalogClient struct{ client pb.CatalogClient }
type OrdersClient struct{ client pb.OrdersClient }

var _ UsersBackend = (*UsersClient)(nil)
var _ CatalogBackend = (*CatalogClient)(nil)
var _ OrdersBackend = (*OrdersClient)(nil)

func NewUsersClient(cc grpc.ClientConnInterface) *UsersClient {
	return &UsersClient{client: pb.NewUsersClient(cc)}
}
func NewCatalogClient(cc grpc.ClientConnInterface) *CatalogClient {
	return &CatalogClient{client: pb.NewCatalogClient(cc)}
}
func NewOrdersClient(cc grpc.ClientConnInterface) *OrdersClient {
	return &OrdersClient{client: pb.NewOrdersClient(cc)}
}

func decodeResponse[Wire, Result any](v *Wire, err error, decode func(*Wire) (*Result, error)) (*Result, error) {
	if err != nil {
		return nil, fromRPCError(err)
	}
	result, err := decode(v)
	if err != nil {
		return nil, invalidResponse()
	}
	return result, nil
}
func invalidResponse() error {
	return fault.New(503, "SERVICE_UNAVAILABLE", "Некорректный ответ сервиса")
}
func (c *UsersClient) Register(ctx context.Context, r *api.RegisterRequest) (*api.UserResponse, error) {
	req := &pb.RegisterRequest{Email: r.Email, Password: r.Password}
	if r.Role.Set {
		req.Role = ptr(string(r.Role.Value))
	}
	v, e := c.client.Register(ctx, req)
	return decodeResponse(v, e, userFromProto)
}
func (c *UsersClient) Login(ctx context.Context, r *api.LoginRequest) (*api.TokenPair, error) {
	v, e := c.client.Login(ctx, &pb.LoginRequest{Email: r.Email, Password: r.Password})
	return decodeResponse(v, e, tokensFromProto)
}
func (c *UsersClient) Refresh(ctx context.Context, r *api.RefreshRequest) (*api.TokenPair, error) {
	v, e := c.client.Refresh(ctx, &pb.RefreshRequest{RefreshToken: r.RefreshToken})
	return decodeResponse(v, e, tokensFromProto)
}
func (c *UsersClient) LookupRole(ctx context.Context, id uuid.UUID) (string, error) {
	v, e := c.client.LookupRole(ctx, &pb.IDRequest{Id: id.String()})
	if e != nil {
		return "", fromRPCError(e)
	}
	if v == nil || api.Role(v.Role).Validate() != nil {
		return "", invalidResponse()
	}
	return v.Role, nil
}
func (c *CatalogClient) CreateProduct(ctx context.Context, p identity.Principal, r *api.ProductCreate) (*api.ProductResponse, error) {
	v, e := c.client.CreateProduct(withPrincipal(ctx, p), productCreateToProto(r))
	return decodeResponse(v, e, productFromProto)
}
func (c *CatalogClient) GetProduct(ctx context.Context, id uuid.UUID) (*api.ProductResponse, error) {
	v, e := c.client.GetProduct(ctx, &pb.IDRequest{Id: id.String()})
	return decodeResponse(v, e, productFromProto)
}
func (c *CatalogClient) ListProducts(ctx context.Context, r api.ListProductsParams) (*api.ProductPage, error) {
	v, e := c.client.ListProducts(ctx, listToProto(r))
	return decodeResponse(v, e, pageFromProto)
}
func (c *CatalogClient) UpdateProduct(ctx context.Context, p identity.Principal, id uuid.UUID, r *api.ProductUpdate) (*api.ProductResponse, error) {
	v, e := c.client.UpdateProduct(withPrincipal(ctx, p), &pb.ProductUpdateRequest{Id: id.String(), Product: productUpdateToProto(r)})
	return decodeResponse(v, e, productFromProto)
}
func (c *CatalogClient) DeleteProduct(ctx context.Context, p identity.Principal, id uuid.UUID) error {
	_, e := c.client.DeleteProduct(withPrincipal(ctx, p), &pb.IDRequest{Id: id.String()})
	return fromRPCError(e)
}
func (c *CatalogClient) PrepareReservation(ctx context.Context, gid string, old []api.OrderItemResponse, items []api.OrderItemRequest) ([]api.OrderItemResponse, error) {
	v, e := c.client.PrepareReservation(ctx, &pb.PrepareReservationRequest{TransactionId: gid, OldItems: itemsToProto(old), NewItems: requestItemsToProto(items)})
	if e != nil {
		return nil, fromRPCError(e)
	}
	if v == nil {
		return nil, invalidResponse()
	}
	d := decoder{}
	result := d.items(v.Items)
	if d.err != nil {
		return nil, invalidResponse()
	}
	return result, nil
}
func (c *CatalogClient) ResolveReservation(ctx context.Context, gid string, commit bool) error {
	_, e := c.client.ResolveReservation(ctx, &pb.ResolveReservationRequest{TransactionId: gid, Commit: commit})
	return fromRPCError(e)
}
func (c *OrdersClient) CreateOrder(ctx context.Context, p identity.Principal, r *api.OrderCreate) (*api.OrderResponse, error) {
	req := &pb.OrderCreateRequest{Items: requestItemsToProto(r.Items)}
	if r.PromoCode.Set {
		req.PromoCode = ptr(r.PromoCode.Value)
	}
	orderID := uuid.New()
	ctx, transactionID := newOperation(withPrincipal(ctx, p), orderID)
	v, e := c.client.CreateOrder(ctx, req)
	return decodeOrderMutation(v, e, transactionID, orderID)
}
func (c *OrdersClient) GetOrder(ctx context.Context, p identity.Principal, id uuid.UUID) (*api.OrderResponse, error) {
	v, e := c.client.GetOrder(withPrincipal(ctx, p), &pb.IDRequest{Id: id.String()})
	return decodeResponse(v, e, orderFromProto)
}
func (c *OrdersClient) UpdateOrder(ctx context.Context, p identity.Principal, id uuid.UUID, r *api.OrderUpdate) (*api.OrderResponse, error) {
	ctx, transactionID := newOperation(withPrincipal(ctx, p), id)
	v, e := c.client.UpdateOrder(ctx, &pb.OrderUpdateRequest{Id: id.String(), Items: requestItemsToProto(r.Items)})
	return decodeOrderMutation(v, e, transactionID, id)
}
func (c *OrdersClient) CancelOrder(ctx context.Context, p identity.Principal, id uuid.UUID) (*api.OrderResponse, error) {
	ctx, transactionID := newOperation(withPrincipal(ctx, p), id)
	v, e := c.client.CancelOrder(ctx, &pb.IDRequest{Id: id.String()})
	return decodeOrderMutation(v, e, transactionID, id)
}
func (c *OrdersClient) ChangeOrderStatus(ctx context.Context, p identity.Principal, id uuid.UUID, state api.OrderStatusUpdateStatus) (*api.OrderResponse, error) {
	v, e := c.client.ChangeOrderStatus(withPrincipal(ctx, p), &pb.OrderStatusRequest{Id: id.String(), Status: string(state)})
	return decodeResponse(v, e, orderFromProto)
}
func (c *OrdersClient) CreatePromoCode(ctx context.Context, p identity.Principal, r *api.PromoCodeCreate) (*api.PromoCodeResponse, error) {
	v, e := c.client.CreatePromoCode(withPrincipal(ctx, p), promoCreateToProto(r))
	return decodeResponse(v, e, promoFromProto)
}
