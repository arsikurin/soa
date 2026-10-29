package booking

import (
	"context"
	"errors"
	"testing"
	"time"

	"flightbooking/internal/rpc/pb"
	"github.com/google/uuid"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestReserveFailureCreatesNoBooking(t *testing.T) {
	for _, code := range []codes.Code{codes.ResourceExhausted, codes.Unavailable, codes.DeadlineExceeded} {
		t.Run(code.String(), func(t *testing.T) {
			pool := bookingDatabase(t)
			flights := newFlightBoundary()
			flights.reserveErr = status.Error(code, "reservation failed")
			service := New(pool, flights, testLogger())
			id := uuid.New()
			_, err := service.Create(context.Background(), validInput(), id)
			if status.Code(err) != code {
				t.Fatalf("wanted %v, got %v", code, err)
			}
			if _, err := service.Get(context.Background(), id); status.Code(err) != codes.NotFound {
				t.Fatalf("failed create became visible: %v", err)
			}
			if flights.activeSeats(id) != 0 {
				t.Fatal("failed create retained seats")
			}
		})
	}
}

func TestLostReserveResponseRecoversAfterWorkerRestart(t *testing.T) {
	pool := bookingDatabase(t)
	flights := newFlightBoundary()
	flights.afterReserve = func(context.Context, *pb.ReserveSeatsRequest) error {
		return status.Error(codes.DeadlineExceeded, "response lost")
	}
	flights.releaseErr = status.Error(codes.Unavailable, "flight service stopped")
	service := New(pool, flights, testLogger())
	input, id := validInput(), uuid.New()
	_, err := service.Create(context.Background(), input, id)
	assertPending(t, err, id)
	if flights.activeSeats(id) != 2 {
		t.Fatal("fault fixture did not reach remote reservation")
	}
	list, err := service.List(context.Background(), input.UserID)
	if err != nil || len(list) != 0 {
		t.Fatalf("internal pending operation leaked into bookings: %+v, %v", list, err)
	}
	flights.mu.Lock()
	flights.releaseErr = nil
	flights.mu.Unlock()
	restarted := New(pool, flights, testLogger())
	if err := restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if flights.activeSeats(id) != 0 {
		t.Fatal("restart did not return reserved seats")
	}
	_, err = restarted.Create(context.Background(), input, id)
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("idempotent retry lost terminal result: %v", err)
	}
}

func TestRecoveryDoesNotRaceLiveCreate(t *testing.T) {
	pool := bookingDatabase(t)
	flights := newFlightBoundary()
	entered, proceed := make(chan struct{}), make(chan struct{})
	flights.beforeReserve = func(ctx context.Context, req *pb.ReserveSeatsRequest) error {
		close(entered)
		select {
		case <-proceed:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	service := New(pool, flights, testLogger())
	input, id := validInput(), uuid.New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	created := make(chan error, 1)
	go func() {
		_, err := service.Create(ctx, input, id)
		created <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("create did not reach reservation")
	}
	if err := New(pool, flights, testLogger()).Recover(ctx); err != nil {
		t.Fatal(err)
	}
	close(proceed)
	if err := <-created; err != nil {
		t.Fatalf("recovery interfered with create: %v", err)
	}
	if flights.activeSeats(id) != 2 {
		t.Fatal("recovery released a live request's reservation")
	}
}

func TestConcurrentIdempotencyKeyCreatesOneBooking(t *testing.T) {
	pool := bookingDatabase(t)
	flights := newFlightBoundary()
	service := New(pool, flights, testLogger())
	input, id := validInput(), uuid.New()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	errors := make(chan error, 12)
	for range 12 {
		go func() {
			got, err := service.Create(ctx, input, id)
			if err == nil && (got.ID != id || got.TotalPriceMinor != 250000) {
				err = status.Error(codes.Internal, "different result for the same key")
			}
			errors <- err
		}()
	}
	for range 12 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	list, err := service.List(ctx, input.UserID)
	if err != nil || len(list) != 1 || flights.activeSeats(id) != 2 {
		t.Fatalf("duplicate booking: %+v, %v", list, err)
	}
	input.SeatCount = 3
	if _, err := service.Create(ctx, input, id); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("same key accepted a different request: %v", err)
	}
}

func TestDatabaseFailureCompensatesReservation(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		name, timing := "insert failure", ""
		if deferred {
			name, timing = "commit failure", "DEFERRABLE INITIALLY DEFERRED"
		}
		t.Run(name, func(t *testing.T) {
			pool := bookingDatabase(t)
			_, err := pool.Exec(context.Background(), `CREATE FUNCTION reject_booking() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected write failure'; END; $$;
			CREATE CONSTRAINT TRIGGER reject_booking AFTER INSERT ON bookings `+timing+` FOR EACH ROW EXECUTE FUNCTION reject_booking()`)
			if err != nil {
				t.Fatal(err)
			}
			flights := newFlightBoundary()
			service := New(pool, flights, testLogger())
			id := uuid.New()
			if _, err := service.Create(context.Background(), validInput(), id); status.Code(err) != codes.Unavailable {
				t.Fatalf("expected storage error: %v", err)
			}
			if flights.activeSeats(id) != 0 {
				t.Fatal("database failure left seats reserved")
			}
			if _, err := service.Get(context.Background(), id); status.Code(err) != codes.NotFound {
				t.Fatalf("failed transaction left booking visible: %v", err)
			}
		})
	}
}

