package flight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const seedFlight = "11111111-1111-4111-8111-111111111111"

func TestDatabaseDisconnectsAreRetryable(t *testing.T) {
	s := New(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 5*time.Minute)
	tests := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"closed connection", io.EOF, codes.Unavailable},
		{"truncated response", io.ErrUnexpectedEOF, codes.Unavailable},
		{"wrapped disconnect", fmt.Errorf("read response: %w", io.EOF), codes.Unavailable},
		{"database operation failure", errors.New("invalid statement"), codes.Internal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { requireCode(t, s.databaseError(t.Context(), "query", tt.err), tt.want) })
	}
}

func TestValidationRejectsMalformedRequests(t *testing.T) {
	s := New(nil, nil, nil, 5*time.Minute)
	ctx := t.Context()
	tests := []struct {
		name string
		call func() error
	}{
		{"get invalid UUID", func() error { _, err := s.GetFlight(ctx, &pb.GetFlightRequest{Id: "bad"}); return err }},
		{"get nil", func() error { _, err := s.GetFlight(ctx, nil); return err }},
		{"search origin", func() error {
			_, err := s.SearchFlights(ctx, &pb.SearchFlightsRequest{Origin: "svo", Destination: "LED"})
			return err
		}},
		{"search destination", func() error {
			_, err := s.SearchFlights(ctx, &pb.SearchFlightsRequest{Origin: "SVO", Destination: ""})
			return err
		}},
		{"search same airport", func() error {
			_, err := s.SearchFlights(ctx, &pb.SearchFlightsRequest{Origin: "SVO", Destination: "SVO"})
			return err
		}},
		{"search date", func() error {
			_, err := s.SearchFlights(ctx, &pb.SearchFlightsRequest{Origin: "SVO", Destination: "LED", Date: strptr("2030-02-30")})
			return err
		}},
		{"reserve count", func() error { _, err := s.ReserveSeats(ctx, reserveRequest(uuid.NewString(), 0)); return err }},
		{"reserve booking", func() error { _, err := s.ReserveSeats(ctx, reserveRequest("bad", 1)); return err }},
		{"release booking", func() error {
			_, err := s.ReleaseReservation(ctx, &pb.ReleaseReservationRequest{BookingId: "bad"})
			return err
		}},
		{"update price", func() error {
			_, err := s.UpdateFlight(ctx, &pb.UpdateFlightRequest{Id: seedFlight, PriceMinor: 0, Status: pb.FlightStatus_SCHEDULED})
			return err
		}},
		{"update status", func() error {
			_, err := s.UpdateFlight(ctx, &pb.UpdateFlightRequest{Id: seedFlight, PriceMinor: 1, Status: pb.FlightStatus(99)})
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { requireCode(t, tt.call(), codes.InvalidArgument) })
	}
}

func TestReservationLifecycleIsIdempotent(t *testing.T) {
	s, pool := testService(t, nil)
	ctx := t.Context()
	booking := uuid.NewString()
	first, err := s.ReserveSeats(ctx, reserveRequest(booking, 2))
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.ReserveSeats(ctx, reserveRequest(booking, 2))
	if err != nil {
		t.Fatal(err)
	}
	if first.Id == "" || first.Id != again.Id || again.Status != pb.ReservationStatus_ACTIVE {
		t.Fatalf("unexpected replay: %v / %v", first, again)
	}
	_, err = s.ReserveSeats(ctx, reserveRequest(booking, 3))
	requireCode(t, err, codes.AlreadyExists)
	assertSeats(t, pool, 8)
	released, err := s.ReleaseReservation(ctx, &pb.ReleaseReservationRequest{BookingId: booking})
	if err != nil || !released.GetReleased() {
		t.Fatalf("release: %v, %v", released, err)
	}
	released, err = s.ReleaseReservation(ctx, &pb.ReleaseReservationRequest{BookingId: booking})
	if err != nil || released.GetReleased() {
		t.Fatalf("repeat release: %v, %v", released, err)
	}
	assertSeats(t, pool, 10)
	_, err = s.ReserveSeats(ctx, reserveRequest(booking, 2))
	requireCode(t, err, codes.FailedPrecondition)
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM seat_reservations WHERE booking_id=$1 AND status='RELEASED'", booking).Scan(&count); err != nil || count != 1 {
		t.Fatalf("released reservations: %d, %v", count, err)
	}
}

func TestReleaseBeforeReserveFencesLateRequest(t *testing.T) {
	s, pool := testService(t, nil)
	booking := uuid.NewString()
	response, err := s.ReleaseReservation(t.Context(), &pb.ReleaseReservationRequest{BookingId: booking})
	if err != nil || response.GetReleased() {
		t.Fatalf("release unknown: %v, %v", response, err)
	}
	_, err = s.ReserveSeats(t.Context(), reserveRequest(booking, 1))
	requireCode(t, err, codes.FailedPrecondition)
	assertSeats(t, pool, 10)
}

