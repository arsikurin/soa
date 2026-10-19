package orders

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"marketplace/internal/api"
	"marketplace/internal/catalog"
	"marketplace/internal/fault"
	"marketplace/internal/identity"
)

type fixture struct {
	t                          *testing.T
	ctx                        context.Context
	db                         *pgxpool.Pool
	svc                        *testServices
	catalogDB                  *pgxpool.Pool
	usersDB                    *pgxpool.Pool
	seller, admin, user, other identity.Principal
	category                   string
	promos                     []uuid.UUID
}

type productService = catalog.Service
type testServices struct {
	*Service
	*productService
}
type lookupUsers struct{ db *pgxpool.Pool }

func (u lookupUsers) LookupRole(ctx context.Context, id uuid.UUID) (string, error) {
	var role string
	err := u.db.QueryRow(ctx, "SELECT role FROM users WHERE id=$1", id).Scan(&role)
	return role, err
}
func testPool(t *testing.T, env string) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(env)
	if dsn == "" {
		t.Skip(env + " is not set")
	}
	db, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}
func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	users := testPool(t, "TEST_USERS_DATABASE_URL")
	cat := testPool(t, "TEST_CATALOG_DATABASE_URL")
	db := testPool(t, "TEST_ORDERS_DATABASE_URL")
	cs := catalog.NewService(cat, lookupUsers{users})
	f := &fixture{t: t, ctx: ctx, db: db, catalogDB: cat, usersDB: users, category: uuid.NewString(), svc: &testServices{Service: NewService(db, cs, 0), productService: cs}}
	actors := []*identity.Principal{&f.seller, &f.admin, &f.user, &f.other}
	roles := []string{"SELLER", "ADMIN", "USER", "USER"}
	for i, p := range actors {
		p.ID = uuid.New()
		p.Role = roles[i]
		if _, err := users.Exec(ctx, "INSERT INTO users(id,email,password_hash,role) VALUES($1,$2,'unused',$3)", p.ID, p.ID.String()+"@business.test", p.Role); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := f.svc.Recover(cleanup); err != nil {
			t.Errorf("recover cleanup: %v", err)
		}
		ids := []uuid.UUID{f.seller.ID, f.admin.ID, f.user.ID, f.other.ID}
		for _, q := range []string{"DELETE FROM orders WHERE user_id=ANY($1)", "DELETE FROM user_operations WHERE user_id=ANY($1)", "DELETE FROM actor_locks WHERE id=ANY($1)"} {
			if _, err := db.Exec(cleanup, q, ids); err != nil {
				t.Error(err)
			}
		}
		if _, err := cat.Exec(cleanup, "DELETE FROM products WHERE seller_id=ANY($1)", ids); err != nil {
			t.Error(err)
		}
		if _, err := users.Exec(cleanup, "DELETE FROM users WHERE id=ANY($1)", ids); err != nil {
			t.Error(err)
		}
		if len(f.promos) > 0 {
			if _, err := db.Exec(cleanup, "DELETE FROM promo_codes WHERE id=ANY($1)", f.promos); err != nil {
				t.Error(err)
			}
		}
	})
	return f
}

