package e2e

import (
	"strconv"
	"testing"
	"time"

	"flightbooking/internal/rpc/pb"
)

func TestRedisCacheTTLHitsAndMutationInvalidation(t *testing.T) {
	h := newHarness(t)
	master, err := h.redisMaster()
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{"flight:" + seedFlightID, "search:SVO:LED:2030-04-01", "search:SVO:LED:all"}
	h.redis(master, append([]string{"DEL"}, keys...)...)
	since := time.Now()
	warm := func() {
		h.getFlight()
		h.request("GET", "/flights?origin=SVO&destination=LED&date=2030-04-01", nil, "").expect(t, 200, nil)
		h.request("GET", "/flights?origin=SVO&destination=LED", nil, "").expect(t, 200, nil)
	}
	warm()
	h.getFlight()
	configuredTTL, err := time.ParseDuration(h.serviceEnvironment("flight")["CACHE_TTL"])
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		ttl, err := strconv.Atoi(h.redis(master, "TTL", key))
		if err != nil || ttl <= 0 || ttl > int(configuredTTL.Seconds()) {
			t.Fatalf("key=%s TTL=%d configured=%s error=%v", key, ttl, configuredTTL, err)
		}
		t.Logf("Redis SELECT evidence: key=%s TTL=%ds", key, ttl)
	}
	hit, miss := false, false
	for _, event := range h.events("flight", since) {
		if event["key"] == keys[0] {
			hit = hit || event["msg"] == "cache_hit"
			miss = miss || event["msg"] == "cache_miss"
		}
	}
	if !hit || !miss {
		t.Fatalf("missing cache evidence in JSON logs: hit=%t miss=%t", hit, miss)
	}
	assertInvalidated := func() {
		if count := h.redis(master, append([]string{"EXISTS"}, keys...)...); count != "0" {
			t.Fatalf("mutation left %s cached flight/search entries", count)
		}
	}
	id := h.bookingID()
	h.reserve(id, 1)
	assertInvalidated()
	warm()
	if got := h.getFlight().AvailableSeats; got != h.initial.AvailableSeats-1 {
		t.Fatalf("stale cache after reserve: %d", got)
	}
	h.release(id)
	assertInvalidated()
	warm()
	h.updateFlight(h.initial.PriceMinor+11300, pb.FlightStatus_SCHEDULED)
	assertInvalidated()
	if got := h.getFlight().PriceMinor; got != h.initial.PriceMinor+11300 {
		t.Fatalf("stale cache after price update: %d", got)
	}
}