func TestConcurrentLastSeatHasOneWinner(t *testing.T) {
	s, pool := testService(t, nil)
	if _, err := pool.Exec(t.Context(), "UPDATE flights SET total_seats=1, available_seats=1 WHERE id=$1", seedFlight); err != nil {
		t.Fatal(err)
	}
	errors := make(chan error, 12)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			<-start
			_, err := s.ReserveSeats(t.Context(), reserveRequest(uuid.NewString(), 1))
			errors <- err
		})
	}
	close(start)
	wg.Wait()
	close(errors)
	winners := 0
	for err := range errors {
		if err == nil {
			winners++
		} else {
			requireCode(t, err, codes.ResourceExhausted)
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d, want 1", winners)
	}
	assertSeats(t, pool, 0)
	var reservations int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM seat_reservations").Scan(&reservations); err != nil || reservations != 1 {
		t.Fatalf("reservations = %d, %v", reservations, err)
	}
}

func TestConcurrentSameBookingConsumesSeatsOnce(t *testing.T) {
	s, pool := testService(t, nil)
	booking := uuid.NewString()
	responses := make(chan *pb.SeatReservation, 8)
	errors := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			r, err := s.ReserveSeats(t.Context(), reserveRequest(booking, 2))
			responses <- r
			errors <- err
		})
	}
	wg.Wait()
	close(responses)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var id string
	for response := range responses {
		if id == "" {
			id = response.Id
		}
		if id != response.Id {
			t.Fatalf("different reservation IDs: %q, %q", id, response.Id)
		}
	}
	assertSeats(t, pool, 8)
}

func TestReservationInsertFailureRollsBackSeats(t *testing.T) {
	s, pool := testService(t, nil)
	_, err := pool.Exec(t.Context(), "CREATE FUNCTION reject_reservation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END $$; CREATE TRIGGER reject_reservation BEFORE INSERT ON seat_reservations FOR EACH ROW EXECUTE FUNCTION reject_reservation()")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ReserveSeats(t.Context(), reserveRequest(uuid.NewString(), 2))
	requireCode(t, err, codes.Internal)
	assertSeats(t, pool, 10)
	var revision int64
	if err := pool.QueryRow(t.Context(), "SELECT revision FROM cache_revision WHERE id=1").Scan(&revision); err != nil || revision != 1 {
		t.Fatalf("revision = %d, %v", revision, err)
	}
}

func TestReleaseFailureRollsBackSeatsAndFence(t *testing.T) {
	s, pool := testService(t, nil)
	booking := uuid.NewString()
	if _, err := s.ReserveSeats(t.Context(), reserveRequest(booking, 2)); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(t.Context(), "CREATE FUNCTION reject_release() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END $$; CREATE TRIGGER reject_release BEFORE UPDATE ON seat_reservations FOR EACH ROW EXECUTE FUNCTION reject_release()")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ReleaseReservation(t.Context(), &pb.ReleaseReservationRequest{BookingId: booking})
	requireCode(t, err, codes.Internal)
	assertSeats(t, pool, 8)
	var fences int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM reservation_fences WHERE booking_id=$1", booking).Scan(&fences); err != nil || fences != 0 {
		t.Fatalf("fences = %d, %v", fences, err)
	}
	_, err = pool.Exec(t.Context(), "DROP TRIGGER reject_release ON seat_reservations")
	if err != nil {
		t.Fatal(err)
	}
	response, err := s.ReleaseReservation(t.Context(), &pb.ReleaseReservationRequest{BookingId: booking})
	if err != nil || !response.GetReleased() {
		t.Fatalf("retry release: %v, %v", response, err)
	}
	assertSeats(t, pool, 10)
}

func TestConcurrentReleaseAndReserveCannotLeaveActiveReservation(t *testing.T) {
	s, pool := testService(t, nil)
	for range 12 {
		booking := uuid.NewString()
		start := make(chan struct{})
		reserveErr := make(chan error, 1)
		releaseErr := make(chan error, 1)
		go func() { <-start; _, err := s.ReserveSeats(t.Context(), reserveRequest(booking, 1)); reserveErr <- err }()
		go func() {
			<-start
			_, err := s.ReleaseReservation(t.Context(), &pb.ReleaseReservationRequest{BookingId: booking})
			releaseErr <- err
		}()
		close(start)
		if err := <-reserveErr; err != nil {
			requireCode(t, err, codes.FailedPrecondition)
		}
		if err := <-releaseErr; err != nil {
			t.Fatal(err)
		}
		assertSeats(t, pool, 10)
	}
	var active int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM seat_reservations WHERE status='ACTIVE'").Scan(&active); err != nil || active != 0 {
		t.Fatalf("active reservations = %d, %v", active, err)
	}
}