func (f *fixture) product(price string, stock int32) *api.ProductResponse {
	f.t.Helper()
	p, e := f.svc.CreateProduct(f.ctx, f.seller, &api.ProductCreate{Name: "Product", Price: decimal.RequireFromString(price), Stock: stock, Category: f.category, Status: api.ProductStatusACTIVE})
	if e != nil {
		f.t.Fatal(e)
	}
	return p
}
func (f *fixture) promo(kind api.DiscountType, value, min string, max int32) *api.PromoCodeResponse {
	f.t.Helper()
	p, e := f.svc.CreatePromoCode(f.ctx, f.seller, &api.PromoCodeCreate{Code: "TEST_" + strings.ToUpper(uuid.NewString()[:8]), DiscountType: kind, DiscountValue: decimal.RequireFromString(value), MinOrderAmount: decimal.RequireFromString(min), MaxUses: max, ValidFrom: time.Now().Add(-time.Hour), ValidUntil: time.Now().Add(time.Hour), Active: api.NewOptBool(true)})
	if e != nil {
		f.t.Fatal(e)
	}
	f.promos = append(f.promos, p.ID)
	return p
}
func (f *fixture) stock(id uuid.UUID, want int32) {
	f.t.Helper()
	var got int32
	if e := f.catalogDB.QueryRow(f.ctx, "SELECT stock FROM products WHERE id=$1", id).Scan(&got); e != nil {
		f.t.Fatal(e)
	}
	if got != want {
		f.t.Fatalf("stock=%d, want %d", got, want)
	}
}
func (f *fixture) uses(id uuid.UUID, want int32) {
	f.t.Helper()
	var got int32
	if e := f.db.QueryRow(f.ctx, "SELECT current_uses FROM promo_codes WHERE id=$1", id).Scan(&got); e != nil {
		f.t.Fatal(e)
	}
	if got != want {
		f.t.Fatalf("uses=%d, want %d", got, want)
	}
}
func mustCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *fault.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("error=%v, want %s", err, code)
	}
}
func item(p *api.ProductResponse, q int32) api.OrderItemRequest {
	return api.OrderItemRequest{ProductID: p.ID, Quantity: q}
}
func productUpdate(p *api.ProductResponse) *api.ProductUpdate {
	return &api.ProductUpdate{Name: p.Name, Price: p.Price, Stock: p.Stock, Category: p.Category, Status: p.Status}
}

func TestProductCRUDAndOwnership(t *testing.T) {
	f := newFixture(t)
	p := f.product("19.99", 10)
	if !p.Description.Null || p.SellerID != f.seller.ID || p.CreatedAt.IsZero() {
		t.Fatalf("bad product: %+v", p)
	}
	_, err := f.svc.CreateProduct(f.ctx, f.user, &api.ProductCreate{})
	mustCode(t, err, "ACCESS_DENIED")
	r := productUpdate(p)
	r.Name = "Changed"
	r.Description = api.NewOptNilString("description")
	r.Stock = 4
	updated, err := f.svc.UpdateProduct(f.ctx, f.seller, p.ID, r)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Changed" || updated.Description.Value != "description" || updated.Stock != 4 {
		t.Fatal(updated)
	}
	_, err = f.svc.UpdateProduct(f.ctx, identity.Principal{ID: f.other.ID, Role: "SELLER"}, p.ID, r)
	mustCode(t, err, "ACCESS_DENIED")
	r.SellerID = api.NewOptUUID(f.admin.ID)
	_, err = f.svc.UpdateProduct(f.ctx, f.seller, p.ID, r)
	mustCode(t, err, "ACCESS_DENIED")
	r.SellerID = api.OptUUID{}
	r.Status = api.ProductStatusINACTIVE
	if _, err = f.svc.UpdateProduct(f.ctx, f.admin, p.ID, r); err != nil {
		t.Fatal(err)
	}
	f.product("1", 1)
	page, err := f.svc.ListProducts(f.ctx, api.ListProductsParams{Category: api.NewOptString(f.category), Status: api.NewOptProductStatus(api.ProductStatusACTIVE), Size: api.NewOptInt(1)})
	if err != nil {
		t.Fatal(err)
	}
	if page.TotalElements != 1 || len(page.Items) != 1 || page.Page != 0 || page.Size != 1 {
		t.Fatalf("page: %+v", page)
	}
	if err = f.svc.DeleteProduct(f.ctx, f.seller, p.ID); err != nil {
		t.Fatal(err)
	}
	archived, err := f.svc.GetProduct(f.ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if archived.Status != api.ProductStatusARCHIVED {
		t.Fatal(archived.Status)
	}
	page, err = f.svc.ListProducts(f.ctx, api.ListProductsParams{Category: api.NewOptString(f.category), Page: api.NewOptInt(100)})
	if err != nil {
		t.Fatal(err)
	}
	if page.TotalElements != 2 || len(page.Items) != 0 {
		t.Fatal(page)
	}
	_, err = f.svc.GetProduct(f.ctx, uuid.New())
	mustCode(t, err, "PRODUCT_NOT_FOUND")
}

