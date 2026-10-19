package orders

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"marketplace/internal/api"
	"marketplace/internal/catalog"
	"marketplace/internal/twopc"
)

type lossyCatalog struct {
	CatalogParticipant
	dropPrepare        bool
	dropCommit         bool
	refuseCommit       bool
	cancelAfterPrepare context.CancelFunc
}

func (c *lossyCatalog) PrepareReservation(ctx context.Context, id string, old []api.OrderItemResponse, next []api.OrderItemRequest) ([]api.OrderItemResponse, error) {
	items, err := c.CatalogParticipant.PrepareReservation(ctx, id, old, next)
	if err == nil && c.cancelAfterPrepare != nil {
		c.cancelAfterPrepare()
	}
	if err == nil && c.dropPrepare {
		return nil, errors.New("injected lost prepare reply")
	}
	return items, err
}
func (c *lossyCatalog) ResolveReservation(ctx context.Context, id string, commit bool) error {
	if commit && c.refuseCommit {
		return errors.New("injected unavailable catalog")
	}
	err := c.CatalogParticipant.ResolveReservation(ctx, id, commit)
	if err == nil && commit && c.dropCommit {
		return errors.New("injected lost commit reply")
	}
	return err
}
func assertNoPrepared(t *testing.T, f *fixture) {
	t.Helper()
	for _, db := range []interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	}{f.db, f.catalogDB} {
		var n int
		if err := db.QueryRow(f.ctx, "SELECT count(*) FROM pg_prepared_xacts WHERE database=current_database() AND gid LIKE 'marketplace:%'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%d forgotten prepared transactions", n)
		}
	}
}

