package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"flightbooking/internal/rpc/pb"
	"github.com/google/uuid"
)

func TestRESTBookingLifecycleAndPriceSnapshot(t *testing.T) {
	h := newHarness(t)
	if h.initial.AvailableSeats < 2 {
		t.Fatal("fixture needs at least two available seats")
	}
	id, user := h.bookingID(), uuid.NewString()
	body := createBody(user, 2)
	var created bookingView
	h.request("POST", "/bookings", body, id).expect(t, 201, &created)
	if created.ID != id || created.UserID != user || created.FlightID != seedFlightID || created.SeatCount != 2 || created.TotalPrice != money(h.initial.PriceMinor*2) || created.Currency != "RUB" || created.Status != "CONFIRMED" {
		t.Fatalf("unexpected booking: %+v", created)
	}
	var repeated bookingView
	h.request("POST", "/bookings", body, id).expect(t, 201, &repeated)
	if repeated != created {
		t.Fatalf("idempotent create changed booking: %+v -> %+v", created, repeated)
	}
	body["seat_count"] = 1
	h.request("POST", "/bookings", body, id).expect(t, 409, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var dbStatus, reservationStatus string
	var total int64
	var count, available int
	if err := h.bookingDB.QueryRow(ctx, "SELECT status, total_price_minor FROM bookings WHERE id=$1", id).Scan(&dbStatus, &total); err != nil {
		t.Fatal(err)
	}
	if err := h.flightDB.QueryRow(ctx, "SELECT count(*), min(status) FROM seat_reservations WHERE booking_id=$1", id).Scan(&count, &reservationStatus); err != nil {
		t.Fatal(err)
	}
	if err := h.flightDB.QueryRow(ctx, "SELECT available_seats FROM flights WHERE id=$1", seedFlightID).Scan(&available); err != nil {
		t.Fatal(err)
	}
	if dbStatus != "CONFIRMED" || total != h.initial.PriceMinor*2 || count != 1 || reservationStatus != "ACTIVE" || available != int(h.initial.AvailableSeats)-2 {
		t.Fatalf("inconsistent SQL state: booking=%s total=%d reservation=%s count=%d available=%d", dbStatus, total, reservationStatus, count, available)
	}
	t.Logf("SELECT evidence: booking=%s status=%s total_price_minor=%d reservation_count=%d reservation_status=%s available_seats=%d", id, dbStatus, total, count, reservationStatus, available)
	h.updateFlight(h.initial.PriceMinor+11300, pb.FlightStatus_SCHEDULED)
	var loaded bookingView
	h.request("GET", "/bookings/"+id, nil, "").expect(t, 200, &loaded)
	if loaded.TotalPrice != created.TotalPrice {
		t.Fatalf("price snapshot changed: %s -> %s", created.TotalPrice, loaded.TotalPrice)
	}
	var listed []bookingView
	h.request("GET", "/bookings?user_id="+user, nil, "").expect(t, 200, &listed)
	if len(listed) != 1 || listed[0].ID != id {
		t.Fatalf("incorrect user booking list: %+v", listed)
	}
	for range 2 {
		var cancelled bookingView
		h.request("POST", "/bookings/"+id+"/cancel", nil, "").expect(t, 200, &cancelled)
		if cancelled.Status != "CANCELLED" || cancelled.TotalPrice != created.TotalPrice {
			t.Fatalf("unexpected canceled booking: %+v", cancelled)
		}
	}
	if got := h.getFlight().AvailableSeats; got != h.initial.AvailableSeats {
		t.Fatalf("cancel returned seats incorrectly: got %d want %d", got, h.initial.AvailableSeats)
	}
	if err := h.flightDB.QueryRow(ctx, "SELECT status FROM seat_reservations WHERE booking_id=$1", id).Scan(&reservationStatus); err != nil || reservationStatus != "RELEASED" {
		t.Fatalf("reservation=%s error=%v", reservationStatus, err)
	}
	t.Logf("SELECT evidence after repeated cancel: reservation_status=%s available_seats=%d", reservationStatus, h.initial.AvailableSeats)
}

func TestRESTSearchFiltersDateAndScheduledStatus(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/flights?origin=SVO&destination=LED&date=2030-04-01", "/flights?origin=SVO&destination=LED"} {
		var flights []flightView
		h.request("GET", path, nil, "").expect(t, 200, &flights)
		if !containsFlight(flights, seedFlightID) {
			t.Fatalf("seed missing from %s: %+v", path, flights)
		}
	}
	var otherDay []flightView
	h.request("GET", "/flights?origin=SVO&destination=LED&date=2030-04-02", nil, "").expect(t, 200, &otherDay)
	if containsFlight(otherDay, seedFlightID) {
		t.Fatal("date filter returned the previous day's flight")
	}
	h.updateFlight(h.initial.PriceMinor, pb.FlightStatus_CANCELLED)
	var cancelled []flightView
	h.request("GET", "/flights?origin=SVO&destination=LED&date=2030-04-01", nil, "").expect(t, 200, &cancelled)
	if containsFlight(cancelled, seedFlightID) {
		t.Fatal("search returned canceled flight")
	}
	var direct flightView
	h.request("GET", "/flights/"+seedFlightID, nil, "").expect(t, 200, &direct)
	if direct.Status != "CANCELLED" {
		t.Fatalf("get did not expose status update: %+v", direct)
	}
}

