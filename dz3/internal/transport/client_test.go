package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func testConfig() Config {
	cfg := DefaultConfig()
	cfg.APIKey = "secret"
	cfg.AttemptTimeout = 25 * time.Millisecond
	cfg.InitialBackoff = time.Millisecond
	cfg.OpenTimeout = 20 * time.Millisecond
	cfg.FailureWindow = time.Second
	return cfg
}

func testInterceptor(t *testing.T, cfg Config) grpc.UnaryClientInterceptor {
	t.Helper()
	interceptor, err := NewClientInterceptor(cfg, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return interceptor
}

func invoke(ctx context.Context, interceptor grpc.UnaryClientInterceptor, invoker grpc.UnaryInvoker) error {
	return interceptor(ctx, "/flight.v1.FlightService/GetFlight", nil, nil, nil, invoker)
}

func TestRetryOnlyTransportFailuresAndNeverMoreThanThreeAttempts(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{
		{codes.OK, 1}, {codes.Canceled, 1}, {codes.Unknown, 1}, {codes.InvalidArgument, 1},
		{codes.DeadlineExceeded, 3}, {codes.NotFound, 1}, {codes.AlreadyExists, 1},
		{codes.PermissionDenied, 1}, {codes.ResourceExhausted, 1}, {codes.FailedPrecondition, 1},
		{codes.Aborted, 1}, {codes.OutOfRange, 1}, {codes.Unimplemented, 1}, {codes.Internal, 1},
		{codes.Unavailable, 3}, {codes.DataLoss, 1}, {codes.Unauthenticated, 1},
	} {
		t.Run(tc.code.String(), func(t *testing.T) {
			calls := 0
			err := invoke(context.Background(), testInterceptor(t, testConfig()), func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
				calls++
				return status.Error(tc.code, "test")
			})
			if status.Code(err) != tc.code || calls != tc.want {
				t.Fatalf("code=%s calls=%d, want code=%s calls=%d", status.Code(err), calls, tc.code, tc.want)
			}
		})
	}
}

func TestRetryStopsAfterSuccessAndSetsCredentials(t *testing.T) {
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("x-api-key", "old", "request-id", "123"))
	calls := 0
	err := invoke(ctx, testInterceptor(t, testConfig()), func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		calls++
		md, _ := metadata.FromOutgoingContext(ctx)
		if keys := md.Get("x-api-key"); len(keys) != 1 || keys[0] != "secret" || md.Get("request-id")[0] != "123" {
			t.Fatalf("incorrect metadata: %v", md)
		}
		if calls == 1 {
			return status.Error(codes.Unavailable, "temporary")
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("got error=%v calls=%d", err, calls)
	}
	md, _ := metadata.FromOutgoingContext(ctx)
	if md.Get("x-api-key")[0] != "old" {
		t.Fatal("caller metadata mutated")
	}
}

func TestRetryUsesIndependentAttemptTimeouts(t *testing.T) {
	interceptor := testInterceptor(t, testConfig())
	var deadlines []time.Time
	err := invoke(context.Background(), interceptor, func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("attempt missing deadline")
		}
		deadlines = append(deadlines, deadline)
		<-ctx.Done()
		return ctx.Err()
	})
	if status.Code(err) != codes.DeadlineExceeded || len(deadlines) != 3 {
		t.Fatalf("got error=%v deadlines=%v", err, deadlines)
	}
	if !deadlines[1].After(deadlines[0]) || !deadlines[2].After(deadlines[1]) {
		t.Fatalf("attempts reused expired deadline: %v", deadlines)
	}
}

func TestParentCancellationStopsDuringBackoff(t *testing.T) {
	cfg := testConfig()
	cfg.InitialBackoff = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	started := time.Now()
	err := invoke(ctx, testInterceptor(t, cfg), func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		calls++
		time.AfterFunc(10*time.Millisecond, cancel)
		return status.Error(codes.Unavailable, "temporary")
	})
	if status.Code(err) != codes.Canceled || calls != 1 || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("error=%v calls=%d elapsed=%s", err, calls, time.Since(started))
	}
}

func TestParentDeadlineBoundsAllAttempts(t *testing.T) {
	cfg := testConfig()
	cfg.AttemptTimeout = time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	calls := 0
	err := invoke(ctx, testInterceptor(t, cfg), func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		calls++
		<-ctx.Done()
		return status.FromContextError(ctx.Err()).Err()
	})
	if status.Code(err) != codes.DeadlineExceeded || calls != 1 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}

