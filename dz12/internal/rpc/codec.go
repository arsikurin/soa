package rpc

import (
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"

	"marketplace/internal/api"
	"marketplace/internal/fault"
	"marketplace/internal/rpc/pb"
)

var moneyPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,9})(\.[0-9]{1,2})?$`)

type decoder struct{ err error }

func (d *decoder) invalid(field string) {
	if d.err == nil {
		d.err = fault.Validation(field, "Некорректное значение внутреннего протокола")
	}
}
func (d *decoder) uuid(raw, field string) uuid.UUID {
	id, e := uuid.Parse(raw)
	if e != nil || id.String() != raw {
		d.invalid(field)
	}
	return id
}
func (d *decoder) money(raw, field string) decimal.Decimal {
	if !moneyPattern.MatchString(raw) {
		d.invalid(field)
		return decimal.Zero
	}
	v, e := decimal.NewFromString(raw)
	if e != nil {
		d.invalid(field)
	}
	return v
}
func (d *decoder) timestamp(raw *timestamppb.Timestamp, field string) time.Time {
	if raw == nil || raw.CheckValid() != nil {
		d.invalid(field)
		return time.Time{}
	}
	return raw.AsTime()
}
func ptr[T any](v T) *T { return &v }
func optDescriptionToProto(v api.OptNilString) *pb.NullableString {
	if !v.Set {
		return nil
	}
	if v.Null {
		return &pb.NullableString{}
	}
	return &pb.NullableString{Value: ptr(v.Value)}
}
func optDescriptionFromProto(v *pb.NullableString) api.OptNilString {
	if v == nil {
		return api.OptNilString{}
	}
	if v.Value == nil {
		return api.OptNilString{Set: true, Null: true}
	}
	return api.OptNilString{Set: true, Value: *v.Value}
}
func productCreateToProto(v *api.ProductCreate) *pb.ProductWrite {
	r := &pb.ProductWrite{Name: v.Name, Description: optDescriptionToProto(v.Description), Price: v.Price.String(), Stock: v.Stock, Category: v.Category, Status: string(v.Status)}
	if v.SellerID.Set {
		r.SellerId = ptr(v.SellerID.Value.String())
	}
	return r
}
func productUpdateToProto(v *api.ProductUpdate) *pb.ProductWrite {
	return productCreateToProto((*api.ProductCreate)(v))
}
func productCreateFromProto(v *pb.ProductWrite) (*api.ProductCreate, error) {
	d := decoder{}
	if v == nil {
		d.invalid("product")
		return nil, d.err
	}
	r := &api.ProductCreate{Name: v.Name, Description: optDescriptionFromProto(v.Description), Price: d.money(v.Price, "price"), Stock: v.Stock, Category: v.Category, Status: api.ProductStatus(v.Status)}
	if v.SellerId != nil {
		r.SellerID = api.NewOptUUID(d.uuid(*v.SellerId, "seller_id"))
	}
	return r, d.err
}
func productToProto(v *api.ProductResponse) *pb.Product {
	if v == nil {
		return nil
	}
	r := &pb.Product{Id: v.ID.String(), Name: v.Name, Price: v.Price.String(), Stock: v.Stock, Category: v.Category, Status: string(v.Status), SellerId: v.SellerID.String(), CreatedAt: timestamppb.New(v.CreatedAt), UpdatedAt: timestamppb.New(v.UpdatedAt)}
	if !v.Description.Null {
		r.Description = ptr(v.Description.Value)
	}
	return r
}
func productFromProto(v *pb.Product) (*api.ProductResponse, error) {
	d := decoder{}
	if v == nil {
		d.invalid("product")
		return nil, d.err
	}
	r := &api.ProductResponse{ID: d.uuid(v.Id, "id"), Name: v.Name, Price: d.money(v.Price, "price"), Stock: v.Stock, Category: v.Category, Status: api.ProductStatus(v.Status), SellerID: d.uuid(v.SellerId, "seller_id"), CreatedAt: d.timestamp(v.CreatedAt, "created_at"), UpdatedAt: d.timestamp(v.UpdatedAt, "updated_at"), Description: api.NilString{Null: true}}
	if v.Description != nil {
		r.Description.SetTo(*v.Description)
	}
	return r, d.err
}
