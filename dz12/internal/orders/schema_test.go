package orders

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestBusinessColumnsUseNativeEnums(t *testing.T) {
	for _, tc := range []struct {
		database, table, column string
	}{
		{"USERS", "users", "role"},
		{"CATALOG", "products", "status"},
		{"ORDERS", "orders", "status"},
		{"ORDERS", "promo_codes", "discount_type"},
		{"ORDERS", "user_operations", "operation_type"},
	} {
		t.Run(tc.table+"."+tc.column, func(t *testing.T) {
			db := testPool(t, "TEST_"+tc.database+"_DATABASE_URL")
			var kind string
			err := db.QueryRow(context.Background(), `SELECT t.typtype::text
				FROM pg_attribute a JOIN pg_type t ON t.oid=a.atttypid
				WHERE a.attrelid=to_regclass('public.' || $1) AND a.attname=$2 AND NOT a.attisdropped`, tc.table, tc.column).Scan(&kind)
			if err != nil {
				t.Fatal(err)
			}
			if kind != "e" {
				t.Fatalf("%s.%s is type kind %q, want native enum", tc.table, tc.column, kind)
			}
		})
	}
}

func TestPromoActiveDatabaseDefault(t *testing.T) {
	db := testPool(t, "TEST_ORDERS_DATABASE_URL")
	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	code := "DEFAULT_" + strings.ToUpper(uuid.NewString()[:8])
	var active bool
	err = tx.QueryRow(context.Background(), `INSERT INTO promo_codes
		(code,discount_type,discount_value,min_order_amount,max_uses,valid_from,valid_until)
		VALUES ($1,'PERCENTAGE',10,0,1,now(),now()+interval '1 day') RETURNING active`, code).Scan(&active)
	if err != nil {
		t.Fatalf("insert without active: %v", err)
	}
	if !active {
		t.Fatal("omitted active must default to true")
	}
	if err = tx.QueryRow(context.Background(), "UPDATE promo_codes SET active=false WHERE code=$1 RETURNING active", code).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active {
		t.Fatal("explicit false must remain false")
	}
}