func TestFlightConstraintsRejectInvalidValuesAndDuplicateUTCDay(t *testing.T) {
	_, pool := testService(t, nil)
	for _, query := range []string{
		"UPDATE flights SET available_seats=-1",
		"UPDATE flights SET available_seats=11",
		"UPDATE flights SET total_seats=0",
		"UPDATE flights SET price_minor=0",
		"UPDATE flights SET origin=destination",
		"UPDATE flights SET arrival_at=departure_at",
		"INSERT INTO flights SELECT '22222222-2222-4222-8222-222222222222', flight_number, origin, destination, '2030-04-02T01:00:00+03:00', '2030-04-02T03:00:00+03:00', total_seats, available_seats, price_minor, status FROM flights",
	} {
		if _, err := pool.Exec(t.Context(), query); err == nil {
			t.Fatalf("constraint accepted: %s", query)
		}
	}
}

func TestSearchAndUpdateUseUTCDateAndScheduledStatus(t *testing.T) {
	s, _ := testService(t, nil)
	ctx := t.Context()
	response, err := s.SearchFlights(ctx, &pb.SearchFlightsRequest{Origin: "SVO", Destination: "LED", Date: strptr("2030-04-01")})
	if err != nil || len(response.GetFlights()) != 1 {
		t.Fatalf("search: %v, %v", response, err)
	}
	if f := response.Flights[0]; f.Id != seedFlight || f.PriceMinor != 125000 || f.FlightNumber != "SU1234" || f.AvailableSeats != 10 {
		t.Fatalf("seed flight: %v", f)
	}
	response, err = s.SearchFlights(ctx, &pb.SearchFlightsRequest{Origin: "SVO", Destination: "LED", Date: strptr("2030-04-02")})
	if err != nil || len(response.GetFlights()) != 0 {
		t.Fatalf("different day search: %v, %v", response, err)
	}
	updated, err := s.UpdateFlight(ctx, &pb.UpdateFlightRequest{Id: seedFlight, PriceMinor: 150000, Status: pb.FlightStatus_CANCELLED})
	if err != nil || updated.GetPriceMinor() != 150000 || updated.GetStatus() != pb.FlightStatus_CANCELLED {
		t.Fatalf("update: %v, %v", updated, err)
	}
	response, err = s.SearchFlights(ctx, &pb.SearchFlightsRequest{Origin: "SVO", Destination: "LED"})
	if err != nil || len(response.GetFlights()) != 0 {
		t.Fatalf("cancelled search: %v, %v", response, err)
	}
	_, err = s.ReserveSeats(ctx, reserveRequest(uuid.NewString(), 1))
	requireCode(t, err, codes.FailedPrecondition)
	_, err = s.GetFlight(ctx, &pb.GetFlightRequest{Id: uuid.NewString()})
	requireCode(t, err, codes.NotFound)
}