func containsFlight(flights []flightView, id string) bool {
	for _, flight := range flights {
		if flight.ID == id {
			return true
		}
	}
	return false
}

func TestRESTRejectsInvalidAndUnavailableBookings(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		name  string
		field string
		value any
	}{
		{"empty_name", "passenger_name", ""},
		{"blank_name", "passenger_name", "   "},
		{"long_name", "passenger_name", strings.Repeat("n", 256)},
		{"invalid_email", "passenger_email", "invalid"},
		{"long_email", "passenger_email", strings.Repeat("e", 243) + "@example.test"},
		{"zero_seats", "seat_count", 0},
		{"too_many_seats", "seat_count", 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local := *h
			local.t = t
			body := createBody(uuid.NewString(), 1)
			body[tc.field] = tc.value
			local.request("POST", "/bookings", body, "").expect(t, 400, nil)
		})
	}
	for _, tc := range []struct {
		path string
		code int
	}{
		{"/flights?origin=SVO", 400},
		{"/flights?origin=svo&destination=LED", 400},
		{"/flights?origin=SVO&destination=LED&date=not-a-date", 400},
		{"/flights/" + uuid.NewString(), 404},
		{"/bookings/" + uuid.NewString(), 404},
	} {
		h.request("GET", tc.path, nil, "").expect(t, tc.code, nil)
	}
	id := h.bookingID()
	var result errorView
	h.request("POST", "/bookings", createBody(uuid.NewString(), h.initial.AvailableSeats+1), id).expect(t, 409, &result)
	if result.Code != "RESOURCE_EXHAUSTED" {
		t.Fatalf("unexpected shortage error: %+v", result)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var count int
	if err := h.bookingDB.QueryRow(ctx, "SELECT count(*) FROM bookings WHERE id=$1", id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed create left booking: count=%d error=%v", count, err)
	}
	if got := h.getFlight().AvailableSeats; got != h.initial.AvailableSeats {
		t.Fatalf("failed create changed seats: %d -> %d", h.initial.AvailableSeats, got)
	}
}

func TestRESTAcceptsDocumentedInputBoundaries(t *testing.T) {
	h := newHarness(t)
	body := createBody(uuid.NewString(), 1)
	body["passenger_name"] = strings.Repeat("n", 255)
	body["passenger_email"] = strings.Repeat("p", 63) + "@" + strings.Repeat("d", 63) + "." + strings.Repeat("e", 63) + "." + strings.Repeat("f", 62)
	h.request("POST", "/bookings", body, h.bookingID()).expect(t, 201, nil)
	body = createBody(uuid.NewString(), 999)
	var result errorView
	h.request("POST", "/bookings", body, h.bookingID()).expect(t, 409, &result)
	if result.Code != "RESOURCE_EXHAUSTED" {
		t.Fatalf("valid seat-count boundary rejected before availability check: %+v", result)
	}
}
