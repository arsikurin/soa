package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"flightbooking/internal/rpc/pb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

const seedFlightID = "11111111-1111-4111-8111-111111111111"

type harness struct {
	t         *testing.T
	root      string
	baseURL   string
	key       string
	http      *http.Client
	rpc       pb.FlightServiceClient
	flightDB  *pgxpool.Pool
	bookingDB *pgxpool.Pool
	initial   *pb.Flight
}

type bookingView struct {
	ID         string `json:"id"`
	UserID     string `json:"user_id"`
	FlightID   string `json:"flight_id"`
	SeatCount  int32  `json:"seat_count"`
	TotalPrice string `json:"total_price"`
	Currency   string `json:"currency"`
	Status     string `json:"status"`
}

type flightView struct {
	ID             string `json:"id"`
	AvailableSeats int32  `json:"available_seats"`
	Price          string `json:"price"`
	Status         string `json:"status"`
}

type errorView struct {
	Code string `json:"error_code"`
}

type httpResult struct {
	status  int
	body    []byte
	elapsed time.Duration
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	if os.Getenv("RUN_E2E") != "1" {
		t.Skip("set RUN_E2E=1 to test the dedicated flight-booking-homework Compose stack")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, root: root, baseURL: env("E2E_HTTP_URL", "http://127.0.0.1:"+env("BOOKING_PORT", "8090")), key: env("FLIGHT_API_KEY", "homework-flight-service-key-change-me"), http: &http.Client{Timeout: 15 * time.Second}}
	h.flightDB = openPool(t, env("E2E_FLIGHT_DATABASE_URL", "postgres://flight:flight@127.0.0.1:"+env("FLIGHT_POSTGRES_PORT", "55431")+"/flight?sslmode=disable"))
	h.bookingDB = openPool(t, env("E2E_BOOKING_DATABASE_URL", "postgres://booking:booking@127.0.0.1:"+env("BOOKING_POSTGRES_PORT", "55432")+"/booking?sslmode=disable"))
	conn, err := grpc.NewClient(env("E2E_GRPC_ADDR", "127.0.0.1:"+env("FLIGHT_GRPC_PORT", "50061")), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDisableRetry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	h.rpc = pb.NewFlightServiceClient(conn)
	h.initial = h.getFlight()
	if h.initial.Status != pb.FlightStatus_SCHEDULED {
		t.Fatalf("seed fixture must be SCHEDULED, got %s", h.initial.Status)
	}
	t.Cleanup(func() {
		ctx, cancel := h.rpcContext()
		defer cancel()
		_, err := h.rpc.UpdateFlight(ctx, &pb.UpdateFlightRequest{Id: seedFlightID, PriceMinor: h.initial.PriceMinor, Status: h.initial.Status})
		if err != nil {
			t.Errorf("restore fixture price/status: %v", err)
		}
	})
	return h
}

func openPool(t *testing.T, address string) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("database unavailable: %v", err)
	}
	return pool
}

func (h *harness) rpcContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	return metadata.AppendToOutgoingContext(ctx, "x-api-key", h.key), cancel
}

func (h *harness) getFlight() *pb.Flight {
	h.t.Helper()
	ctx, cancel := h.rpcContext()
	defer cancel()
	flight, err := h.rpc.GetFlight(ctx, &pb.GetFlightRequest{Id: seedFlightID})
	if err != nil {
		h.t.Fatal(err)
	}
	return flight
}

