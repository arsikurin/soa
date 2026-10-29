package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"flightbooking/internal/rpc/pb"
	"google.golang.org/grpc/metadata"
)

func TestCircuitBreakerStopsCallsAndRecoversAfterFlightRestart(t *testing.T) {
	h := newHarness(t)
	configuration := h.serviceEnvironment("booking")
	threshold, err := strconv.Atoi(configuration["CIRCUIT_FAILURE_THRESHOLD"])
	if err != nil || threshold < 1 || threshold > 100 {
		t.Fatalf("unsupported test failure threshold %q", configuration["CIRCUIT_FAILURE_THRESHOLD"])
	}
	openTimeout, err := time.ParseDuration(configuration["CIRCUIT_OPEN_TIMEOUT"])
	if err != nil {
		t.Fatal(err)
	}
	h.request("GET", "/flights/"+seedFlightID, nil, "").expect(t, 200, nil)
	h.restoreContainers("flight")
	since := time.Now()
	h.mustCompose("stop", "--timeout", "5", "flight")
	for attempt := 0; attempt < threshold; attempt++ {
		var failure errorView
		h.request("GET", "/flights/"+seedFlightID, nil, "").expect(t, 503, &failure)
		if failure.Code == "CIRCUIT_OPEN" {
			t.Fatalf("circuit opened after only %d logical failures; threshold=%d", attempt, threshold)
		}
	}
	var failure errorView
	openResponse := h.request("GET", "/flights/"+seedFlightID, nil, "")
	openResponse.expect(t, 503, &failure)
	if failure.Code != "CIRCUIT_OPEN" || openResponse.elapsed > 500*time.Millisecond {
		t.Fatalf("open circuit response code=%s elapsed=%s", failure.Code, openResponse.elapsed)
	}
	t.Logf("Open circuit returned HTTP 503 CIRCUIT_OPEN in %s", openResponse.elapsed)
	h.mustCompose("start", "flight")
	h.waitFlight()
	eventually(t, openTimeout+30*time.Second, "booking breaker to close after a successful probe", func() (bool, error) {
		response, err := h.doHTTP("GET", "/flights/"+seedFlightID, nil, "")
		return response.status == 200, err
	})
	initialRetries, recoveryRetries, probeRetries := 0, 0, 0
	state := "CLOSED"
	var transitions []string
	for _, event := range h.events("booking", since) {
		if event["event"] == "rpc_retry" && event["method"] == "/flight.v1.FlightService/GetFlight" {
			switch state {
			case "CLOSED":
				if event["attempt"] != float64(2+initialRetries%2) {
					t.Fatalf("outage call exceeded its three-attempt budget: %v", event)
				}
				initialRetries++
			case "HALF_OPEN":
				if probeRetries >= 2 || event["attempt"] != float64(2+probeRetries) {
					t.Fatalf("half-open probe exceeded its three-attempt budget: %v", event)
				}
				probeRetries++
				recoveryRetries++
			default:
				t.Fatalf("open circuit issued retry: %v", event)
			}
		}
		if event["event"] == "circuit_state_change" {
			from, to := fmt.Sprint(event["from"]), fmt.Sprint(event["to"])
			if from != state {
				t.Fatalf("inconsistent circuit transition: state=%s event=%v", state, event)
			}
			switch from + ">" + to {
			case "CLOSED>OPEN":
				if initialRetries != threshold*2 || len(transitions) != 0 {
					t.Fatalf("circuit opened with %d retries for %d failures", initialRetries, threshold)
				}
			case "OPEN>HALF_OPEN":
				probeRetries = 0
			case "HALF_OPEN>OPEN":
				if probeRetries != 2 {
					t.Fatalf("failed probe used %d retries, want 2", probeRetries)
				}
			case "HALF_OPEN>CLOSED":
			default:
				t.Fatalf("unexpected circuit transition: %v", event)
			}
			state = to
			transitions = append(transitions, from+">"+to)
		}
	}
	if len(transitions) < 3 || state != "CLOSED" {
		t.Fatalf("unexpected breaker transitions: %v", transitions)
	}
	t.Logf("JSON log evidence: outage_retries=%d recovery_retries=%d transitions=%v", initialRetries, recoveryRetries, transitions)
}

func (h *harness) waitFlight() {
	h.t.Helper()
	eventually(h.t, 30*time.Second, "Flight gRPC readiness", func() (bool, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		ctx = metadata.AppendToOutgoingContext(ctx, "x-api-key", h.key)
		_, err := h.rpc.GetFlight(ctx, &pb.GetFlightRequest{Id: seedFlightID})
		return err == nil, err
	})
}

