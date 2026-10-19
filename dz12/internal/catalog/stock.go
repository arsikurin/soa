package catalog

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
	"marketplace/internal/api"
	"marketplace/internal/fault"
	"math"
	"sort"
)

func validMoney(value decimal.Decimal) bool {
	return !value.IsNegative() && value.LessThanOrEqual(decimal.RequireFromString("9999999999.99")) && value.Equal(value.Round(2))
}

type reservedProduct struct {
	product *api.ProductResponse
	stock   int64
}

func productIDs(items []api.OrderItemRequest, old []api.OrderItemResponse) []uuid.UUID {
	set := make(map[uuid.UUID]bool, len(items)+len(old))
	for _, i := range items {
		set[i.ProductID] = true
	}
	for _, i := range old {
		set[i.ProductID] = true
	}
	ids := make([]uuid.UUID, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids
}
func lockProducts(ctx context.Context, tx pgx.Tx, ids []uuid.UUID) (map[uuid.UUID]*reservedProduct, error) {
	rows, err := tx.Query(ctx, "SELECT "+productColumns+" FROM products WHERE id=ANY($1) ORDER BY id FOR UPDATE", ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	products := make(map[uuid.UUID]*reservedProduct, len(ids))
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		products[p.ID] = &reservedProduct{product: p, stock: int64(p.Stock)}
	}
	return products, rows.Err()
}
func restoreItems(products map[uuid.UUID]*reservedProduct, old []api.OrderItemResponse) error {
	for _, item := range old {
		p, ok := products[item.ProductID]
		if !ok {
			return productNotFound()
		}
		p.stock += int64(item.Quantity)
	}
	return nil
}
func reserveItems(products map[uuid.UUID]*reservedProduct, items []api.OrderItemRequest) ([]api.OrderItemResponse, decimal.Decimal, error) {
	for _, item := range items {
		if _, ok := products[item.ProductID]; !ok {
			return nil, decimal.Zero, productNotFound()
		}
	}
	for _, item := range items {
		if products[item.ProductID].product.Status != api.ProductStatusACTIVE {
			return nil, decimal.Zero, fault.New(409, "PRODUCT_INACTIVE", "Товар недоступен для заказа")
		}
	}
	shortages := []map[string]any{}
	for _, item := range items {
		p := products[item.ProductID]
		if p.stock < int64(item.Quantity) {
			shortages = append(shortages, map[string]any{"product_id": item.ProductID.String(), "requested": item.Quantity, "available": p.stock})
		}
	}
	if len(shortages) > 0 {
		err := fault.New(409, "INSUFFICIENT_STOCK", "Недостаточный остаток товара")
		err.Details = map[string]any{"products": shortages}
		return nil, decimal.Zero, err
	}
	result := make([]api.OrderItemResponse, 0, len(items))
	subtotal := decimal.Zero
	for _, item := range items {
		p := products[item.ProductID]
		p.stock -= int64(item.Quantity)
		result = append(result, api.OrderItemResponse{ID: uuid.New(), ProductID: item.ProductID, Quantity: item.Quantity, PriceAtOrder: p.product.Price})
		subtotal = subtotal.Add(p.product.Price.Mul(decimal.NewFromInt(int64(item.Quantity))))
	}
	if !validMoney(subtotal) {
		return nil, decimal.Zero, fault.Validation("items", "Сумма заказа превышает 9999999999.99")
	}
	return result, subtotal, nil
}
func saveStock(ctx context.Context, tx pgx.Tx, ids []uuid.UUID, products map[uuid.UUID]*reservedProduct) error {
	for _, id := range ids {
		p := products[id]
		if p == nil {
			continue
		}
		if p.stock < 0 || p.stock > math.MaxInt32 {
			return fault.Validation("stock", "Остаток выходит за допустимый диапазон")
		}
		if _, err := tx.Exec(ctx, "UPDATE products SET stock=$2 WHERE id=$1", id, p.stock); err != nil {
			return err
		}
	}
	return nil
}
