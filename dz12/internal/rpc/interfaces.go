package rpc

import (
	"context"
	"github.com/google/uuid"
	"marketplace/internal/api"
	"marketplace/internal/identity"
)

type UsersBackend interface {
	Register(context.Context, *api.RegisterRequest) (*api.UserResponse, error)
	Login(context.Context, *api.LoginRequest) (*api.TokenPair, error)
	Refresh(context.Context, *api.RefreshRequest) (*api.TokenPair, error)
	LookupRole(context.Context, uuid.UUID) (string, error)
}
type CatalogBackend interface {
	CreateProduct(context.Context, identity.Principal, *api.ProductCreate) (*api.ProductResponse, error)
	GetProduct(context.Context, uuid.UUID) (*api.ProductResponse, error)
	ListProducts(context.Context, api.ListProductsParams) (*api.ProductPage, error)
	UpdateProduct(context.Context, identity.Principal, uuid.UUID, *api.ProductUpdate) (*api.ProductResponse, error)
	DeleteProduct(context.Context, identity.Principal, uuid.UUID) error
	PrepareReservation(context.Context, string, []api.OrderItemResponse, []api.OrderItemRequest) ([]api.OrderItemResponse, error)
	ResolveReservation(context.Context, string, bool) error
}
type OrdersBackend interface {
	CreateOrder(context.Context, identity.Principal, *api.OrderCreate) (*api.OrderResponse, error)
	GetOrder(context.Context, identity.Principal, uuid.UUID) (*api.OrderResponse, error)
	UpdateOrder(context.Context, identity.Principal, uuid.UUID, *api.OrderUpdate) (*api.OrderResponse, error)
	CancelOrder(context.Context, identity.Principal, uuid.UUID) (*api.OrderResponse, error)
	ChangeOrderStatus(context.Context, identity.Principal, uuid.UUID, api.OrderStatusUpdateStatus) (*api.OrderResponse, error)
	CreatePromoCode(context.Context, identity.Principal, *api.PromoCodeCreate) (*api.PromoCodeResponse, error)
}
