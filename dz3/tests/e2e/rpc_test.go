package e2e

import (
	"context"
	"testing"
	"time"

	"flightbooking/internal/rpc/pb"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestRPCAuthenticationProtectsEveryMethod(t *testing.T) {
	h := newHarness(t)
	methods := []struct {
		name string
		call func(context.Context) error
	}{
		{"SearchFlights", func(ctx context.Context) error {
			_, err := h.rpc.SearchFlights(ctx, &pb.SearchFlightsRequest{})
			return err
		}},
		{"GetFlight", func(ctx context.Context) error { _, err := h.rpc.GetFlight(ctx, &pb.GetFlightRequest{}); return err }},
		{"ReserveSeats", func(ctx context.Context) error {
			_, err := h.rpc.ReserveSeats(ctx, &pb.ReserveSeatsRequest{})
			return err
		}},
		{"ReleaseReservation", func(ctx context.Context) error {
			_, err := h.rpc.ReleaseReservation(ctx, &pb.ReleaseReservationRequest{})
			return err
		}},
		{"UpdateFlight", func(ctx context.Context) error {
			_, err := h.rpc.UpdateFlight(ctx, &pb.UpdateFlightRequest{})
			return err
		}},
	}
	for _, method := range methods {
		for _, key := range []string{"", "invalid-e2e-key"} {
			t.Run(method.name+"/"+key, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if key != "" {
					ctx = metadata.AppendToOutgoingContext(ctx, "x-api-key", key)
				}
				if err := method.call(ctx); status.Code(err) != codes.Unauthenticated {
					t.Fatalf("unauthorized %s returned %v", method.name, err)
				}
			})
		}
	}
	ctx, cancel := h.rpcContext()
	defer cancel()
	if _, err := h.rpc.GetFlight(ctx, &pb.GetFlightRequest{Id: "bad-id"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid ID returned %v", err)
	}
	if _, err := h.rpc.GetFlight(ctx, &pb.GetFlightRequest{Id: uuid.NewString()}); status.Code(err) != codes.NotFound {
		t.Fatalf("missing flight returned %v", err)
	}
	if _, err := h.rpc.ReserveSeats(ctx, &pb.ReserveSeatsRequest{FlightId: seedFlightID, BookingId: h.bookingID(), SeatCount: h.initial.AvailableSeats + 1}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("seat shortage returned %v", err)
	}
}

func TestRPCReservationIdempotencyAndReleaseFence(t *testing.T) {
	h := newHarness(t)
	if h.initial.AvailableSeats < 2 {
		t.Fatal("fixture needs two available seats")
	}
	id := h.bookingID()
	first := h.reserve(id, 2)
	repeat := h.reserve(id, 2)
	if first.Id != repeat.Id || first.BookingId != repeat.BookingId || first.SeatCount != repeat.SeatCount {
		t.Fatalf("duplicate reservation: first=%v repeated=%v", first, repeat)
	}
	ctx, cancel := h.rpcContext()
	defer cancel()
	if _, err := h.rpc.ReserveSeats(ctx, &pb.ReserveSeatsRequest{FlightId: seedFlightID, BookingId: id, SeatCount: 1}); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("conflicting reservation returned %v", err)
	}
	var count, seats int
	if err := h.flightDB.QueryRow(ctx, "SELECT count(*), sum(seat_count) FROM seat_reservations WHERE booking_id=$1", id).Scan(&count, &seats); err != nil || count != 1 || seats != 2 {
		t.Fatalf("idempotency SQL evidence: count=%d seats=%d error=%v", count, seats, err)
	}
	if got := h.getFlight().AvailableSeats; got != h.initial.AvailableSeats-2 {
		t.Fatalf("repeated reserve changed seats twice: %d", got)
	}
	if !h.release(id).Released || h.release(id).Released {
		t.Fatal("release was not idempotent")
	}
	if _, err := h.rpc.ReserveSeats(ctx, &pb.ReserveSeatsRequest{FlightId: seedFlightID, BookingId: id, SeatCount: 2}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("late reserve after release returned %v", err)
	}
	fenced := h.bookingID()
	if h.release(fenced).Released {
		t.Fatal("release of absent reservation reported returned seats")
	}
	if _, err := h.rpc.ReserveSeats(ctx, &pb.ReserveSeatsRequest{FlightId: seedFlightID, BookingId: fenced, SeatCount: 1}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("late reserve bypassed compensation fence: %v", err)
	}
	if got := h.getFlight().AvailableSeats; got != h.initial.AvailableSeats {
		t.Fatalf("release/fence changed seats: %d", got)
	}
	t.Logf("SELECT evidence: repeated reserve stored %d row with %d seats; releases restored %d available seats", count, seats, h.initial.AvailableSeats)
}

func TestConcurrentLastSeatHasExactlyOneWinner(t *testing.T) {
	h := newHarness(t)
	if h.initial.AvailableSeats < 1 {
		t.Fatal("fixture has no available seat")
	}
	holder := h.bookingID()
	if h.initial.AvailableSeats > 1 {
		h.reserve(holder, h.initial.AvailableSeats-1)
	}
	ids := []string{h.bookingID(), h.bookingID()}
	type outcome struct {
		id  string
		err error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for _, id := range ids {
		go func() {
			<-start
			ctx, cancel := h.rpcContext()
			defer cancel()
			_, err := h.rpc.ReserveSeats(ctx, &pb.ReserveSeatsRequest{FlightId: seedFlightID, BookingId: id, SeatCount: 1})
			results <- outcome{id: id, err: err}
		}()
	}
	close(start)
	winners, shortages := 0, 0
	winner := ""
	for range 2 {
		result := <-results
		switch status.Code(result.err) {
		case codes.OK:
			winners++
			winner = result.id
		case codes.ResourceExhausted:
			shortages++
		default:
			t.Fatalf("unexpected reservation outcome: %v", result.err)
		}
	}
	if winners != 1 || shortages != 1 {
		t.Fatalf("winners=%d shortages=%d", winners, shortages)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var available, active int
	if err := h.flightDB.QueryRow(ctx, "SELECT available_seats FROM flights WHERE id=$1", seedFlightID).Scan(&available); err != nil || available != 0 {
		t.Fatalf("available=%d error=%v", available, err)
	}
	if err := h.flightDB.QueryRow(ctx, "SELECT count(*) FROM seat_reservations WHERE booking_id=ANY($1::uuid[]) AND status='ACTIVE'", ids).Scan(&active); err != nil || active != 1 {
		t.Fatalf("active=%d error=%v", active, err)
	}
	t.Logf("SELECT evidence: concurrent last-seat winner=%s active_reservations=%d available_seats=%d", winner, active, available)
	h.release(winner)
	h.release(holder)
	if got := h.getFlight().AvailableSeats; got != h.initial.AvailableSeats {
		t.Fatalf("fixture seats not restored: %d", got)
	}
}
