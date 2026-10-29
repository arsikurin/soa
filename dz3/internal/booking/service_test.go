package booking

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"flightbooking/internal/rpc/pb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestCreateStoresPriceSnapshotAndIsolatesUserList(t *testing.T) {
	pool := bookingDatabase(t)
	flights := newFlightBoundary()
	service := New(pool, flights, testLogger())
	input := validInput()
	id := uuid.New()
	got, err := service.Create(context.Background(), input, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.UserID != input.UserID || got.FlightID != input.FlightID || got.TotalPriceMinor != 250000 || got.Status != "CONFIRMED" || got.SeatCount != 2 {
		t.Fatalf("unexpected booking: %+v", got)
	}
	flights.setPrice(999999)
	stored, err := service.Get(context.Background(), id)
	if err != nil || stored.TotalPriceMinor != 250000 {
		t.Fatalf("price snapshot changed: %+v, %v", stored, err)
	}
	list, err := service.List(context.Background(), input.UserID)
	if err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("user list: %+v, %v", list, err)
	}
	list, err = service.List(context.Background(), uuid.New())
	if err != nil || len(list) != 0 {
		t.Fatalf("other user's list: %+v, %v", list, err)
	}
	if flights.activeSeats(id) != 2 {
		t.Fatal("booking did not reserve exactly two seats")
	}
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		edit func(*CreateInput)
	}{
		{"missing user", func(v *CreateInput) { v.UserID = uuid.Nil }},
		{"missing flight", func(v *CreateInput) { v.FlightID = uuid.Nil }},
		{"missing name", func(v *CreateInput) { v.PassengerName = "  " }},
		{"invalid email", func(v *CreateInput) { v.PassengerEmail = "missing-at" }},
		{"no seats", func(v *CreateInput) { v.SeatCount = 0 }},
		{"negative seats", func(v *CreateInput) { v.SeatCount = -1 }},
		{"too many seats", func(v *CreateInput) { v.SeatCount = 1000 }},
		{"name too long", func(v *CreateInput) { v.PassengerName = strings.Repeat("Ж", 256) }},
		{"email too long", func(v *CreateInput) { v.PassengerEmail = strings.Repeat("x", 243) + "@example.com" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := validInput()
			tt.edit(&input)
			_, err := New(nil, nil, testLogger()).Create(context.Background(), input, uuid.New())
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("wanted InvalidArgument, got %v", err)
			}
		})
	}
}

func TestCreateAcceptsMaximumUnicodeNameLength(t *testing.T) {
	pool := bookingDatabase(t)
	service := New(pool, newFlightBoundary(), testLogger())
	input := validInput()
	input.PassengerName = strings.Repeat("Ж", 255)
	booking, err := service.Create(context.Background(), input, uuid.New())
	if err != nil || booking.PassengerName != input.PassengerName {
		t.Fatalf("valid Unicode name rejected: %+v, %v", booking, err)
	}
}

func bookingDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_BOOKING_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_BOOKING_DATABASE_URL is required")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := "booking_test_" + uuid.New().String()[:8]
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	files, err := filepath.Glob("../../migrations/booking/V*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("booking migration missing")
	}
	for _, name := range files {
		sql, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func validInput() CreateInput {
	return CreateInput{
		UserID: uuid.New(), FlightID: uuid.MustParse("11111111-1111-4111-8111-111111111111"),
		PassengerName: "Ivan Petrov", PassengerEmail: "ivan@example.com", SeatCount: 2,
	}
}

type flightBoundary struct {
	pb.FlightServiceClient
	mu            sync.Mutex
	flight        *pb.Flight
	reservations  map[string]*pb.SeatReservation
	fences        map[string]bool
	getErr        error
	reserveErr    error
	releaseErr    error
	beforeReserve func(context.Context, *pb.ReserveSeatsRequest) error
	afterReserve  func(context.Context, *pb.ReserveSeatsRequest) error
	beforeRelease func(context.Context, *pb.ReleaseReservationRequest) error
}

func newFlightBoundary() *flightBoundary {
	return &flightBoundary{
		flight:       &pb.Flight{Id: "11111111-1111-4111-8111-111111111111", FlightNumber: "SU1234", Airline: "Aeroflot", Origin: "SVO", Destination: "LED", DepartureAt: timestamppb.New(time.Date(2030, 4, 1, 9, 0, 0, 0, time.UTC)), ArrivalAt: timestamppb.New(time.Date(2030, 4, 1, 10, 30, 0, 0, time.UTC)), TotalSeats: 10, AvailableSeats: 10, PriceMinor: 125000, Status: pb.FlightStatus_SCHEDULED},
		reservations: make(map[string]*pb.SeatReservation), fences: make(map[string]bool),
	}
}

func (f *flightBoundary) GetFlight(ctx context.Context, req *pb.GetFlightRequest, opts ...grpc.CallOption) (*pb.Flight, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	if req.Id != f.flight.Id {
		return nil, status.Error(codes.NotFound, "flight not found")
	}
	return proto.Clone(f.flight).(*pb.Flight), nil
}

func (f *flightBoundary) ReserveSeats(ctx context.Context, req *pb.ReserveSeatsRequest, opts ...grpc.CallOption) (*pb.SeatReservation, error) {
	if f.beforeReserve != nil {
		if err := f.beforeReserve(ctx, req); err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	if f.reserveErr != nil {
		f.mu.Unlock()
		return nil, f.reserveErr
	}
	if f.fences[req.BookingId] {
		f.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "reservation released")
	}
	if existing := f.reservations[req.BookingId]; existing != nil {
		f.mu.Unlock()
		if existing.FlightId != req.FlightId || existing.SeatCount != req.SeatCount {
			return nil, status.Error(codes.AlreadyExists, "reservation differs")
		}
		return proto.Clone(existing).(*pb.SeatReservation), nil
	}
	if req.SeatCount > f.flight.AvailableSeats {
		f.mu.Unlock()
		return nil, status.Error(codes.ResourceExhausted, "not enough seats")
	}
	reservation := &pb.SeatReservation{Id: uuid.NewString(), BookingId: req.BookingId, FlightId: req.FlightId, SeatCount: req.SeatCount, Status: pb.ReservationStatus_ACTIVE}
	f.reservations[req.BookingId] = reservation
	f.flight.AvailableSeats -= req.SeatCount
	f.mu.Unlock()
	if f.afterReserve != nil {
		if err := f.afterReserve(ctx, req); err != nil {
			return nil, err
		}
	}
	return proto.Clone(reservation).(*pb.SeatReservation), nil
}

func (f *flightBoundary) ReleaseReservation(ctx context.Context, req *pb.ReleaseReservationRequest, opts ...grpc.CallOption) (*pb.ReleaseReservationResponse, error) {
	if f.beforeRelease != nil {
		if err := f.beforeRelease(ctx, req); err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.releaseErr != nil {
		return nil, f.releaseErr
	}
	f.fences[req.BookingId] = true
	reservation := f.reservations[req.BookingId]
	if reservation == nil || reservation.Status != pb.ReservationStatus_ACTIVE {
		return &pb.ReleaseReservationResponse{}, nil
	}
	reservation.Status = pb.ReservationStatus_RELEASED
	f.flight.AvailableSeats += reservation.SeatCount
	return &pb.ReleaseReservationResponse{Released: true}, nil
}

func (f *flightBoundary) setPrice(price int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flight.PriceMinor = price
}

func (f *flightBoundary) activeSeats(id uuid.UUID) int32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	reservation := f.reservations[id.String()]
	if reservation == nil || reservation.Status != pb.ReservationStatus_ACTIVE {
		return 0
	}
	return reservation.SeatCount
}