func TestCacheRejectsStaleFlightAndSearchAfterMutation(t *testing.T) {
	cache := testRedis(t)
	s, _ := testService(t, cache)
	ctx := t.Context()
	search := &pb.SearchFlightsRequest{Origin: "SVO", Destination: "LED", Date: strptr("2030-04-01")}
	flightKey, searchKey := "flight:"+seedFlight, "search:SVO:LED:2030-04-01"
	if _, err := cache.Del(ctx, flightKey, searchKey).Result(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetFlight(ctx, &pb.GetFlightRequest{Id: seedFlight}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SearchFlights(ctx, search); err != nil {
		t.Fatal(err)
	}
	oldFlight, err := cache.Get(ctx, flightKey).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	oldSearch, err := cache.Get(ctx, searchKey).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{flightKey, searchKey} {
		ttl, err := cache.TTL(ctx, key).Result()
		if err != nil || ttl <= 0 || ttl > 5*time.Minute {
			t.Fatalf("TTL for %s: %v, %v", key, ttl, err)
		}
	}
	booking := uuid.NewString()
	if _, err := s.ReserveSeats(ctx, reserveRequest(booking, 2)); err != nil {
		t.Fatal(err)
	}
	if n, err := cache.Exists(ctx, flightKey, searchKey).Result(); err != nil || n != 0 {
		t.Fatalf("invalidated keys = %d, %v", n, err)
	}
	if err := cache.Set(ctx, flightKey, oldFlight, 5*time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := cache.Set(ctx, searchKey, oldSearch, 5*time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	f, err := s.GetFlight(ctx, &pb.GetFlightRequest{Id: seedFlight})
	if err != nil || f.GetAvailableSeats() != 8 {
		t.Fatalf("stale flight accepted: %v, %v", f, err)
	}
	result, err := s.SearchFlights(ctx, search)
	if err != nil || len(result.GetFlights()) != 1 || result.Flights[0].AvailableSeats != 8 {
		t.Fatalf("stale search accepted: %v, %v", result, err)
	}
	if _, err := s.ReleaseReservation(ctx, &pb.ReleaseReservationRequest{BookingId: booking}); err != nil {
		t.Fatal(err)
	}
	f, err = s.GetFlight(ctx, &pb.GetFlightRequest{Id: seedFlight})
	if err != nil || f.GetAvailableSeats() != 10 {
		t.Fatalf("release cache: %v, %v", f, err)
	}
	if _, err := s.UpdateFlight(ctx, &pb.UpdateFlightRequest{Id: seedFlight, PriceMinor: 50000, Status: pb.FlightStatus_CANCELLED}); err != nil {
		t.Fatal(err)
	}
	f, err = s.GetFlight(ctx, &pb.GetFlightRequest{Id: seedFlight})
	if err != nil || f.GetPriceMinor() != 50000 {
		t.Fatalf("update cache: %v, %v", f, err)
	}
	result, err = s.SearchFlights(ctx, search)
	if err != nil || len(result.GetFlights()) != 0 {
		t.Fatalf("update search cache: %v, %v", result, err)
	}
}

func TestRedisFailureFallsBackToDatabase(t *testing.T) {
	cache := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 20 * time.Millisecond, ReadTimeout: 20 * time.Millisecond, WriteTimeout: 20 * time.Millisecond, MaxRetries: -1})
	t.Cleanup(func() { _ = cache.Close() })
	s, _ := testService(t, cache)
	f, err := s.GetFlight(t.Context(), &pb.GetFlightRequest{Id: seedFlight})
	if err != nil || f.GetAvailableSeats() != 10 {
		t.Fatalf("fallback: %v, %v", f, err)
	}
}

func TestMalformedCacheDoesNotLeakPartiallyDecodedSearch(t *testing.T) {
	cache := testRedis(t)
	s, _ := testService(t, cache)
	ctx := t.Context()
	key := "search:SVO:LED:2030-04-01"
	if err := cache.Del(ctx, key).Err(); err != nil {
		t.Fatal(err)
	}
	request := &pb.SearchFlightsRequest{Origin: "SVO", Destination: "LED", Date: strptr("2030-04-01")}
	if _, err := s.SearchFlights(ctx, request); err != nil {
		t.Fatal(err)
	}
	raw, err := cache.Get(ctx, key).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var entry struct {
		Revision int64           `json:"revision"`
		Data     json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatal(err)
	}
	entry.Data = append(entry.Data[:len(entry.Data)-1], []byte(",\"unexpected_field\":true}")...)
	corrupt, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Set(ctx, key, corrupt, 5*time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	response, err := s.SearchFlights(ctx, request)
	if err != nil || len(response.GetFlights()) != 1 {
		t.Fatalf("partially decoded cache leaked into fallback: %v, %v", response, err)
	}
}

func testService(t *testing.T, cache *redis.Client) (*Service, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_FLIGHT_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_FLIGHT_DATABASE_URL is unset")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_flight_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(); _, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); admin.Close() })
	files, err := filepath.Glob("../../migrations/flight/V*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			t.Fatalf("migration %s: %v", path, err)
		}
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return New(pool, cache, logger, 5*time.Minute), pool
}

func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR is unset")
	}
	cache := redis.NewClient(&redis.Options{Addr: addr, DB: 15})
	t.Cleanup(func() { _ = cache.Close() })
	return cache
}

func assertSeats(t *testing.T, pool *pgxpool.Pool, want int32) {
	t.Helper()
	var available int32
	if err := pool.QueryRow(t.Context(), "SELECT available_seats FROM flights WHERE id=$1", seedFlight).Scan(&available); err != nil {
		t.Fatal(err)
	}
	if available != want {
		t.Fatalf("available_seats = %d, want %d", available, want)
	}
}

func requireCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if status.Code(err) != want {
		t.Fatalf("code = %v, want %v: %v", status.Code(err), want, err)
	}
}

func reserveRequest(booking string, seats int32) *pb.ReserveSeatsRequest {
	return &pb.ReserveSeatsRequest{FlightId: seedFlight, BookingId: booking, SeatCount: seats}
}

func strptr(s string) *string { return &s }
