package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/ogen-go/ogen/ogenerrors"
	"marketplace/internal/api"
	"marketplace/internal/auth"
	"marketplace/internal/fault"
	"marketplace/internal/identity"
)

type server struct {
	catalog Catalog
	orders  Orders
	auth    Authentication
	tokens  *auth.Tokens
	logger  *slog.Logger
}

var _ api.Handler = (*server)(nil)
var _ api.SecurityHandler = (*server)(nil)

func New(catalog Catalog, orders Orders, authentication Authentication, tokens *auth.Tokens, logger *slog.Logger, allowedOrigins ...string) (http.Handler, error) {
	h := &server{catalog: catalog, orders: orders, auth: authentication, tokens: tokens, logger: logger}
	generated, err := api.NewServer(h, h, api.WithErrorHandler(h.protocolError),
		api.WithNotFound(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, fault.New(404, "NOT_FOUND", "Маршрут не найден"))
		}),
		api.WithMethodNotAllowed(func(w http.ResponseWriter, r *http.Request, allowed string) {
			w.Header().Set("Allow", allowed)
			writeError(w, fault.New(405, "METHOD_NOT_ALLOWED", "Метод не поддерживается"))
		}))
	if err != nil {
		return nil, err
	}
	return corsHandler(generated, logger, allowedOrigins), nil
}
func (s *server) HandleBearerAuth(ctx context.Context, operation api.OperationName, t api.BearerAuth) (context.Context, error) {
	c, err := s.tokens.Parse(t.Token, "access")
	if err != nil {
		code := "TOKEN_INVALID"
		if errors.Is(err, jwt.ErrTokenExpired) {
			code = "TOKEN_EXPIRED"
		}
		return ctx, fault.New(401, code, "Access token недействителен или истёк")
	}
	id, err := uuid.Parse(c.Subject)
	if err != nil {
		return ctx, fault.New(401, "TOKEN_INVALID", "Access token недействителен")
	}
	p := identity.Principal{ID: id, Role: c.Role}
	ctx = identity.WithPrincipal(ctx, p)
	if info := requestInfo(ctx); info != nil {
		info.UserID = id.String()
	}
	switch operation {
	case api.CreateProductOperation, api.UpdateProductOperation, api.DeleteProductOperation, api.CreatePromoCodeOperation:
		if p.Role == "USER" {
			return ctx, fault.Denied()
		}
	case api.CreateOrderOperation, api.GetOrderOperation, api.UpdateOrderOperation, api.CancelOrderOperation:
		if p.Role == "SELLER" {
			return ctx, fault.Denied()
		}
	case api.ChangeOrderStatusOperation:
		if p.Role != "ADMIN" {
			return ctx, fault.Denied()
		}
	}
	return ctx, nil
}
func (s *server) protocolError(ctx context.Context, w http.ResponseWriter, r *http.Request, err error) {
	var f *fault.Error
	switch {
	case errors.As(err, &f):
		writeError(w, f)
	case ogenerrors.ErrorCode(err) == 401:
		writeError(w, fault.New(401, "TOKEN_INVALID", "Требуется access token"))
	default:
		writeError(w, validationError(err))
	}
}
func (s *server) NewError(ctx context.Context, err error) *api.ErrorStatusCode {
	result := contractError(err)
	if result.StatusCode >= 500 {
		s.logger.ErrorContext(ctx, "request failed", "error", err.Error())
	}
	return result
}
func (s *server) GetHealth(context.Context) (*api.Health, error) {
	return &api.Health{Status: api.HealthStatusOk}, nil
}
func (s *server) HeadHealth(ctx context.Context) (*api.Health, error) { return s.GetHealth(ctx) }
func (s *server) CreateProduct(ctx context.Context, r *api.ProductCreate) (api.CreateProductRes, error) {
	return s.catalog.CreateProduct(ctx, identity.FromContext(ctx), r)
}
func (s *server) GetProduct(ctx context.Context, p api.GetProductParams) (api.GetProductRes, error) {
	return s.catalog.GetProduct(ctx, p.ID)
}
func (s *server) ListProducts(ctx context.Context, p api.ListProductsParams) (api.ListProductsRes, error) {
	return s.catalog.ListProducts(ctx, p)
}
func (s *server) UpdateProduct(ctx context.Context, r *api.ProductUpdate, p api.UpdateProductParams) (api.UpdateProductRes, error) {
	return s.catalog.UpdateProduct(ctx, identity.FromContext(ctx), p.ID, r)
}
func (s *server) DeleteProduct(ctx context.Context, p api.DeleteProductParams) (api.DeleteProductRes, error) {
	if err := s.catalog.DeleteProduct(ctx, identity.FromContext(ctx), p.ID); err != nil {
		return nil, err
	}
	return &api.DeleteProductNoContent{}, nil
}
func validateItems(items []api.OrderItemRequest) error {
	seen := map[uuid.UUID]bool{}
	for i, item := range items {
		if seen[item.ProductID] {
			return fault.Validation(fmt.Sprintf("items[%d].product_id", i), "Товар указан повторно")
		}
		seen[item.ProductID] = true
	}
	return nil
}
func (s *server) CreateOrder(ctx context.Context, r *api.OrderCreate) (api.CreateOrderRes, error) {
	if err := validateItems(r.Items); err != nil {
		return nil, err
	}
	return s.orders.CreateOrder(ctx, identity.FromContext(ctx), r)
}
func (s *server) GetOrder(ctx context.Context, p api.GetOrderParams) (api.GetOrderRes, error) {
	return s.orders.GetOrder(ctx, identity.FromContext(ctx), p.ID)
}
func (s *server) UpdateOrder(ctx context.Context, r *api.OrderUpdate, p api.UpdateOrderParams) (api.UpdateOrderRes, error) {
	if err := validateItems(r.Items); err != nil {
		return nil, err
	}
	return s.orders.UpdateOrder(ctx, identity.FromContext(ctx), p.ID, r)
}
func (s *server) CancelOrder(ctx context.Context, p api.CancelOrderParams) (api.CancelOrderRes, error) {
	return s.orders.CancelOrder(ctx, identity.FromContext(ctx), p.ID)
}
func (s *server) ChangeOrderStatus(ctx context.Context, r *api.OrderStatusUpdate, p api.ChangeOrderStatusParams) (api.ChangeOrderStatusRes, error) {
	return s.orders.ChangeOrderStatus(ctx, identity.FromContext(ctx), p.ID, r.Status)
}
func (s *server) CreatePromoCode(ctx context.Context, r *api.PromoCodeCreate) (api.CreatePromoCodeRes, error) {
	if !r.ValidUntil.After(r.ValidFrom) {
		return nil, fault.Validation("valid_until", "Дата окончания должна быть после даты начала")
	}
	return s.orders.CreatePromoCode(ctx, identity.FromContext(ctx), r)
}
func (s *server) Register(ctx context.Context, r *api.RegisterRequest) (api.RegisterRes, error) {
	if len(r.Password) > 72 {
		return nil, fault.Validation("password", "Пароль должен занимать не более 72 байт UTF-8")
	}
	return s.auth.Register(ctx, r)
}
func (s *server) Login(ctx context.Context, r *api.LoginRequest) (api.LoginRes, error) {
	return s.auth.Login(ctx, r)
}
func (s *server) Refresh(ctx context.Context, r *api.RefreshRequest) (api.RefreshRes, error) {
	return s.auth.Refresh(ctx, r)
}
