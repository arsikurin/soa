package e2e

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestBookingRestartCompensatesReservationAfterLostCreate(t *testing.T) {
	h := newHarness(t)
	if h.initial.AvailableSeats < 1 {
		t.Fatal("fixture needs an available seat")
	}
	id := h.bookingID()
	h.restoreContainers("booking")
	gateID := uuid.New()
	gateKey := int64(binary.BigEndian.Uint64(gateID[:8]))
	name := "e2e_gate_" + strings.ReplaceAll(gateID.String(), "-", "")
	identifier := pgx.Identifier{name}.Sanitize()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	gate, err := h.bookingDB.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gateReleased := false
	unlock := func() error {
		if gateReleased {
			return nil
		}
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, err := gate.Exec(cleanupContext, "SELECT pg_advisory_unlock($1)", gateKey)
		if err != nil {
			_ = gate.Conn().Close(cleanupContext)
		}
		gate.Release()
		gateReleased = true
		return err
	}
	removeGate := func() error {
		if err := unlock(); err != nil {
			return err
		}
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, err := h.bookingDB.Exec(cleanupContext, "DROP TRIGGER IF EXISTS "+identifier+" ON bookings; DROP FUNCTION IF EXISTS "+identifier+"()")
		return err
	}
	t.Cleanup(func() {
		if err := removeGate(); err != nil {
			t.Errorf("remove scoped SQL gate: %v", err)
		}
	})
	if _, err := gate.Exec(ctx, "SELECT pg_advisory_lock($1)", gateKey); err != nil {
		t.Fatal(err)
	}
	function := fmt.Sprintf("CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id='%s'::uuid THEN PERFORM pg_advisory_xact_lock(%d::bigint); END IF; RETURN NEW; END $$", identifier, id, gateKey)
	if _, err := h.bookingDB.Exec(ctx, function); err != nil {
		t.Fatal(err)
	}
	if _, err := h.bookingDB.Exec(ctx, "CREATE TRIGGER "+identifier+" BEFORE INSERT ON bookings FOR EACH ROW EXECUTE FUNCTION "+identifier+"()"); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		response httpResult
		err      error
	}
	finished := make(chan outcome, 1)
	go func() {
		response, err := h.doHTTP("POST", "/bookings", createBody(uuid.NewString(), 1), id)
		finished <- outcome{response: response, err: err}
	}()
	eventually(t, 10*time.Second, "active reservation and booking insert blocked by the scoped gate", func() (bool, error) {
		checkContext, checkCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer checkCancel()
		var active, pending, blocked bool
		if err := h.flightDB.QueryRow(checkContext, "SELECT EXISTS (SELECT 1 FROM seat_reservations WHERE booking_id=$1 AND status='ACTIVE')", id).Scan(&active); err != nil {
			return false, err
		}
		if err := h.bookingDB.QueryRow(checkContext, "SELECT EXISTS (SELECT 1 FROM booking_operations WHERE booking_id=$1 AND state='PENDING')", id).Scan(&pending); err != nil {
			return false, err
		}
		if err := h.bookingDB.QueryRow(checkContext, "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND query LIKE 'INSERT INTO bookings%')").Scan(&blocked); err != nil {
			return false, err
		}
		return active && pending && blocked, nil
	})
	t.Logf("SELECT evidence before crash: booking_id=%s reservation=ACTIVE operation=PENDING booking insert waits on advisory gate", id)
	h.mustCompose("kill", "--signal", "SIGKILL", "booking")
	select {
	case result := <-finished:
		if result.err == nil && result.response.status == 201 {
			t.Fatalf("gated create unexpectedly completed: %s", result.response.body)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("original HTTP request did not finish after process death")
	}
	if err := removeGate(); err != nil {
		t.Fatal(err)
	}
	h.mustCompose("start", "booking")
	eventually(t, 30*time.Second, "restart recovery to release seats and finish the journal", func() (bool, error) {
		checkContext, checkCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer checkCancel()
		var operation, reservation string
		var bookings, available int
		if err := h.bookingDB.QueryRow(checkContext, "SELECT state FROM booking_operations WHERE booking_id=$1", id).Scan(&operation); err != nil {
			return false, err
		}
		if err := h.flightDB.QueryRow(checkContext, "SELECT status FROM seat_reservations WHERE booking_id=$1", id).Scan(&reservation); err != nil {
			return false, err
		}
		if err := h.bookingDB.QueryRow(checkContext, "SELECT count(*) FROM bookings WHERE id=$1", id).Scan(&bookings); err != nil {
			return false, err
		}
		if err := h.flightDB.QueryRow(checkContext, "SELECT available_seats FROM flights WHERE id=$1", seedFlightID).Scan(&available); err != nil {
			return false, err
		}
		return operation == "FAILED" && reservation == "RELEASED" && bookings == 0 && available == int(h.initial.AvailableSeats), nil
	})
	t.Logf("SELECT evidence after restart: operation=FAILED reservation=RELEASED bookings=0 available_seats=%d", h.initial.AvailableSeats)
}
