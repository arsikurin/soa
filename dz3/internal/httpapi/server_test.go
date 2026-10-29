package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"flightbooking/internal/booking"
	"github.com/google/uuid"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type bookingStub struct {
	calls int
	key   uuid.UUID
	err   error
}

func (s *bookingStub) Create(ctx context.Context, input booking.CreateInput, key uuid.UUID) (*booking.Booking, error) {
	s.calls++
	s.key = key
	if s.err != nil {
		return nil, s.err
	}
	return &booking.Booking{ID: key, UserID: input.UserID, FlightID: input.FlightID, PassengerName: input.PassengerName, PassengerEmail: input.PassengerEmail, SeatCount: input.SeatCount, TotalPriceMinor: 250001, Status: "CONFIRMED", CreatedAt: time.Now(), UpdatedAt: time.Now()}, nil
}
func (s *bookingStub) Get(context.Context, uuid.UUID) (*booking.Booking, error) {
	return nil, status.Error(codes.NotFound, "booking not found")
}
func (s *bookingStub) List(context.Context, uuid.UUID) ([]booking.Booking, error) {
	return []booking.Booking{}, nil
}
func (s *bookingStub) Cancel(context.Context, uuid.UUID) (*booking.Booking, error) {
	return nil, status.Error(codes.NotFound, "booking not found")
}

const goodBody = `{"user_id":"22222222-2222-4222-8222-222222222222","flight_id":"11111111-1111-4111-8111-111111111111","passenger_name":"Ivan Ivanov","passenger_email":"ivan@example.com","seat_count":2}`

func TestInvalidRequestNeverCallsBooking(t *testing.T) {
	for _, body := range []string{`{}`, `{"seat_count":0}`, `{"user_id":"not-a-uuid"}`, `{"user_id":"22222222-2222-4222-8222-222222222222","flight_id":"11111111-1111-4111-8111-111111111111","passenger_name":" ","passenger_email":"ivan@example.com","seat_count":2}`, `{"user_id":"22222222-2222-4222-8222-222222222222","flight_id":"11111111-1111-4111-8111-111111111111","passenger_name":"Ivan","passenger_email":"invalid","seat_count":2}`} {
		t.Run(body, func(t *testing.T) {
			backend := &bookingStub{}
			handler, err := New(backend, nil, slog.New(slog.NewJSONHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/bookings", bytes.NewBufferString(body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 400 || backend.calls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, backend.calls, w.Body.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || payload["error_code"] != "INVALID_ARGUMENT" {
				t.Fatalf("invalid error: %s", w.Body.String())
			}
		})
	}
}
func TestCreateBookingMapsMoneyAndIdempotencyKey(t *testing.T) {
	backend := &bookingStub{}
	var logs bytes.Buffer
	handler, err := New(backend, nil, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/bookings", bytes.NewBufferString(goodBody))
	r.Header.Set("Content-Type", "application/json")
	key := uuid.New()
	r.Header.Set("Idempotency-Key", key.String())
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 201 || backend.key != key {
		t.Fatalf("status=%d key=%s body=%s", w.Code, backend.key, w.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["total_price"] != "2500.01" || payload["currency"] != "RUB" || payload["id"] != key.String() {
		t.Fatalf("payload=%v", payload)
	}
	if _, err := uuid.Parse(w.Header().Get("X-Request-Id")); err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(logs.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event["status_code"] != float64(201) || event["request_id"] != w.Header().Get("X-Request-Id") {
		t.Fatalf("event=%v", event)
	}
}
func TestPendingOperationReturnsItsBookingID(t *testing.T) {
	key := uuid.New()
	failure, err := status.New(codes.Unavailable, "Recovery pending").WithDetails(&errdetails.ErrorInfo{Reason: "OPERATION_PENDING", Metadata: map[string]string{"booking_id": key.String()}})
	if err != nil {
		t.Fatal(err)
	}
	backend := &bookingStub{err: failure.Err()}
	handler, err := New(backend, nil, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/bookings", bytes.NewBufferString(goodBody))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var payload map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &payload)
	if w.Code != 503 || payload["error_code"] != "OPERATION_PENDING" || payload["booking_id"] != key.String() {
		t.Fatalf("status=%d payload=%v", w.Code, payload)
	}
}

func TestAbortedOperationIsConflict(t *testing.T) {
	response := contractError(status.Error(codes.Aborted, "booking creation was interrupted"))
	if response.StatusCode != 409 || response.Response.ErrorCode != "ABORTED" {
		t.Fatalf("response=%+v", response)
	}
}