func TestOrderRollbackSnapshotsAndCancellation(t *testing.T) {
	f := newFixture(t)
	a := f.product("10", 5)
	b := f.product("20", 1)
	promo := f.promo(api.DiscountTypePERCENTAGE, "10", "30", 1)
	order, err := f.svc.CreateOrder(f.ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(a, 4)}, PromoCode: api.NewOptString(promo.Code)})
	if err != nil {
		t.Fatal(err)
	}
	if !order.TotalAmount.Equal(decimal.NewFromInt(36)) || !order.DiscountAmount.Equal(decimal.NewFromInt(4)) {
		t.Fatal(order)
	}
	f.stock(a.ID, 1)
	f.uses(promo.ID, 1)
	upd := productUpdate(a)
	upd.Price = decimal.NewFromInt(15)
	upd.Stock = 1
	if _, err = f.svc.UpdateProduct(f.ctx, f.seller, a.ID, upd); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.GetOrder(f.ctx, f.user, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Items[0].PriceAtOrder.Equal(decimal.NewFromInt(10)) {
		t.Fatal("snapshot changed")
	}
	_, err = f.svc.UpdateOrder(f.ctx, f.user, order.ID, &api.OrderUpdate{Items: []api.OrderItemRequest{item(a, 2), item(b, 2)}})
	mustCode(t, err, "INSUFFICIENT_STOCK")
	f.stock(a.ID, 1)
	f.stock(b.ID, 1)
	f.uses(promo.ID, 1)
	got, err = f.svc.GetOrder(f.ctx, f.user, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Items[0].Quantity != 4 || !got.TotalAmount.Equal(decimal.NewFromInt(36)) {
		t.Fatal("failed update persisted")
	}
	got, err = f.svc.UpdateOrder(f.ctx, f.user, order.ID, &api.OrderUpdate{Items: []api.OrderItemRequest{item(a, 2)}})
	if err != nil {
		t.Fatal(err)
	}
	if !got.TotalAmount.Equal(decimal.NewFromInt(27)) || !got.Items[0].PriceAtOrder.Equal(decimal.NewFromInt(15)) {
		t.Fatal(got)
	}
	f.stock(a.ID, 3)
	f.uses(promo.ID, 1)
	got, err = f.svc.UpdateOrder(f.ctx, f.user, order.ID, &api.OrderUpdate{Items: []api.OrderItemRequest{item(a, 1)}})
	if err != nil {
		t.Fatal(err)
	}
	if !got.PromoCodeID.Null || !got.DiscountAmount.IsZero() || !got.TotalAmount.Equal(decimal.NewFromInt(15)) {
		t.Fatal(got)
	}
	f.uses(promo.ID, 0)
	_, err = f.svc.GetOrder(f.ctx, f.other, order.ID)
	mustCode(t, err, "ORDER_OWNERSHIP_VIOLATION")
	_, err = f.svc.CancelOrder(f.ctx, f.other, order.ID)
	mustCode(t, err, "ORDER_OWNERSHIP_VIOLATION")
	got, err = f.svc.CancelOrder(f.ctx, f.admin, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != api.OrderStatusCANCELED {
		t.Fatal(got)
	}
	f.stock(a.ID, 5)
	_, err = f.svc.CancelOrder(f.ctx, f.user, order.ID)
	mustCode(t, err, "INVALID_STATE_TRANSITION")
	f.stock(a.ID, 5)
}

func TestCreateOrderFailureIsAtomic(t *testing.T) {
	f := newFixture(t)
	a := f.product("10", 3)
	b := f.product("20", 0)
	_, err := f.svc.CreateOrder(f.ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(a, 4), item(b, 1)}})
	mustCode(t, err, "INSUFFICIENT_STOCK")
	var problem *fault.Error
	errors.As(err, &problem)
	shortages, ok := problem.Details["products"].([]map[string]any)
	if !ok || len(shortages) != 2 {
		t.Fatalf("shortages=%v", problem.Details)
	}
	f.stock(a.ID, 3)
	_, err = f.svc.CreateOrder(f.ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(a, 1)}, PromoCode: api.NewOptString("MISSING")})
	mustCode(t, err, "PROMO_CODE_INVALID")
	f.stock(a.ID, 3)
	promo := f.promo(api.DiscountTypeFIXEDAMOUNT, "10", "50", 1)
	_, err = f.svc.CreateOrder(f.ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(a, 1)}, PromoCode: api.NewOptString(promo.Code)})
	mustCode(t, err, "PROMO_CODE_MIN_AMOUNT")
	f.stock(a.ID, 3)
	f.uses(promo.ID, 0)
	var orders, operations int
	if err = f.db.QueryRow(f.ctx, "SELECT (SELECT count(*) FROM orders WHERE user_id=$1),(SELECT count(*) FROM user_operations WHERE user_id=$1)", f.user.ID).Scan(&orders, &operations); err != nil {
		t.Fatal(err)
	}
	if orders != 0 || operations != 0 {
		t.Fatalf("failed create left orders=%d operations=%d", orders, operations)
	}
	_, err = f.svc.CreateOrder(f.ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(a, 9), {ProductID: uuid.New(), Quantity: 1}}})
	mustCode(t, err, "PRODUCT_NOT_FOUND")
	if err = f.svc.DeleteProduct(f.ctx, f.seller, b.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.CreateOrder(f.ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(a, 9), item(b, 1)}})
	mustCode(t, err, "PRODUCT_INACTIVE")
	huge := f.product("9999999999.99", 2)
	_, err = f.svc.CreateOrder(f.ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(huge, 2)}})
	mustCode(t, err, "VALIDATION_ERROR")
	f.stock(huge.ID, 2)
}

