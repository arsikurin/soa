package booking

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLostCommitResponseDoesNotCompensateCommittedBooking(t *testing.T) {
	pool := bookingDatabase(t)
	armed, dropped := new(atomic.Bool), new(atomic.Bool)
	armed.Store(true)
	faultPool := dropCommitResponse(t, pool, armed, dropped)
	flights := newFlightBoundary()
	service := New(faultPool, flights, testLogger())
	id := uuid.New()
	booking, err := service.Create(context.Background(), validInput(), id)
	if !dropped.Load() {
		t.Fatal("COMMIT response fault was not injected")
	}
	if err != nil || booking.Status != "CONFIRMED" {
		t.Fatalf("committed create was not recovered: %+v, %v", booking, err)
	}
	if flights.activeSeats(id) != 2 {
		t.Fatal("lost commit response caused compensation of a confirmed booking")
	}
	if err := New(pool, flights, testLogger()).Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if flights.activeSeats(id) != 2 {
		t.Fatal("worker compensated a committed booking")
	}
}

func TestLostCancellationCommitResponseIsRecoverable(t *testing.T) {
	pool := bookingDatabase(t)
	armed, dropped := new(atomic.Bool), new(atomic.Bool)
	faultPool := dropCommitResponse(t, pool, armed, dropped)
	flights := newFlightBoundary()
	service := New(faultPool, flights, testLogger())
	id := uuid.New()
	if _, err := service.Create(context.Background(), validInput(), id); err != nil {
		t.Fatal(err)
	}
	armed.Store(true)
	_, err := service.Cancel(context.Background(), id)
	assertPending(t, err, id)
	if !dropped.Load() {
		t.Fatal("COMMIT response fault was not injected")
	}
	restarted := New(pool, flights, testLogger())
	if err := restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	booking, err := restarted.Cancel(context.Background(), id)
	if err != nil || booking.Status != "CANCELLED" || flights.activeSeats(id) != 0 {
		t.Fatalf("cancellation lost after ambiguous commit: %+v, %v", booking, err)
	}
}

func dropCommitResponse(t *testing.T, base *pgxpool.Pool, armed, dropped *atomic.Bool) *pgxpool.Pool {
	t.Helper()
	config := base.Config()
	dial := config.ConnConfig.DialFunc
	config.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &commitResponseConn{Conn: conn, armed: armed, dropped: dropped}, nil
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type commitResponseConn struct {
	net.Conn
	armed       *atomic.Bool
	dropped     *atomic.Bool
	awaitCommit atomic.Bool
}

func (c *commitResponseConn) Write(data []byte) (int, error) {
	if bytes.Contains(data, []byte("commit\x00")) {
		c.awaitCommit.Store(true)
	}
	return c.Conn.Write(data)
}

func (c *commitResponseConn) Read(data []byte) (int, error) {
	n, err := c.Conn.Read(data)
	if c.awaitCommit.Load() && bytes.Contains(data[:n], []byte("COMMIT\x00")) && c.armed.CompareAndSwap(true, false) {
		c.dropped.Store(true)
		_ = c.Conn.Close()
		return 0, io.EOF
	}
	return n, err
}