func TestCancelRetriesAfterRestart(t *testing.T) {
	pool := bookingDatabase(t)
	flights := newFlightBoundary()
	service := New(pool, flights, testLogger())
	id := uuid.New()
	if _, err := service.Create(context.Background(), validInput(), id); err != nil {
		t.Fatal(err)
	}
	flights.releaseErr = status.Error(codes.Unavailable, "flight service stopped")
	_, err := service.Cancel(context.Background(), id)
	assertPending(t, err, id)
	stored, err := service.Get(context.Background(), id)
	if err != nil || stored.Status != "CONFIRMED" {
		t.Fatalf("failed cancellation changed public state: %+v, %v", stored, err)
	}
	flights.releaseErr = nil
	restarted := New(pool, flights, testLogger())
	if err := restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		cancelled, err := restarted.Cancel(context.Background(), id)
		if err != nil || cancelled.Status != "CANCELLED" || cancelled.TotalPriceMinor != 250000 {
			t.Fatalf("cancel result: %+v, %v", cancelled, err)
		}
	}
	if flights.activeSeats(id) != 0 {
		t.Fatal("cancellation did not return seats")
	}
}

func TestGetFlightFailureCreatesNoBooking(t *testing.T) {
	pool := bookingDatabase(t)
	flights := newFlightBoundary()
	flights.getErr = status.Error(codes.NotFound, "flight gone")
	service := New(pool, flights, testLogger())
	id := uuid.New()
	_, err := service.Create(context.Background(), validInput(), id)
	if status.Code(err) != codes.NotFound {
		t.Fatal(err)
	}
	if _, err := service.Get(context.Background(), id); status.Code(err) != codes.NotFound {
		t.Fatalf("GetFlight failure created booking: %v", err)
	}
	if flights.activeSeats(id) != 0 {
		t.Fatal("GetFlight failure reserved seats")
	}
}

func TestGetFlightOutageDoesNotLeaveUnnecessaryCompensation(t *testing.T) {
	pool := bookingDatabase(t)
	flights := newFlightBoundary()
	flights.getErr = status.Error(codes.Unavailable, "flight service stopped")
	flights.releaseErr = status.Error(codes.Unavailable, "flight service stopped")
	service := New(pool, flights, testLogger())
	id := uuid.New()
	_, err := service.Create(context.Background(), validInput(), id)
	if status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
	var state string
	if err := pool.QueryRow(context.Background(), "SELECT state FROM booking_operations WHERE booking_id=$1", id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "FAILED" {
		t.Fatalf("read-only RPC failure unnecessarily retained operation: %s", state)
	}
}

func TestRecoveryDoesNotStarveBehindFailingOperations(t *testing.T) {
	pool := bookingDatabase(t)
	flights := newFlightBoundary()
	ids := make([]string, 101)
	for i := range ids {
		ids[i] = uuid.NewString()
	}
	lastID := ids[100]
	flights.beforeRelease = func(ctx context.Context, req *pb.ReleaseReservationRequest) error {
		if req.BookingId != lastID {
			return status.Error(codes.Unavailable, "reservation temporarily unavailable")
		}
		return nil
	}
	_, err := pool.Exec(context.Background(), `INSERT INTO booking_operations (booking_id, user_id, flight_id, passenger_name, passenger_email, seat_count, kind, state, updated_at)
		SELECT id, $2, $3, 'Ivan Petrov', 'ivan@example.com', 2, 'CREATE', 'PENDING', now() + n * interval '1 millisecond' FROM unnest($1::uuid[]) WITH ORDINALITY AS x(id, n)`, ids, uuid.New(), validInput().FlightID)
	if err != nil {
		t.Fatal(err)
	}
	service := New(pool, flights, testLogger())
	for range 2 {
		if err := service.Recover(context.Background()); err == nil {
			t.Fatal("expected errors for unavailable reservations")
		}
	}
	var state string
	if err := pool.QueryRow(context.Background(), "SELECT state FROM booking_operations WHERE booking_id=$1", lastID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "FAILED" {
		t.Fatalf("recoverable operation starved behind failing operations: %s", state)
	}
}

func assertPending(t *testing.T, err error, id uuid.UUID) {
	t.Helper()
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("wanted pending Unavailable, got %v", err)
	}
	for _, detail := range status.Convert(err).Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && info.Reason == "OPERATION_PENDING" && info.Metadata["booking_id"] == id.String() {
			return
		}
	}
	t.Fatal(errors.New("missing pending operation detail"))
}
