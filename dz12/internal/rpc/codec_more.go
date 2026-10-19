package rpc

import (
	"google.golang.org/protobuf/types/known/timestamppb"
	"marketplace/internal/api"
	"marketplace/internal/rpc/pb"
)

func userToProto(v *api.UserResponse) *pb.User {
	if v == nil {
		return nil
	}
	return &pb.User{Id: v.ID.String(), Email: v.Email, Role: string(v.Role), CreatedAt: timestamppb.New(v.CreatedAt), UpdatedAt: timestamppb.New(v.UpdatedAt)}
}
func userFromProto(v *pb.User) (*api.UserResponse, error) {
	d := decoder{}
	if v == nil {
		d.invalid("user")
		return nil, d.err
	}
	return &api.UserResponse{ID: d.uuid(v.Id, "id"), Email: v.Email, Role: api.Role(v.Role), CreatedAt: d.timestamp(v.CreatedAt, "created_at"), UpdatedAt: d.timestamp(v.UpdatedAt, "updated_at")}, d.err
}
func tokensToProto(v *api.TokenPair) *pb.TokenPair {
	if v == nil {
		return nil
	}
	return &pb.TokenPair{AccessToken: v.AccessToken, RefreshToken: v.RefreshToken, TokenType: string(v.TokenType), ExpiresIn: v.ExpiresIn}
}
func tokensFromProto(v *pb.TokenPair) (*api.TokenPair, error) {
	d := decoder{}
	if v == nil {
		d.invalid("tokens")
		return nil, d.err
	}
	return &api.TokenPair{AccessToken: v.AccessToken, RefreshToken: v.RefreshToken, TokenType: api.TokenPairTokenType(v.TokenType), ExpiresIn: v.ExpiresIn}, nil
}
func listToProto(v api.ListProductsParams) *pb.ListProductsRequest {
	r := &pb.ListProductsRequest{}
	if v.Page.Set {
		r.Page = ptr(int64(v.Page.Value))
	}
	if v.Size.Set {
		r.Size = ptr(int64(v.Size.Value))
	}
	if v.Status.Set {
		r.Status = ptr(string(v.Status.Value))
	}
	if v.Category.Set {
		r.Category = ptr(v.Category.Value)
	}
	return r
}
func listFromProto(v *pb.ListProductsRequest) api.ListProductsParams {
	r := api.ListProductsParams{}
	if v.Page != nil {
		r.Page = api.NewOptInt(int(*v.Page))
	}
	if v.Size != nil {
		r.Size = api.NewOptInt(int(*v.Size))
	}
	if v.Status != nil {
		r.Status = api.NewOptProductStatus(api.ProductStatus(*v.Status))
	}
	if v.Category != nil {
		r.Category = api.NewOptString(*v.Category)
	}
	return r
}
func pageToProto(v *api.ProductPage) *pb.ProductPage {
	if v == nil {
		return nil
	}
	r := &pb.ProductPage{Items: make([]*pb.Product, len(v.Items)), Page: int64(v.Page), Size: int64(v.Size), TotalElements: v.TotalElements}
	for i := range v.Items {
		r.Items[i] = productToProto(&v.Items[i])
	}
	return r
}
func pageFromProto(v *pb.ProductPage) (*api.ProductPage, error) {
	d := decoder{}
	if v == nil {
		d.invalid("page")
		return nil, d.err
	}
	r := &api.ProductPage{Items: make([]api.ProductResponse, len(v.Items)), Page: int(v.Page), Size: int(v.Size), TotalElements: v.TotalElements}
	for i, item := range v.Items {
		p, err := productFromProto(item)
		if err != nil {
			return nil, err
		}
		r.Items[i] = *p
	}
	return r, nil
}
func requestItemsToProto(v []api.OrderItemRequest) []*pb.OrderItemRequest {
	r := make([]*pb.OrderItemRequest, len(v))
	for i, item := range v {
		r[i] = &pb.OrderItemRequest{ProductId: item.ProductID.String(), Quantity: item.Quantity}
	}
	return r
}
func (d *decoder) requestItems(v []*pb.OrderItemRequest) []api.OrderItemRequest {
	r := make([]api.OrderItemRequest, len(v))
	for i, item := range v {
		if item == nil {
			d.invalid("items")
			continue
		}
		r[i] = api.OrderItemRequest{ProductID: d.uuid(item.ProductId, "product_id"), Quantity: item.Quantity}
		if item.Quantity < 1 {
			d.invalid("quantity")
		}
	}
	return r
}
func itemsToProto(v []api.OrderItemResponse) []*pb.OrderItem {
	r := make([]*pb.OrderItem, len(v))
	for i, item := range v {
		r[i] = &pb.OrderItem{Id: item.ID.String(), ProductId: item.ProductID.String(), Quantity: item.Quantity, PriceAtOrder: item.PriceAtOrder.String()}
	}
	return r
}
func (d *decoder) items(v []*pb.OrderItem) []api.OrderItemResponse {
	r := make([]api.OrderItemResponse, len(v))
	for i, item := range v {
		if item == nil {
			d.invalid("items")
			continue
		}
		r[i] = api.OrderItemResponse{ID: d.uuid(item.Id, "id"), ProductID: d.uuid(item.ProductId, "product_id"), Quantity: item.Quantity, PriceAtOrder: d.money(item.PriceAtOrder, "price_at_order")}
		if item.Quantity < 1 {
			d.invalid("quantity")
		}
	}
	return r
}
func orderToProto(v *api.OrderResponse) *pb.Order {
	if v == nil {
		return nil
	}
	r := &pb.Order{Id: v.ID.String(), UserId: v.UserID.String(), Status: string(v.Status), TotalAmount: v.TotalAmount.String(), DiscountAmount: v.DiscountAmount.String(), Items: itemsToProto(v.Items), CreatedAt: timestamppb.New(v.CreatedAt), UpdatedAt: timestamppb.New(v.UpdatedAt)}
	if !v.PromoCodeID.Null {
		r.PromoCodeId = ptr(v.PromoCodeID.Value.String())
	}
	return r
}
func orderFromProto(v *pb.Order) (*api.OrderResponse, error) {
	d := decoder{}
	if v == nil {
		d.invalid("order")
		return nil, d.err
	}
	r := &api.OrderResponse{ID: d.uuid(v.Id, "id"), UserID: d.uuid(v.UserId, "user_id"), Status: api.OrderStatus(v.Status), TotalAmount: d.money(v.TotalAmount, "total_amount"), DiscountAmount: d.money(v.DiscountAmount, "discount_amount"), Items: d.items(v.Items), PromoCodeID: api.NilUUID{Null: true}, CreatedAt: d.timestamp(v.CreatedAt, "created_at"), UpdatedAt: d.timestamp(v.UpdatedAt, "updated_at")}
	if v.PromoCodeId != nil {
		r.PromoCodeID.SetTo(d.uuid(*v.PromoCodeId, "promo_code_id"))
	}
	return r, d.err
}
func promoCreateToProto(v *api.PromoCodeCreate) *pb.PromoCodeCreateRequest {
	return &pb.PromoCodeCreateRequest{Code: v.Code, DiscountType: string(v.DiscountType), DiscountValue: v.DiscountValue.String(), MinOrderAmount: v.MinOrderAmount.String(), MaxUses: v.MaxUses, ValidFrom: timestamppb.New(v.ValidFrom), ValidUntil: timestamppb.New(v.ValidUntil), Active: v.Active.Or(true)}
}
func promoCreateFromProto(v *pb.PromoCodeCreateRequest) (*api.PromoCodeCreate, error) {
	d := decoder{}
	if v == nil {
		d.invalid("promo_code")
		return nil, d.err
	}
	r := &api.PromoCodeCreate{Code: v.Code, DiscountType: api.DiscountType(v.DiscountType), DiscountValue: d.money(v.DiscountValue, "discount_value"), MinOrderAmount: d.money(v.MinOrderAmount, "min_order_amount"), MaxUses: v.MaxUses, ValidFrom: d.timestamp(v.ValidFrom, "valid_from"), ValidUntil: d.timestamp(v.ValidUntil, "valid_until"), Active: api.NewOptBool(v.Active)}
	return r, d.err
}
func promoToProto(v *api.PromoCodeResponse) *pb.PromoCode {
	if v == nil {
		return nil
	}
	return &pb.PromoCode{Id: v.ID.String(), Code: v.Code, DiscountType: string(v.DiscountType), DiscountValue: v.DiscountValue.String(), MinOrderAmount: v.MinOrderAmount.String(), MaxUses: v.MaxUses, ValidFrom: timestamppb.New(v.ValidFrom), ValidUntil: timestamppb.New(v.ValidUntil), Active: v.Active, CurrentUses: v.CurrentUses, CreatedAt: timestamppb.New(v.CreatedAt), UpdatedAt: timestamppb.New(v.UpdatedAt)}
}
func promoFromProto(v *pb.PromoCode) (*api.PromoCodeResponse, error) {
	d := decoder{}
	if v == nil {
		d.invalid("promo_code")
		return nil, d.err
	}
	r := &api.PromoCodeResponse{ID: d.uuid(v.Id, "id"), Code: v.Code, DiscountType: api.DiscountType(v.DiscountType), DiscountValue: d.money(v.DiscountValue, "discount_value"), MinOrderAmount: d.money(v.MinOrderAmount, "min_order_amount"), MaxUses: v.MaxUses, ValidFrom: d.timestamp(v.ValidFrom, "valid_from"), ValidUntil: d.timestamp(v.ValidUntil, "valid_until"), Active: v.Active, CurrentUses: v.CurrentUses, CreatedAt: d.timestamp(v.CreatedAt, "created_at"), UpdatedAt: d.timestamp(v.UpdatedAt, "updated_at")}
	return r, d.err
}
