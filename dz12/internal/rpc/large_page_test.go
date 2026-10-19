package rpc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"marketplace/internal/api"
	"marketplace/internal/fault"
	"marketplace/internal/platform"
)

type largePageCatalog struct {
	CatalogBackend
	page *api.ProductPage
}

func (c largePageCatalog) ListProducts(context.Context, api.ListProductsParams) (*api.ProductPage, error) {
	return c.page, nil
}
func TestMaximumUnicodeProductPageFitsResponseLimit(t *testing.T) {
	name, description, category := strings.Repeat("😀", 255), strings.Repeat("😀", 4000), strings.Repeat("😀", 100)
	page := &api.ProductPage{Items: make([]api.ProductResponse, 100), Page: 0, Size: 100, TotalElements: 100}
	for i := range page.Items {
		page.Items[i] = api.ProductResponse{ID: testID, SellerID: testID, Name: name, Description: api.NilString{Value: description}, Price: decimal.RequireFromString("9999999999.99"), Stock: 2147483647, Category: category, Status: api.ProductStatusACTIVE, CreatedAt: testTime, UpdatedAt: testTime}
	}
	if err := page.Validate(); err != nil {
		t.Fatalf("maximum-length fixture violates external contract: %v", err)
	}
	size := proto.Size(pageToProto(page))
	t.Logf("maximum Unicode page protobuf payload: %d bytes", size)
	if size <= 1<<20 || size > platform.MaxResponseBytes {
		t.Fatalf("fixture size%d must exceed old1MiB while fitting current response limit%d", size, platform.MaxResponseBytes)
	}
	t.Run("old limit reproduces rejection", func(t *testing.T) {
		cc := testConnection(t, func(s *grpc.Server) { RegisterCatalogServer(s, largePageCatalog{page: page}) }, grpc.MaxSendMsgSize(1<<20))
		_, err := NewCatalogClient(cc).ListProducts(context.Background(), api.ListProductsParams{Size: api.NewOptInt(100)})
		var f *fault.Error
		if !errors.As(err, &f) || f.Status != 503 {
			t.Fatalf("old limit did not reject oversized valid response: %v", err)
		}
	})
	t.Run("configured limits allow full page", func(t *testing.T) {
		cc := testConnection(t, func(s *grpc.Server) { RegisterCatalogServer(s, largePageCatalog{page: page}) })
		got, err := NewCatalogClient(cc).ListProducts(context.Background(), api.ListProductsParams{Size: api.NewOptInt(100)})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Items) != 100 || got.Size != 100 || got.TotalElements != 100 {
			t.Fatalf("page truncated: size%d items%d total%d", got.Size, len(got.Items), got.TotalElements)
		}
		for i, p := range got.Items {
			if p.Name != name || p.Description.Value != description || p.Description.Null || p.Category != category || p.Stock != 2147483647 || p.Price.String() != "9999999999.99" {
				t.Fatalf("product%d lost fields in large response", i)
			}
		}
	})
}