func TestPromoRevalidationRollsBack(t *testing.T) {
	f := newFixture(t)
	p := f.product("20", 5)
	promo := f.promo(api.DiscountTypePERCENTAGE, "80", "0", 1)
	order, err := f.svc.CreateOrder(f.ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(p, 2)}, PromoCode: api.NewOptString(promo.Code)})
	if err != nil {
		t.Fatal(err)
	}
	if !order.TotalAmount.Equal(decimal.NewFromInt(40)) || !order.DiscountAmount.IsZero() {
		t.Fatal(order)
	}
	if _, err = f.db.Exec(f.ctx, "UPDATE promo_codes SET active=false WHERE id=$1", promo.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.UpdateOrder(f.ctx, f.user, order.ID, &api.OrderUpdate{Items: []api.OrderItemRequest{item(p, 1)}})
	mustCode(t, err, "PROMO_CODE_INVALID")
	f.stock(p.ID, 3)
	f.uses(promo.ID, 1)
	if _, err = f.svc.CancelOrder(f.ctx, f.user, order.ID); err != nil {
		t.Fatal(err)
	}
	f.stock(p.ID, 5)
	f.uses(promo.ID, 0)
}

func TestOrderRateLimitRolesAndTransitions(t *testing.T) {
	f := newFixture(t)
	p := f.product("20", 8)
	f.svc.Service = NewService(f.db, f.svc.productService, time.Hour)
	_, err := f.svc.CreateOrder(f.ctx, f.seller, &api.OrderCreate{Items: []api.OrderItemRequest{item(p, 1)}})
	mustCode(t, err, "ACCESS_DENIED")
	order, err := f.svc.CreateOrder(f.ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(p, 1)}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.CreateOrder(f.ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(p, 1)}})
	mustCode(t, err, "ORDER_LIMIT_EXCEEDED")
	f.svc.Service = NewService(f.db, f.svc.productService, 0)
	_, err = f.svc.CreateOrder(f.ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(p, 1)}})
	mustCode(t, err, "ORDER_HAS_ACTIVE")
	f.svc.Service = NewService(f.db, f.svc.productService, time.Hour)
	if _, err = f.svc.UpdateOrder(f.ctx, f.user, order.ID, &api.OrderUpdate{Items: []api.OrderItemRequest{item(p, 2)}}); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.UpdateOrder(f.ctx, f.user, order.ID, &api.OrderUpdate{Items: []api.OrderItemRequest{item(p, 3)}})
	mustCode(t, err, "ORDER_LIMIT_EXCEEDED")
	f.stock(p.ID, 6)
	_, err = f.svc.ChangeOrderStatus(f.ctx, f.user, order.ID, api.OrderStatusUpdateStatusPAYMENTPENDING)
	mustCode(t, err, "ACCESS_DENIED")
	_, err = f.svc.ChangeOrderStatus(f.ctx, f.admin, order.ID, api.OrderStatusUpdateStatusPAID)
	mustCode(t, err, "INVALID_STATE_TRANSITION")
	if _, err = f.svc.ChangeOrderStatus(f.ctx, f.admin, order.ID, api.OrderStatusUpdateStatusPAYMENTPENDING); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.UpdateOrder(f.ctx, f.user, order.ID, &api.OrderUpdate{Items: []api.OrderItemRequest{item(p, 3)}})
	mustCode(t, err, "INVALID_STATE_TRANSITION")
	for _, state := range []api.OrderStatusUpdateStatus{api.OrderStatusUpdateStatusPAID, api.OrderStatusUpdateStatusSHIPPED, api.OrderStatusUpdateStatusCOMPLETED} {
		if _, err = f.svc.ChangeOrderStatus(f.ctx, f.admin, order.ID, state); err != nil {
			t.Fatal(err)
		}
	}
	_, err = f.svc.CancelOrder(f.ctx, f.admin, order.ID)
	mustCode(t, err, "INVALID_STATE_TRANSITION")
	_, err = f.svc.GetOrder(f.ctx, f.seller, order.ID)
	mustCode(t, err, "ACCESS_DENIED")
	_, err = f.svc.GetOrder(f.ctx, f.user, uuid.New())
	mustCode(t, err, "ORDER_NOT_FOUND")
}