func (h *harness) updateFlight(price int64, state pb.FlightStatus) {
	h.t.Helper()
	ctx, cancel := h.rpcContext()
	defer cancel()
	if _, err := h.rpc.UpdateFlight(ctx, &pb.UpdateFlightRequest{Id: seedFlightID, PriceMinor: price, Status: state}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) reserve(id string, seats int32) *pb.SeatReservation {
	h.t.Helper()
	ctx, cancel := h.rpcContext()
	defer cancel()
	reservation, err := h.rpc.ReserveSeats(ctx, &pb.ReserveSeatsRequest{FlightId: seedFlightID, BookingId: id, SeatCount: seats})
	if err != nil {
		h.t.Fatal(err)
	}
	return reservation
}

func (h *harness) release(id string) *pb.ReleaseReservationResponse {
	h.t.Helper()
	ctx, cancel := h.rpcContext()
	defer cancel()
	result, err := h.rpc.ReleaseReservation(ctx, &pb.ReleaseReservationRequest{BookingId: id})
	if err != nil {
		h.t.Fatal(err)
	}
	return result
}

func (h *harness) bookingID() string {
	id := uuid.NewString()
	h.t.Cleanup(func() {
		ctx, cancel := h.rpcContext()
		defer cancel()
		if _, err := h.rpc.ReleaseReservation(ctx, &pb.ReleaseReservationRequest{BookingId: id}); err != nil {
			h.t.Errorf("release test reservation %s: %v", id, err)
			return
		}
		for _, item := range []struct {
			pool  *pgxpool.Pool
			query string
		}{
			{h.bookingDB, "DELETE FROM bookings WHERE id=$1"},
			{h.bookingDB, "DELETE FROM booking_operations WHERE booking_id=$1"},
			{h.flightDB, "DELETE FROM seat_reservations WHERE booking_id=$1"},
			{h.flightDB, "DELETE FROM reservation_fences WHERE booking_id=$1"},
		} {
			if _, err := item.pool.Exec(ctx, item.query, id); err != nil {
				h.t.Errorf("clean test record %s: %v", id, err)
			}
		}
	})
	return id
}

func createBody(user string, seats int32) map[string]any {
	return map[string]any{"user_id": user, "flight_id": seedFlightID, "passenger_name": "E2E Passenger", "passenger_email": "e2e@example.test", "seat_count": seats}
}

func (h *harness) doHTTP(method, path string, body any, key string) (httpResult, error) {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return httpResult{}, err
		}
	}
	request, err := http.NewRequest(method, h.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return httpResult{}, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	started := time.Now()
	response, err := h.http.Do(request)
	if err != nil {
		return httpResult{}, err
	}
	defer response.Body.Close()
	data, err = io.ReadAll(io.LimitReader(response.Body, 1024*1024))
	return httpResult{status: response.StatusCode, body: data, elapsed: time.Since(started)}, err
}

func (h *harness) request(method, path string, body any, key string) httpResult {
	h.t.Helper()
	result, err := h.doHTTP(method, path, body, key)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	return result
}

func (r httpResult) expect(t *testing.T, code int, value any) {
	t.Helper()
	if r.status != code {
		t.Fatalf("HTTP %d, want %d: %s", r.status, code, r.body)
	}
	if value != nil {
		if err := json.Unmarshal(r.body, value); err != nil {
			t.Fatalf("decode response %s: %v", r.body, err)
		}
	}
}

func eventually(t *testing.T, timeout time.Duration, description string, check func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastError error
	for time.Now().Before(deadline) {
		ok, err := check()
		if ok && err == nil {
			return
		}
		lastError = err
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s: %v", description, lastError)
}

func (h *harness) compose(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", append([]string{"compose", "--project-name", "flight-booking-homework", "--file", filepath.Join(h.root, "docker-compose.yml")}, args...)...)
	command.Dir = h.root
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker compose %s: %w: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output)), nil
}

func (h *harness) mustCompose(args ...string) string {
	h.t.Helper()
	output, err := h.compose(args...)
	if err != nil {
		h.t.Fatal(err)
	}
	return output
}

func (h *harness) restoreContainers(services ...string) {
	h.t.Cleanup(func() {
		if _, err := h.compose(append([]string{"start"}, services...)...); err != nil {
			h.t.Errorf("restore containers: %v", err)
			return
		}
		for _, service := range services {
			if service == "flight" {
				h.waitFlight()
			}
			if service == "booking" {
				eventually(h.t, 30*time.Second, "Booking HTTP readiness during cleanup", func() (bool, error) {
					response, err := h.doHTTP("GET", "/health", nil, "")
					return response.status == 200, err
				})
			}
		}
	})
}

func (h *harness) redisMaster() (string, error) {
	value, err := h.compose("exec", "-T", "sentinel-1", "redis-cli", "--raw", "-p", "26379", "SENTINEL", "get-master-addr-by-name", "flight-cache")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(value)
	if len(fields) != 2 || (fields[0] != "redis-a" && fields[0] != "redis-b") || fields[1] != "6379" {
		return "", fmt.Errorf("unexpected Sentinel master: %q", value)
	}
	return fields[0], nil
}

func (h *harness) redis(master string, args ...string) string {
	h.t.Helper()
	return h.mustCompose(append([]string{"exec", "-T", "sentinel-1", "redis-cli", "--raw", "-h", master}, args...)...)
}

func (h *harness) serviceEnvironment(service string) map[string]string {
	h.t.Helper()
	var config struct {
		Services map[string]struct {
			Environment map[string]string `json:"environment"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(h.mustCompose("config", "--format", "json")), &config); err != nil {
		h.t.Fatal(err)
	}
	return config.Services[service].Environment
}

func (h *harness) events(service string, since time.Time) []map[string]any {
	h.t.Helper()
	output := h.mustCompose("logs", "--no-color", "--no-log-prefix", "--since", since.UTC().Format(time.RFC3339Nano), service)
	var events []map[string]any
	for _, line := range strings.Split(output, "\n") {
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) == nil {
			events = append(events, event)
		}
	}
	return events
}

func money(minor int64) string {
	return strconv.FormatInt(minor/100, 10) + fmt.Sprintf(".%02d", minor%100)
}
