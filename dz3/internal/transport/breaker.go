package transport

import (
	"log/slog"
	"sync"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type circuitState string

const (
	closed   circuitState = "CLOSED"
	open     circuitState = "OPEN"
	halfOpen circuitState = "HALF_OPEN"
)

type circuitBreaker struct {
	mu          sync.Mutex
	state       circuitState
	generation  uint64
	probeActive bool
	failures    []time.Time
	openedAt    time.Time
	threshold   uint32
	window      time.Duration
	openTimeout time.Duration
	logger      *slog.Logger
}

func (b *circuitBreaker) acquire() (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == open {
		if time.Since(b.openedAt) < b.openTimeout {
			return 0, circuitOpenError()
		}
		b.transition(halfOpen)
	}
	if b.state == halfOpen {
		if b.probeActive {
			return 0, circuitOpenError()
		}
		b.probeActive = true
	}
	return b.generation, nil
}

func (b *circuitBreaker) complete(generation uint64, err, parentErr error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if generation != b.generation {
		return
	}
	if parentErr != nil {
		if b.state == halfOpen {
			b.probeActive = false
		}
		return
	}
	if !transportFailure(err) {
		b.failures = nil
		if b.state == halfOpen {
			b.transition(closed)
		}
		return
	}
	now := time.Now()
	if b.state == halfOpen {
		b.openedAt = now
		b.transition(open)
		return
	}
	first := 0
	for first < len(b.failures) && now.Sub(b.failures[first]) >= b.window {
		first++
	}
	b.failures = append(b.failures[first:], now)
	if uint64(len(b.failures)) >= uint64(b.threshold) {
		b.openedAt = now
		b.transition(open)
	}
}

func (b *circuitBreaker) transition(next circuitState) {
	previous := b.state
	b.state = next
	b.generation++
	b.probeActive = false
	b.logger.Info("circuit state changed", "event", "circuit_state_change", "from", string(previous), "to", string(next))
}

func circuitOpenError() error {
	err, detailErr := status.New(codes.Unavailable, "flight service circuit is open").WithDetails(&errdetails.ErrorInfo{
		Reason: "CIRCUIT_OPEN",
		Domain: "flightbooking.transport",
	})
	if detailErr != nil {
		return status.Error(codes.Unavailable, "flight service circuit is open")
	}
	return err.Err()
}