func TestConcurrentReservations(t *testing.T) {
	for _, kind := range []string{"stock", "promo", "active"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			p := f.product("10", 1)
			if kind != "stock" {
				r := productUpdate(p)
				r.Stock = 5
				var err error
				p, err = f.svc.UpdateProduct(f.ctx, f.seller, p.ID, r)
				if err != nil {
					t.Fatal(err)
				}
			}
			req := &api.OrderCreate{Items: []api.OrderItemRequest{item(p, 1)}}
			var promo *api.PromoCodeResponse
			if kind == "promo" {
				promo = f.promo(api.DiscountTypeFIXEDAMOUNT, "1", "0", 1)
				req.PromoCode = api.NewOptString(promo.Code)
			}
			actors := []identity.Principal{f.user, f.other}
			if kind == "active" {
				actors[1] = f.user
			}
			errs := make([]error, 2)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i, p := range actors {
				wg.Add(1)
				go func(i int, p identity.Principal) {
					defer wg.Done()
					<-start
					_, errs[i] = f.svc.CreateOrder(f.ctx, p, req)
				}(i, p)
			}
			close(start)
			wg.Wait()
			successes := 0
			code := map[string]string{"stock": "INSUFFICIENT_STOCK", "promo": "PROMO_CODE_INVALID", "active": "ORDER_HAS_ACTIVE"}[kind]
			for _, err := range errs {
				if err == nil {
					successes++
				} else {
					mustCode(t, err, code)
				}
			}
			if successes != 1 {
				t.Fatalf("successes=%d errors=%v", successes, errs)
			}
			stock := int32(0)
			if kind != "stock" {
				stock = 4
			}
			f.stock(p.ID, stock)
			if promo != nil {
				f.uses(promo.ID, 1)
			}
		})
	}
}

func TestPromoUniquenessAndAuthorization(t *testing.T) {
	f := newFixture(t)
	p := f.promo(api.DiscountTypeFIXEDAMOUNT, "5", "0", 2)
	req := &api.PromoCodeCreate{Code: p.Code, DiscountType: p.DiscountType, DiscountValue: p.DiscountValue, MinOrderAmount: p.MinOrderAmount, MaxUses: p.MaxUses, ValidFrom: p.ValidFrom, ValidUntil: p.ValidUntil, Active: api.NewOptBool(true)}
	_, err := f.svc.CreatePromoCode(f.ctx, f.seller, req)
	mustCode(t, err, "PROMO_CODE_EXISTS")
	_, err = f.svc.CreatePromoCode(f.ctx, f.user, req)
	mustCode(t, err, "ACCESS_DENIED")
}