func TestRedisOutageFallsBackToPostgres(t *testing.T) {
	h := newHarness(t)
	master, err := h.redisMaster()
	if err != nil {
		t.Fatal(err)
	}
	replica := "redis-a"
	if master == replica {
		replica = "redis-b"
	}
	h.restoreContainers(master, replica)
	since := time.Now()
	h.mustCompose("stop", "--timeout", "3", replica, master)
	var flight flightView
	response := h.request("GET", "/flights/"+seedFlightID, nil, "")
	response.expect(t, 200, &flight)
	if flight.AvailableSeats != h.initial.AvailableSeats || flight.Price != money(h.initial.PriceMinor) {
		t.Fatalf("incorrect PostgreSQL fallback: %+v", flight)
	}
	h.request("GET", "/flights?origin=SVO&destination=LED&date=2030-04-01", nil, "").expect(t, 200, nil)
	cacheError := false
	for _, event := range h.events("flight", since) {
		cacheError = cacheError || event["msg"] == "cache_error"
	}
	if !cacheError {
		t.Fatal("Redis outage did not produce observable cache_error logs")
	}
	t.Logf("Both Redis nodes unavailable: PostgreSQL fallback returned correct flight in %s", response.elapsed)
	h.mustCompose("start", master, replica)
	eventually(t, 30*time.Second, "Redis master readiness", func() (bool, error) {
		output, err := h.compose("exec", "-T", master, "redis-cli", "ping")
		return output == "PONG", err
	})
}

func TestRedisSentinelFailoverRejectsStaleReplicaData(t *testing.T) {
	h := newHarness(t)
	master, err := h.redisMaster()
	if err != nil {
		t.Fatal(err)
	}
	replica := "redis-a"
	if replica == master {
		replica = "redis-b"
	}
	h.restoreContainers(master)
	key := "flight:" + seedFlightID
	h.getFlight()
	oldEntry := h.redis(master, "GET", key)
	if !json.Valid([]byte(oldEntry)) {
		t.Fatalf("missing cached flight before failover: %q", oldEntry)
	}
	eventually(t, 30*time.Second, "cached value to reach Redis replica", func() (bool, error) {
		value, err := h.compose("exec", "-T", "sentinel-1", "redis-cli", "--raw", "-h", replica, "GET", key)
		return value == oldEntry, err
	})
	h.mustCompose("stop", "--timeout", "3", master)
	newPrice := h.initial.PriceMinor + 27100
	h.updateFlight(newPrice, pb.FlightStatus_SCHEDULED)
	eventually(t, 60*time.Second, "Sentinel to promote the former replica", func() (bool, error) {
		current, err := h.redisMaster()
		return current == replica, err
	})
	if result := h.redis(replica, "SET", key, oldEntry, "EX", "300"); result != "OK" {
		t.Fatalf("could not reproduce stale replica cache entry: %s", result)
	}
	if got := h.getFlight().PriceMinor; got != newPrice {
		t.Fatalf("promoted replica served stale price=%d want=%d", got, newPrice)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var revision int64
	if err := h.flightDB.QueryRow(ctx, "SELECT revision FROM cache_revision WHERE id=1").Scan(&revision); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "Flight client to refill the promoted Redis master", func() (bool, error) {
		callContext, callCancel := h.rpcContext()
		defer callCancel()
		if _, err := h.rpc.GetFlight(callContext, &pb.GetFlightRequest{Id: seedFlightID}); err != nil {
			return false, err
		}
		value, err := h.compose("exec", "-T", "sentinel-1", "redis-cli", "--raw", "-h", replica, "GET", key)
		if err != nil {
			return false, err
		}
		var entry struct {
			Revision int64 `json:"revision"`
		}
		if err := json.Unmarshal([]byte(value), &entry); err != nil {
			return false, err
		}
		return entry.Revision == revision, nil
	})
	var flight flightView
	h.request("GET", "/flights/"+seedFlightID, nil, "").expect(t, 200, &flight)
	if flight.Price != money(newPrice) {
		t.Fatalf("REST served stale price after failover: %+v", flight)
	}
	h.mustCompose("start", master)
	eventually(t, 45*time.Second, "former master to rejoin as a replica", func() (bool, error) {
		info, err := h.compose("exec", "-T", master, "redis-cli", "--raw", "INFO", "replication")
		return strings.Contains(info, "role:slave") && strings.Contains(info, "master_link_status:up"), err
	})
	t.Logf("Sentinel evidence: %s -> %s; stale pre-failover entry rejected; price=%d; former master rejoined as replica", master, replica, newPrice)
}
