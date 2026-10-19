package rpc

import (
	"context"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
	"marketplace/internal/api"
	"marketplace/internal/rpc/pb"
)

type usersServer struct {
	pb.UnimplementedUsersServer
	backend UsersBackend
}
type catalogServer struct {
	pb.UnimplementedCatalogServer
	backend CatalogBackend
}
type ordersServer struct {
	pb.UnimplementedOrdersServer
	backend OrdersBackend
}

func RegisterUsersServer(s *grpc.Server, backend UsersBackend) {
	pb.RegisterUsersServer(s, &usersServer{backend: backend})
}
func RegisterCatalogServer(s *grpc.Server, backend CatalogBackend) {
	pb.RegisterCatalogServer(s, &catalogServer{backend: backend})
}
func RegisterOrdersServer(s *grpc.Server, backend OrdersBackend) {
	pb.RegisterOrdersServer(s, &ordersServer{backend: backend})
}

func (s *usersServer) Register(ctx context.Context, r *pb.RegisterRequest) (*pb.User, error) {
	req := &api.RegisterRequest{Email: r.Email, Password: r.Password}
	if r.Role != nil {
		req.Role = api.NewOptRegisterRequestRole(api.RegisterRequestRole(*r.Role))
	}
	v, e := s.backend.Register(ctx, req)
	return userToProto(v), toRPCError(e)
}
func (s *usersServer) Login(ctx context.Context, r *pb.LoginRequest) (*pb.TokenPair, error) {
	v, e := s.backend.Login(ctx, &api.LoginRequest{Email: r.Email, Password: r.Password})
	return tokensToProto(v), toRPCError(e)
}
func (s *usersServer) Refresh(ctx context.Context, r *pb.RefreshRequest) (*pb.TokenPair, error) {
	v, e := s.backend.Refresh(ctx, &api.RefreshRequest{RefreshToken: r.RefreshToken})
	return tokensToProto(v), toRPCError(e)
}
func (s *usersServer) LookupRole(ctx context.Context, r *pb.IDRequest) (*pb.RoleResponse, error) {
	d := decoder{}
	id := d.uuid(r.Id, "id")
	if d.err != nil {
		return nil, toRPCError(d.err)
	}
	role, e := s.backend.LookupRole(ctx, id)
	return &pb.RoleResponse{Role: role}, toRPCError(e)
}
func (s *catalogServer) CreateProduct(ctx context.Context, r *pb.ProductWrite) (*pb.Product, error) {
	p, e := principalFromContext(ctx)
	if e != nil {
		return nil, e
	}
	req, e := productCreateFromProto(r)
	if e != nil {
		return nil, toRPCError(e)
	}
	v, e := s.backend.CreateProduct(ctx, p, req)
	return productToProto(v), toRPCError(e)
}
func (s *catalogServer) GetProduct(ctx context.Context, r *pb.IDRequest) (*pb.Product, error) {
	d := decoder{}
	id := d.uuid(r.Id, "id")
	if d.err != nil {
		return nil, toRPCError(d.err)
	}
	v, e := s.backend.GetProduct(ctx, id)
	return productToProto(v), toRPCError(e)
}
func (s *catalogServer) ListProducts(ctx context.Context, r *pb.ListProductsRequest) (*pb.ProductPage, error) {
	v, e := s.backend.ListProducts(ctx, listFromProto(r))
	return pageToProto(v), toRPCError(e)
}
func (s *catalogServer) UpdateProduct(ctx context.Context, r *pb.ProductUpdateRequest) (*pb.Product, error) {
	p, e := principalFromContext(ctx)
	if e != nil {
		return nil, e
	}
	d := decoder{}
	id := d.uuid(r.Id, "id")
	if d.err != nil {
		return nil, toRPCError(d.err)
	}
	req, e := productCreateFromProto(r.Product)
	if e != nil {
		return nil, toRPCError(e)
	}
	v, e := s.backend.UpdateProduct(ctx, p, id, (*api.ProductUpdate)(req))
	return productToProto(v), toRPCError(e)
}
func (s *catalogServer) DeleteProduct(ctx context.Context, r *pb.IDRequest) (*emptypb.Empty, error) {
	p, e := principalFromContext(ctx)
	if e != nil {
		return nil, e
	}
	d := decoder{}
	id := d.uuid(r.Id, "id")
	if d.err != nil {
		return nil, toRPCError(d.err)
	}
	e = s.backend.DeleteProduct(ctx, p, id)
	return &emptypb.Empty{}, toRPCError(e)
}
func (s *catalogServer) PrepareReservation(ctx context.Context, r *pb.PrepareReservationRequest) (*pb.PrepareReservationResponse, error) {
	d := decoder{}
	d.uuid(r.TransactionId, "transaction_id")
	old, newItems := d.items(r.OldItems), d.requestItems(r.NewItems)
	if d.err != nil {
		return nil, toRPCError(d.err)
	}
	v, e := s.backend.PrepareReservation(ctx, r.TransactionId, old, newItems)
	return &pb.PrepareReservationResponse{Items: itemsToProto(v)}, toRPCError(e)
}
func (s *catalogServer) ResolveReservation(ctx context.Context, r *pb.ResolveReservationRequest) (*emptypb.Empty, error) {
	d := decoder{}
	d.uuid(r.TransactionId, "transaction_id")
	if d.err != nil {
		return nil, toRPCError(d.err)
	}
	e := s.backend.ResolveReservation(ctx, r.TransactionId, r.Commit)
	return &emptypb.Empty{}, toRPCError(e)
}
func (s *ordersServer) CreateOrder(ctx context.Context, r *pb.OrderCreateRequest) (*pb.Order, error) {
	p, e := principalFromContext(ctx)
	if e != nil {
		return nil, e
	}
	ctx, e = operationFromMetadata(ctx, "")
	if e != nil {
		return nil, e
	}
	d := decoder{}
	req := &api.OrderCreate{Items: d.requestItems(r.Items)}
	if r.PromoCode != nil {
		req.PromoCode = api.NewOptString(*r.PromoCode)
	}
	if d.err != nil {
		return nil, toRPCError(d.err)
	}
	v, e := s.backend.CreateOrder(ctx, p, req)
	return orderToProto(v), toRPCError(e)
}
func (s *ordersServer) GetOrder(ctx context.Context, r *pb.IDRequest) (*pb.Order, error) {
	p, e := principalFromContext(ctx)
	if e != nil {
		return nil, e
	}
	d := decoder{}
	id := d.uuid(r.Id, "id")
	if d.err != nil {
		return nil, toRPCError(d.err)
	}
	v, e := s.backend.GetOrder(ctx, p, id)
	return orderToProto(v), toRPCError(e)
}
func (s *ordersServer) UpdateOrder(ctx context.Context, r *pb.OrderUpdateRequest) (*pb.Order, error) {
	p, e := principalFromContext(ctx)
	if e != nil {
		return nil, e
	}
	ctx, e = operationFromMetadata(ctx, r.Id)
	if e != nil {
		return nil, e
	}
	d := decoder{}
	id := d.uuid(r.Id, "id")
	req := &api.OrderUpdate{Items: d.requestItems(r.Items)}
	if d.err != nil {
		return nil, toRPCError(d.err)
	}
	v, e := s.backend.UpdateOrder(ctx, p, id, req)
	return orderToProto(v), toRPCError(e)
}
func (s *ordersServer) CancelOrder(ctx context.Context, r *pb.IDRequest) (*pb.Order, error) {
	p, e := principalFromContext(ctx)
	if e != nil {
		return nil, e
	}
	ctx, e = operationFromMetadata(ctx, r.Id)
	if e != nil {
		return nil, e
	}
	d := decoder{}
	id := d.uuid(r.Id, "id")
	if d.err != nil {
		return nil, toRPCError(d.err)
	}
	v, e := s.backend.CancelOrder(ctx, p, id)
	return orderToProto(v), toRPCError(e)
}
func (s *ordersServer) ChangeOrderStatus(ctx context.Context, r *pb.OrderStatusRequest) (*pb.Order, error) {
	p, e := principalFromContext(ctx)
	if e != nil {
		return nil, e
	}
	d := decoder{}
	id := d.uuid(r.Id, "id")
	state := api.OrderStatusUpdateStatus(r.Status)
	if state.Validate() != nil {
		d.invalid("status")
	}
	if d.err != nil {
		return nil, toRPCError(d.err)
	}
	v, e := s.backend.ChangeOrderStatus(ctx, p, id, state)
	return orderToProto(v), toRPCError(e)
}
func (s *ordersServer) CreatePromoCode(ctx context.Context, r *pb.PromoCodeCreateRequest) (*pb.PromoCode, error) {
	p, e := principalFromContext(ctx)
	if e != nil {
		return nil, e
	}
	req, e := promoCreateFromProto(r)
	if e != nil {
		return nil, toRPCError(e)
	}
	v, e := s.backend.CreatePromoCode(ctx, p, req)
	return promoToProto(v), toRPCError(e)
}