func TestServiceRoleDefense(t *testing.T) {
	s := &testServices{Service: NewService(nil, nil, 0), productService: catalog.NewService(nil, nil)}
	ctx := context.Background()
	p := identity.Principal{ID: uuid.New(), Role: "USER"}
	if _, err := s.CreateProduct(ctx, p, &api.ProductCreate{}); err == nil {
		t.Fatal("USER created a product")
	}
	for _, role := range []string{"SELLER", "unknown", ""} {
		t.Run(fmt.Sprint(role), func(t *testing.T) {
			p.Role = role
			_, err := s.CreateOrder(ctx, p, &api.OrderCreate{})
			mustCode(t, err, "ACCESS_DENIED")
			_, err = s.GetOrder(ctx, p, uuid.New())
			mustCode(t, err, "ACCESS_DENIED")
			_, err = s.UpdateOrder(ctx, p, uuid.New(), &api.OrderUpdate{})
			mustCode(t, err, "ACCESS_DENIED")
			_, err = s.CancelOrder(ctx, p, uuid.New())
			mustCode(t, err, "ACCESS_DENIED")
		})
	}
}

func TestConcurrentReplacementLocksWholeProductUnion(t *testing.T) {
	f := newFixture(t)
	a := f.product("10", 5)
	b := f.product("20", 5)
	first, err := f.svc.CreateOrder(f.ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(a, 2)}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.svc.CreateOrder(f.ctx, f.other, &api.OrderCreate{Items: []api.OrderItemRequest{item(b, 2)}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, errs[0] = f.svc.UpdateOrder(ctx, f.user, first.ID, &api.OrderUpdate{Items: []api.OrderItemRequest{item(b, 2)}})
	}()
	go func() {
		defer wg.Done()
		<-start
		_, errs[1] = f.svc.UpdateOrder(ctx, f.other, second.ID, &api.OrderUpdate{Items: []api.OrderItemRequest{item(a, 2)}})
	}()
	close(start)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	f.stock(a.ID, 3)
	f.stock(b.ID, 3)
	first, err = f.svc.GetOrder(f.ctx, f.user, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err = f.svc.GetOrder(f.ctx, f.other, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Items[0].ProductID != b.ID || second.Items[0].ProductID != a.ID {
		t.Fatal("concurrent replacements did not persist")
	}
}

func TestConcurrentCancellationReturnsReservationOnce(t *testing.T) {
	f := newFixture(t)
	p := f.product("10", 2)
	promo := f.promo(api.DiscountTypeFIXEDAMOUNT, "5", "0", 1)
	order, err := f.svc.CreateOrder(f.ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(p, 2)}, PromoCode: api.NewOptString(promo.Code)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, actor := range []identity.Principal{f.user, f.admin} {
		wg.Add(1)
		go func(i int, actor identity.Principal) {
			defer wg.Done()
			<-start
			_, errs[i] = f.svc.CancelOrder(ctx, actor, order.ID)
		}(i, actor)
	}
	close(start)
	wg.Wait()
	success := 0
	for _, err := range errs {
		if err == nil {
			success++
		} else {
			mustCode(t, err, "INVALID_STATE_TRANSITION")
		}
	}
	if success != 1 {
		t.Fatalf("success=%d, errors=%v", success, errs)
	}
	f.stock(p.ID, 2)
	f.uses(promo.ID, 0)
}

func TestCancellationStockOverflowRollsBack(t *testing.T) {
	f := newFixture(t)
	p := f.product("10", 1)
	promo := f.promo(api.DiscountTypeFIXEDAMOUNT, "5", "0", 1)
	order, err := f.svc.CreateOrder(f.ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(p, 1)}, PromoCode: api.NewOptString(promo.Code)})
	if err != nil {
		t.Fatal(err)
	}
	r := productUpdate(p)
	r.Stock = 2147483647
	if _, err = f.svc.UpdateProduct(f.ctx, f.seller, p.ID, r); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.CancelOrder(f.ctx, f.user, order.ID)
	mustCode(t, err, "VALIDATION_ERROR")
	f.stock(p.ID, 2147483647)
	f.uses(promo.ID, 1)
	got, err := f.svc.GetOrder(f.ctx, f.user, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != api.OrderStatusCREATED {
		t.Fatal("failed cancellation changed state")
	}
}

func TestPromoWindowAndExpiryOnUpdate(t *testing.T) {
	f := newFixture(t)
	p := f.product("10", 5)
	promo := f.promo(api.DiscountTypeFIXEDAMOUNT, "20", "0", 1)
	if _, err := f.db.Exec(f.ctx, "UPDATE promo_codes SET valid_from=clock_timestamp()+interval '1 hour',valid_until=clock_timestamp()+interval '2 hours' WHERE id=$1", promo.ID); err != nil {
		t.Fatal(err)
	}
	_, err := f.svc.CreateOrder(f.ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(p, 1)}, PromoCode: api.NewOptString(promo.Code)})
	mustCode(t, err, "PROMO_CODE_INVALID")
	f.stock(p.ID, 5)
	f.uses(promo.ID, 0)
	if _, err = f.db.Exec(f.ctx, "UPDATE promo_codes SET valid_from=clock_timestamp()-interval '1 hour',valid_until=clock_timestamp()+interval '1 hour' WHERE id=$1", promo.ID); err != nil {
		t.Fatal(err)
	}
	order, err := f.svc.CreateOrder(f.ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(p, 1)}, PromoCode: api.NewOptString(promo.Code)})
	if err != nil {
		t.Fatal(err)
	}
	if !order.TotalAmount.IsZero() || !order.DiscountAmount.Equal(decimal.NewFromInt(10)) {
		t.Fatal("fixed amount was not capped")
	}
	if _, err = f.db.Exec(f.ctx, "UPDATE promo_codes SET valid_until=clock_timestamp()-interval '1 minute' WHERE id=$1", promo.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.UpdateOrder(f.ctx, f.user, order.ID, &api.OrderUpdate{Items: []api.OrderItemRequest{item(p, 2)}})
	mustCode(t, err, "PROMO_CODE_INVALID")
	f.stock(p.ID, 4)
	f.uses(promo.ID, 1)
}

