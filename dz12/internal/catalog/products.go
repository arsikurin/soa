package catalog

import (
	"context"
	"errors"
	"math"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
	"marketplace/internal/api"
	"marketplace/internal/fault"
	"marketplace/internal/identity"
)

const productColumns = "id,name,description,price::text,stock,category,status,seller_id,created_at,updated_at"

func scanProduct(row pgx.Row) (*api.ProductResponse, error) {
	p := new(api.ProductResponse)
	var price, status string
	var description *string
	if err := row.Scan(&p.ID, &p.Name, &description, &price, &p.Stock, &p.Category, &status, &p.SellerID, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	var err error
	p.Price, err = decimal.NewFromString(price)
	if err != nil {
		return nil, err
	}
	p.Status = api.ProductStatus(status)
	p.Description.Null = description == nil
	if description != nil {
		p.Description.Value = *description
	}
	return p, nil
}
func descriptionValue(d api.OptNilString) any {
	if !d.Set || d.Null {
		return nil
	}
	return d.Value
}

func sellerID(ctx context.Context, q UsersLookup, p identity.Principal, requested api.OptUUID, fallback uuid.UUID) (uuid.UUID, error) {
	id := fallback
	if requested.Set {
		id = requested.Value
	}
	if p.Role != "ADMIN" && id != p.ID {
		return uuid.Nil, fault.Denied()
	}
	role, err := q.LookupRole(ctx, id)
	if missingUser(err) {
		return uuid.Nil, fault.Validation("seller_id", "Продавец не найден")
	} else if err != nil {
		return uuid.Nil, err
	}
	if role != "SELLER" && role != "ADMIN" {
		return uuid.Nil, fault.Validation("seller_id", "Пользователь должен иметь роль SELLER или ADMIN")
	}
	return id, nil
}

func (s *Service) CreateProduct(ctx context.Context, p identity.Principal, r *api.ProductCreate) (*api.ProductResponse, error) {
	if !sellerRole(p) {
		return nil, fault.Denied()
	}
	id, err := sellerID(ctx, s.users, p, r.SellerID, p.ID)
	if err != nil {
		return nil, err
	}
	return scanProduct(s.db.QueryRow(ctx, "INSERT INTO products(name,description,price,stock,category,status,seller_id) VALUES($1,$2,$3::text::numeric,$4,$5,$6,$7) RETURNING "+productColumns, r.Name, descriptionValue(r.Description), r.Price.String(), r.Stock, r.Category, string(r.Status), id))
}
func (s *Service) GetProduct(ctx context.Context, id uuid.UUID) (*api.ProductResponse, error) {
	p, err := scanProduct(s.db.QueryRow(ctx, "SELECT "+productColumns+" FROM products WHERE id=$1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, productNotFound()
	}
	return p, err
}
func (s *Service) UpdateProduct(ctx context.Context, p identity.Principal, id uuid.UUID, r *api.ProductUpdate) (*api.ProductResponse, error) {
	if !sellerRole(p) {
		return nil, fault.Denied()
	}
	return transact(ctx, s.db, pgx.TxOptions{}, func(tx pgx.Tx) (*api.ProductResponse, error) {
		old, err := scanProduct(tx.QueryRow(ctx, "SELECT "+productColumns+" FROM products WHERE id=$1 FOR UPDATE", id))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, productNotFound()
		}
		if err != nil {
			return nil, err
		}
		if p.Role != "ADMIN" && old.SellerID != p.ID {
			return nil, fault.Denied()
		}
		seller, err := sellerID(ctx, s.users, p, r.SellerID, old.SellerID)
		if err != nil {
			return nil, err
		}
		return scanProduct(tx.QueryRow(ctx, "UPDATE products SET name=$2,description=$3,price=$4::text::numeric,stock=$5,category=$6,status=$7,seller_id=$8 WHERE id=$1 RETURNING "+productColumns, id, r.Name, descriptionValue(r.Description), r.Price.String(), r.Stock, r.Category, string(r.Status), seller))
	})
}
func (s *Service) DeleteProduct(ctx context.Context, p identity.Principal, id uuid.UUID) error {
	if !sellerRole(p) {
		return fault.Denied()
	}
	_, err := transact(ctx, s.db, pgx.TxOptions{}, func(tx pgx.Tx) (bool, error) {
		var owner uuid.UUID
		if err := tx.QueryRow(ctx, "SELECT seller_id FROM products WHERE id=$1 FOR UPDATE", id).Scan(&owner); errors.Is(err, pgx.ErrNoRows) {
			return false, productNotFound()
		} else if err != nil {
			return false, err
		}
		if p.Role != "ADMIN" && owner != p.ID {
			return false, fault.Denied()
		}
		_, err := tx.Exec(ctx, "UPDATE products SET status='ARCHIVED' WHERE id=$1", id)
		return err == nil, err
	})
	return err
}
func (s *Service) ListProducts(ctx context.Context, params api.ListProductsParams) (*api.ProductPage, error) {
	page, size := params.Page.Or(0), params.Size.Or(20)
	if page < 0 {
		return nil, fault.Validation("page", "Значение должно быть неотрицательным")
	}
	if size < 1 || size > 100 {
		return nil, fault.Validation("size", "Допустимое значение от 1 до 100")
	}
	offset := int64(math.MaxInt64)
	if int64(page) <= math.MaxInt64/int64(size) {
		offset = int64(page) * int64(size)
	}
	var status, category any
	if params.Status.Set {
		status = string(params.Status.Value)
	}
	if params.Category.Set {
		category = params.Category.Value
	}
	return transact(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) (*api.ProductPage, error) {
		out := &api.ProductPage{Page: page, Size: size, Items: []api.ProductResponse{}}
		const where = " WHERE ($1::product_status IS NULL OR status=$1::product_status) AND ($2::text IS NULL OR category=$2)"
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM products"+where, status, category).Scan(&out.TotalElements); err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, "SELECT "+productColumns+" FROM products"+where+" ORDER BY created_at,id LIMIT $3 OFFSET $4", status, category, size, offset)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanProduct(rows)
			if err != nil {
				return nil, err
			}
			out.Items = append(out.Items, *p)
		}
		return out, rows.Err()
	})
}