func TestParticipantDuplicatePrepareCommitAndLostAcknowledgement(t *testing.T) {
	f := newFixture(t)
	p := f.product("10.01", 4)
	gid := uuid.NewString()
	req := []api.OrderItemRequest{item(p, 2)}
	first, err := f.svc.productService.PrepareReservation(f.ctx, gid, nil, req)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := f.svc.productService.PrepareReservation(f.ctx, gid, nil, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(duplicate) != 1 || first[0].ID != duplicate[0].ID || !duplicate[0].PriceAtOrder.Equal(first[0].PriceAtOrder) {
		t.Fatal("duplicate did not return durable snapshot")
	}
	if _, err = f.svc.productService.PrepareReservation(f.ctx, gid, nil, []api.OrderItemRequest{item(p, 1)}); err == nil {
		t.Fatal("conflicting payload accepted")
	}
	f.stock(p.ID, 4)
	if err = f.svc.productService.ResolveReservation(f.ctx, gid, true); err != nil {
		t.Fatal(err)
	}
	restarted := catalog.NewService(f.catalogDB, lookupUsers{f.usersDB})
	if err = restarted.ResolveReservation(f.ctx, gid, true); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.PrepareReservation(f.ctx, gid, nil, req); err != nil {
		t.Fatal(err)
	}
	if err = restarted.ResolveReservation(f.ctx, gid, false); err == nil {
		t.Fatal("committed transaction aborted")
	}
	f.stock(p.ID, 2)
	assertNoPrepared(t, f)
}
func TestParticipantAbortFencesLatePrepareAndMissingPreparedIsNotCommit(t *testing.T) {
	f := newFixture(t)
	p := f.product("10", 4)
	gid := uuid.NewString()
	if err := f.svc.productService.ResolveReservation(f.ctx, gid, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.productService.PrepareReservation(f.ctx, gid, nil, []api.OrderItemRequest{item(p, 2)}); err == nil {
		t.Fatal("late prepare bypassed abort tombstone")
	}
	if err := f.svc.productService.ResolveReservation(f.ctx, gid, false); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.productService.ResolveReservation(f.ctx, uuid.NewString(), true); err == nil {
		t.Fatal("missing prepare was treated as committed")
	}
	f.stock(p.ID, 4)
	assertNoPrepared(t, f)
}
func TestLostPrepareReplyAbortsBothParticipants(t *testing.T) {
	f := newFixture(t)
	p := f.product("10", 4)
	s := NewService(f.db, &lossyCatalog{CatalogParticipant: f.svc.productService, dropPrepare: true}, 0)
	if _, err := s.CreateOrder(f.ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(p, 2)}}); err == nil {
		t.Fatal("lost prepare reply succeeded")
	}
	if err := f.svc.Recover(f.ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.db.QueryRow(f.ctx, "SELECT count(*) FROM orders WHERE user_id=$1", f.user.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("uncommitted order visible")
	}
	f.stock(p.ID, 4)
	assertNoPrepared(t, f)
}
func TestDurableCommitRecoversAcrossLostReplies(t *testing.T) {
	for _, drop := range []bool{false, true} {
		t.Run(map[bool]string{false: "before catalog commit", true: "after catalog commit"}[drop], func(t *testing.T) {
			f := newFixture(t)
			p := f.product("10", 4)
			transport := &lossyCatalog{CatalogParticipant: f.svc.productService, dropCommit: drop, refuseCommit: !drop}
			s := NewService(f.db, transport, 0)
			_, err := s.CreateOrder(f.ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(p, 2)}})
			mustCode(t, err, "TRANSACTION_PENDING")
			var gid string
			if err = f.db.QueryRow(f.ctx, "SELECT id FROM transaction_journal WHERE decision='COMMIT' AND NOT completed ORDER BY created_at DESC LIMIT 1").Scan(&gid); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Exec(f.ctx, "UPDATE transaction_journal SET decision='ABORT' WHERE id=$1", gid); err == nil {
				t.Fatal("durable decision was mutable")
			}
			restarted := NewService(f.db, f.svc.productService, 0)
			if err = restarted.Recover(f.ctx); err != nil {
				t.Fatal(err)
			}
			if err = restarted.Recover(f.ctx); err != nil {
				t.Fatal(err)
			}
			var n int
			if err = f.db.QueryRow(f.ctx, "SELECT count(*) FROM orders WHERE user_id=$1", f.user.ID).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Fatalf("orders=%d", n)
			}
			f.stock(p.ID, 2)
			assertNoPrepared(t, f)
		})
	}
}
func TestRequestCancellationStillAbortsPreparedCatalog(t *testing.T) {
	f := newFixture(t)
	p := f.product("10", 4)
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	s := NewService(f.db, &lossyCatalog{CatalogParticipant: f.svc.productService, cancelAfterPrepare: cancel}, 0)
	if _, err := s.CreateOrder(ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(p, 2)}}); err == nil {
		t.Fatal("canceled request succeeded")
	}
	if err := f.svc.Recover(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.stock(p.ID, 4)
	assertNoPrepared(t, f)
}
func TestRecoveryFencesActiveCoordinatorAndAbortsAbandonedPrepare(t *testing.T) {
	f := newFixture(t)
	p := f.product("10", 4)
	gid := uuid.NewString()
	oid := uuid.New()
	c, err := f.db.Acquire(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	held, err := twopc.Lock(f.ctx, c.Conn(), "orders", gid, false)
	if err != nil || !held {
		t.Fatal(err)
	}
	defer twopc.Release(c, "orders", gid)
	if _, err = c.Exec(f.ctx, "INSERT INTO transaction_journal(id,order_id) VALUES($1,$2)", gid, oid); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.productService.PrepareReservation(f.ctx, gid, nil, []api.OrderItemRequest{item(p, 2)}); err != nil {
		t.Fatal(err)
	}
	if err = f.svc.Recover(f.ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = c.QueryRow(f.ctx, "SELECT decision FROM transaction_journal WHERE id=$1", gid).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "PREPARING" {
		t.Fatal("recovery decided an active transaction")
	}
	if err = c.Conn().Close(f.ctx); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	if err = f.svc.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow(ctx, "SELECT decision FROM transaction_journal WHERE id=$1", gid).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "ABORT" {
		t.Fatal("abandoned prepare was not aborted")
	}
	f.stock(p.ID, 4)
	assertNoPrepared(t, f)
}

func TestRecoveryAfterCoordinatorBackendDies(t *testing.T) {
	cases := []struct {
		name                                           string
		localPrepared, commit, catalogDone, ordersDone bool
	}{
		{name: "before local prepare"},
		{name: "both prepared before decision", localPrepared: true},
		{name: "after durable commit", localPrepared: true, commit: true},
		{name: "between commits", localPrepared: true, commit: true, catalogDone: true},
		{name: "after commits before journal completion", localPrepared: true, commit: true, catalogDone: true, ordersDone: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			p := f.product("10", 4)
			promo := f.promo(api.DiscountTypeFIXEDAMOUNT, "1", "0", 2)
			gid := uuid.NewString()
			oid := uuid.New()
			c, err := f.db.Acquire(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer twopc.Release(c, "orders", gid)
			if _, err = twopc.Lock(f.ctx, c.Conn(), "orders", gid, false); err != nil {
				t.Fatal(err)
			}
			if _, err = c.Exec(f.ctx, "INSERT INTO transaction_journal(id,order_id) VALUES($1,$2)", gid, oid); err != nil {
				t.Fatal(err)
			}
			tx, err := twopc.Begin(f.ctx, c.Conn())
			if err != nil {
				t.Fatal(err)
			}
			defer twopc.Rollback(tx)
			if err = lockActor(f.ctx, tx, f.user.ID); err != nil {
				t.Fatal(err)
			}
			items, err := f.svc.productService.PrepareReservation(f.ctx, gid, nil, []api.OrderItemRequest{item(p, 2)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(f.ctx, "INSERT INTO orders(id,user_id,status,total_amount,promo_code_id) VALUES($1,$2,'CREATED',19,$3)", oid, f.user.ID, promo.ID); err != nil {
				t.Fatal(err)
			}
			if err = insertItems(f.ctx, tx, oid, items); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(f.ctx, "UPDATE promo_codes SET current_uses=current_uses+1 WHERE id=$1", promo.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(f.ctx, "INSERT INTO transaction_receipts(id) VALUES($1)", gid); err != nil {
				t.Fatal(err)
			}
			if tc.localPrepared {
				if err = twopc.Prepare(f.ctx, tx, "orders", gid); err != nil {
					t.Fatal(err)
				}
			}
			if tc.commit {
				if _, err = c.Exec(f.ctx, "UPDATE transaction_journal SET decision='COMMIT' WHERE id=$1", gid); err != nil {
					t.Fatal(err)
				}
			}
			if tc.catalogDone {
				if err = f.svc.productService.ResolveReservation(f.ctx, gid, true); err != nil {
					t.Fatal(err)
				}
			}
			if tc.ordersDone {
				if err = twopc.Resolve(f.ctx, c.Conn(), "orders", gid, true); err != nil {
					t.Fatal(err)
				}
			}
			var killed bool
			if err = f.db.QueryRow(f.ctx, "SELECT pg_terminate_backend($1)", c.Conn().PgConn().PID()).Scan(&killed); err != nil || !killed {
				t.Fatalf("kill=%v err=%v", killed, err)
			}
			_ = c.Conn().Close(f.ctx)
			restarted := NewService(f.db, f.svc.productService, 0)
			failures := make(chan error, 2)
			for i := 0; i < 2; i++ {
				go func() { failures <- restarted.Recover(f.ctx) }()
			}
			for i := 0; i < 2; i++ {
				if err = <-failures; err != nil {
					t.Fatal(err)
				}
			}
			var exists bool
			if err = f.db.QueryRow(f.ctx, "SELECT EXISTS(SELECT 1 FROM orders WHERE id=$1)", oid).Scan(&exists); err != nil {
				t.Fatal(err)
			}
			if exists != tc.commit {
				t.Fatalf("order exists=%v commit=%v", exists, tc.commit)
			}
			var decision string
			var complete bool
			if err = f.db.QueryRow(f.ctx, "SELECT decision,completed FROM transaction_journal WHERE id=$1", gid).Scan(&decision, &complete); err != nil {
				t.Fatal(err)
			}
			want := "ABORT"
			stock := int32(4)
			uses := int32(0)
			if tc.commit {
				want = "COMMIT"
				stock = 2
				uses = 1
			}
			if decision != want || !complete {
				t.Fatalf("decision=%s complete=%v", decision, complete)
			}
			f.stock(p.ID, stock)
			f.uses(promo.ID, uses)
			assertNoPrepared(t, f)
		})
	}
}

func TestAbortSerializesWithConcurrentPrepare(t *testing.T) {
	f := newFixture(t)
	p := f.product("10", 10)
	for i := 0; i < 5; i++ {
		gid := uuid.NewString()
		start := make(chan struct{})
		done := make(chan error, 2)
		go func() {
			<-start
			_, err := f.svc.productService.PrepareReservation(f.ctx, gid, nil, []api.OrderItemRequest{item(p, 2)})
			done <- err
		}()
		go func() { <-start; done <- f.svc.productService.ResolveReservation(f.ctx, gid, false) }()
		close(start)
		<-done
		<-done
		if err := f.svc.productService.ResolveReservation(f.ctx, gid, false); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.productService.PrepareReservation(f.ctx, gid, nil, []api.OrderItemRequest{item(p, 2)}); err == nil {
			t.Fatal("late prepare recreated aborted reservation")
		}
	}
	f.stock(p.ID, 10)
	assertNoPrepared(t, f)
}

func TestCatalogCommitRecoversSnapshotWhenPostPrepareMetadataWriteWasLost(t *testing.T) {
	f := newFixture(t)
	p := f.product("10", 4)
	gid := uuid.NewString()
	req := []api.OrderItemRequest{item(p, 2)}
	first, err := f.svc.productService.PrepareReservation(f.ctx, gid, nil, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.catalogDB.Exec(f.ctx, "UPDATE reservation_journal SET result=NULL,state='PREPARING' WHERE id=$1", gid); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.productService.PrepareReservation(f.ctx, gid, nil, req); err == nil {
		t.Fatal("lost snapshot was fabricated")
	}
	if err = f.svc.productService.ResolveReservation(f.ctx, gid, true); err != nil {
		t.Fatal(err)
	}
	duplicate, err := f.svc.productService.PrepareReservation(f.ctx, gid, nil, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(duplicate) != 1 || first[0].ID != duplicate[0].ID {
		t.Fatal("commit receipt lost the durable snapshot")
	}
	f.stock(p.ID, 2)
	assertNoPrepared(t, f)
}

func TestRecoveryDoesNotStarveBehindUnresolvableOlderDecisions(t *testing.T) {
	f := newFixture(t)
	poisoned := make([]uuid.UUID, 100)
	for i := range poisoned {
		poisoned[i] = uuid.New()
		if _, err := f.db.Exec(f.ctx, "INSERT INTO transaction_journal(id,order_id,decision) VALUES($1,$2,'COMMIT')", poisoned[i], uuid.New()); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if _, err := f.db.Exec(f.ctx, "DELETE FROM transaction_journal WHERE id=ANY($1)", poisoned); err != nil {
			t.Error(err)
		}
	})
	p := f.product("10", 4)
	gid := uuid.NewString()
	if _, err := f.db.Exec(f.ctx, "INSERT INTO transaction_journal(id,order_id) VALUES($1,$2)", gid, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.productService.PrepareReservation(f.ctx, gid, nil, []api.OrderItemRequest{item(p, 2)}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Recover(f.ctx); err == nil {
		t.Fatal("corrupt older decisions were silently ignored")
	}
	var done bool
	if err := f.db.QueryRow(f.ctx, "SELECT completed FROM transaction_journal WHERE id=$1", gid).Scan(&done); err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Fatal("recoverable transaction starved behind first 100 records")
	}
	f.stock(p.ID, 4)
}

func TestProtocolNeedsOnlyOneConnectionPerDatabase(t *testing.T) {
	f := newFixture(t)
	p := f.product("10", 4)
	catalogConfig := f.catalogDB.Config()
	catalogConfig.MaxConns = 1
	catalogConfig.MinConns = 0
	catalogDB, err := pgxpool.NewWithConfig(f.ctx, catalogConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer catalogDB.Close()
	orderConfig := f.db.Config()
	orderConfig.MaxConns = 1
	orderConfig.MinConns = 0
	ordersDB, err := pgxpool.NewWithConfig(f.ctx, orderConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer ordersDB.Close()
	participant := catalog.NewService(catalogDB, lookupUsers{f.usersDB})
	coordinator := NewService(ordersDB, participant, 0)
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	order, err := coordinator.CreateOrder(ctx, f.user, &api.OrderCreate{Items: []api.OrderItemRequest{item(p, 2)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.CancelOrder(ctx, f.user, order.ID); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	f.stock(p.ID, 4)
	assertNoPrepared(t, f)
}