func TestCanceledContextDoesNotInvokeOrOpenCircuit(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 1
	interceptor := testInterceptor(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := invoke(ctx, interceptor, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		t.Fatal("invoked after cancellation")
		return nil
	})
	if status.Code(err) != codes.Canceled {
		t.Fatalf("got %v", err)
	}
	if err := invoke(context.Background(), interceptor, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return nil }); err != nil {
		t.Fatalf("cancellation opened circuit: %v", err)
	}
}

func TestDefaultBackoffIsExponential(t *testing.T) {
	cfg := DefaultConfig()
	cfg.APIKey = "secret"
	var attempts []time.Time
	err := invoke(context.Background(), testInterceptor(t, cfg), func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		attempts = append(attempts, time.Now())
		return status.Error(codes.Unavailable, "temporary")
	})
	if status.Code(err) != codes.Unavailable || len(attempts) != 3 {
		t.Fatalf("error=%v attempts=%d", err, len(attempts))
	}
	if attempts[1].Sub(attempts[0]) < 100*time.Millisecond || attempts[2].Sub(attempts[1]) < 200*time.Millisecond {
		t.Fatalf("incorrect backoff: %s, %s", attempts[1].Sub(attempts[0]), attempts[2].Sub(attempts[1]))
	}
}

func TestCircuitCountsLogicalCallsAndRejectsOpenWithoutNetwork(t *testing.T) {
	cfg := testConfig()
	cfg.OpenTimeout = time.Second
	interceptor := testInterceptor(t, cfg)
	calls := 0
	failing := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		calls++
		return status.Error(codes.Unavailable, "down")
	}
	for range 5 {
		if err := invoke(context.Background(), interceptor, failing); status.Code(err) != codes.Unavailable {
			t.Fatalf("got %v", err)
		}
	}
	err := invoke(context.Background(), interceptor, failing)
	assertCircuitOpen(t, err)
	if calls != 15 {
		t.Fatalf("got %d network calls, want 15", calls)
	}
}

func assertCircuitOpen(t *testing.T, err error) {
	t.Helper()
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", err)
	}
	for _, detail := range status.Convert(err).Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && info.Reason == "CIRCUIT_OPEN" {
			return
		}
	}
	t.Fatalf("missing CIRCUIT_OPEN detail: %v", err)
}

func TestBusinessResponseResetsConsecutiveFailureCount(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 2
	interceptor := testInterceptor(t, cfg)
	for _, code := range []codes.Code{codes.Unavailable, codes.NotFound, codes.Unavailable, codes.ResourceExhausted, codes.Unavailable, codes.OK} {
		calls := 0
		err := invoke(context.Background(), interceptor, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			calls++
			return status.Error(code, "response")
		})
		if status.Code(err) != code || calls == 0 {
			t.Fatalf("code=%s got error=%v calls=%d", code, err, calls)
		}
	}
}

func TestFailureWindowExpiresOlderErrors(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 2
	cfg.FailureWindow = 20 * time.Millisecond
	interceptor := testInterceptor(t, cfg)
	failing := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		return status.Error(codes.Unavailable, "down")
	}
	invoke(context.Background(), interceptor, failing)
	time.Sleep(30 * time.Millisecond)
	invoke(context.Background(), interceptor, failing)
	err := invoke(context.Background(), interceptor, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return nil })
	if err != nil {
		t.Fatalf("old error contributed to breaker threshold: %v", err)
	}
}

func TestHalfOpenAllowsOneLogicalProbe(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 1
	interceptor := testInterceptor(t, cfg)
	invoke(context.Background(), interceptor, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		return status.Error(codes.Unavailable, "down")
	})
	time.Sleep(30 * time.Millisecond)
	entered := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- invoke(context.Background(), interceptor, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			close(entered)
			<-release
			return status.Error(codes.NotFound, "reachable")
		})
	}()
	<-entered
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			err := invoke(context.Background(), interceptor, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
				t.Error("parallel half-open call reached server")
				return nil
			})
			assertCircuitOpen(t, err)
		})
	}
	wg.Wait()
	close(release)
	if err := <-result; status.Code(err) != codes.NotFound {
		t.Fatalf("probe returned %v", err)
	}
	if err := invoke(context.Background(), interceptor, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return nil }); err != nil {
		t.Fatalf("business response did not close circuit: %v", err)
	}
}

func TestHalfOpenTransportFailureReopensCircuit(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 1
	interceptor := testInterceptor(t, cfg)
	calls := 0
	failing := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		calls++
		return status.Error(codes.Unavailable, "down")
	}
	invoke(context.Background(), interceptor, failing)
	time.Sleep(30 * time.Millisecond)
	invoke(context.Background(), interceptor, failing)
	assertCircuitOpen(t, invoke(context.Background(), interceptor, failing))
	if calls != 6 {
		t.Fatalf("got %d calls, want 6", calls)
	}
}