func TestAdminSellerAssignmentAndPagination(t *testing.T) {
	f := newFixture(t)
	req := &api.ProductCreate{Name: "A", Price: decimal.NewFromInt(1), Stock: 1, Category: f.category, Status: api.ProductStatusACTIVE, SellerID: api.NewOptUUID(f.seller.ID)}
	a, err := f.svc.CreateProduct(f.ctx, f.admin, req)
	if err != nil {
		t.Fatal(err)
	}
	if a.SellerID != f.seller.ID {
		t.Fatal("admin seller assignment ignored")
	}
	req.SellerID = api.NewOptUUID(f.user.ID)
	_, err = f.svc.CreateProduct(f.ctx, f.admin, req)
	mustCode(t, err, "VALIDATION_ERROR")
	req.SellerID = api.NewOptUUID(uuid.New())
	_, err = f.svc.CreateProduct(f.ctx, f.admin, req)
	mustCode(t, err, "VALIDATION_ERROR")
	b := f.product("2", 1)
	first, err := f.svc.ListProducts(f.ctx, api.ListProductsParams{Category: api.NewOptString(f.category), Size: api.NewOptInt(1)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.svc.ListProducts(f.ctx, api.ListProductsParams{Category: api.NewOptString(f.category), Size: api.NewOptInt(1), Page: api.NewOptInt(1)})
	if err != nil {
		t.Fatal(err)
	}
	if first.TotalElements != 2 || second.TotalElements != 2 || len(first.Items) != 1 || len(second.Items) != 1 || first.Items[0].ID != a.ID || second.Items[0].ID != b.ID {
		t.Fatal("unstable pagination")
	}
}