func TestStaleInFlightSuccessCannotCloseNewlyOpenedCircuit(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 1
	cfg.OpenTimeout = time.Second
	interceptor := testInterceptor(t, cfg)
	entered := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- invoke(context.Background(), interceptor, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	invoke(context.Background(), interceptor, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		return status.Error(codes.Unavailable, "down")
	})
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	assertCircuitOpen(t, invoke(context.Background(), interceptor, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return nil }))
}

func TestParentDeadlineDuringAttemptDoesNotOpenCircuit(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 1
	cfg.AttemptTimeout = time.Second
	interceptor := testInterceptor(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := invoke(ctx, interceptor, func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		<-ctx.Done()
		return status.Error(codes.DeadlineExceeded, "caller deadline")
	})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("got %v", err)
	}
	if err := invoke(context.Background(), interceptor, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return nil }); err != nil {
		t.Fatalf("caller deadline opened breaker: %v", err)
	}
}

func TestCanceledHalfOpenProbeReleasesProbeSlot(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 1
	interceptor := testInterceptor(t, cfg)
	invoke(context.Background(), interceptor, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		return status.Error(codes.Unavailable, "down")
	})
	time.Sleep(30 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := invoke(ctx, interceptor, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		cancel()
		return status.Error(codes.Canceled, "caller canceled")
	})
	if status.Code(err) != codes.Canceled {
		t.Fatalf("got %v", err)
	}
	if err := invoke(context.Background(), interceptor, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return nil }); err != nil {
		t.Fatalf("canceled probe left circuit unavailable: %v", err)
	}
}

func TestStaleInFlightFailureCannotReopenRecoveredCircuit(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 1
	cfg.AttemptTimeout = time.Second
	interceptor := testInterceptor(t, cfg)
	entered := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		calls := 0
		result <- invoke(context.Background(), interceptor, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			calls++
			if calls == 1 {
				close(entered)
				<-release
			}
			return status.Error(codes.Unavailable, "old outage")
		})
	}()
	<-entered
	invoke(context.Background(), interceptor, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		return status.Error(codes.Unavailable, "down")
	})
	time.Sleep(30 * time.Millisecond)
	if err := invoke(context.Background(), interceptor, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return nil }); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; status.Code(err) != codes.Unavailable {
		t.Fatalf("got %v", err)
	}
	if err := invoke(context.Background(), interceptor, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return nil }); err != nil {
		t.Fatalf("stale failure reopened recovered circuit: %v", err)
	}
}

func TestRetryAndStateTransitionsEmitStructuredEvents(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 1
	var output bytes.Buffer
	interceptor, err := NewClientInterceptor(cfg, slog.New(slog.NewJSONHandler(&output, nil)))
	if err != nil {
		t.Fatal(err)
	}
	invoke(context.Background(), interceptor, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		return status.Error(codes.Unavailable, "down")
	})
	time.Sleep(30 * time.Millisecond)
	invoke(context.Background(), interceptor, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return nil })
	decoder := json.NewDecoder(&output)
	retries := 0
	var transitions []string
	for decoder.More() {
		var event map[string]any
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		switch event["event"] {
		case "rpc_retry":
			retries++
		case "circuit_state_change":
			transitions = append(transitions, event["from"].(string)+">"+event["to"].(string))
		}
	}
	if retries != 2 || len(transitions) != 3 || transitions[0] != "CLOSED>OPEN" || transitions[1] != "OPEN>HALF_OPEN" || transitions[2] != "HALF_OPEN>CLOSED" {
		t.Fatalf("retries=%d transitions=%v", retries, transitions)
	}
}

type transientHealthServer struct {
	grpc_health_v1.UnimplementedHealthServer
	calls atomic.Int32
}

func (s *transientHealthServer) Check(context.Context, *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	if s.calls.Add(1) < 3 {
		return nil, status.Error(codes.Unavailable, "recovering")
	}
	return &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_SERVING}, nil
}

func TestGRPCClientRetriesAuthenticatedRequestsOverBufconn(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer(grpc.UnaryInterceptor(AuthInterceptor("secret")))
	health := &transientHealthServer{}
	grpc_health_v1.RegisterHealthServer(server, health)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	cfg := testConfig()
	cfg.AttemptTimeout = time.Second
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithDisableRetry(), grpc.WithUnaryInterceptor(testInterceptor(t, cfg)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	response, err := grpc_health_v1.NewHealthClient(conn).Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	if err != nil || response.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING || health.calls.Load() != 3 {
		t.Fatalf("response=%v error=%v server calls=%d", response, err, health.calls.Load())
	}
}
