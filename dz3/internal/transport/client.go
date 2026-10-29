package transport

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func NewClientInterceptor(cfg Config, logger *slog.Logger) (grpc.UnaryClientInterceptor, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}
	breaker := &circuitBreaker{
		state:       closed,
		threshold:   cfg.FailureThreshold,
		window:      cfg.FailureWindow,
		openTimeout: cfg.OpenTimeout,
		logger:      logger,
	}
	return func(ctx context.Context, method string, request, response any, conn *grpc.ClientConn, invoker grpc.UnaryInvoker, options ...grpc.CallOption) error {
		if err := ctx.Err(); err != nil {
			return status.FromContextError(err).Err()
		}
		generation, err := breaker.acquire()
		if err != nil {
			return err
		}
		md, _ := metadata.FromOutgoingContext(ctx)
		md = md.Copy()
		md.Set("x-api-key", cfg.APIKey)
		ctx = metadata.NewOutgoingContext(ctx, md)
		err = invokeWithRetry(ctx, method, request, response, conn, invoker, cfg, logger, options...)
		breaker.complete(generation, err, ctx.Err())
		return err
	}, nil
}

func invokeWithRetry(ctx context.Context, method string, request, response any, conn *grpc.ClientConn, invoker grpc.UnaryInvoker, cfg Config, logger *slog.Logger, options ...grpc.CallOption) error {
	backoff := cfg.InitialBackoff
	for attempt := 1; attempt <= 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return status.FromContextError(err).Err()
		}
		attemptContext, cancel := context.WithTimeout(ctx, cfg.AttemptTimeout)
		err := invoker(attemptContext, method, request, response, conn, options...)
		cancel()
		if parentErr := ctx.Err(); parentErr != nil {
			return status.FromContextError(parentErr).Err()
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			err = status.FromContextError(err).Err()
		}
		if !transportFailure(err) || attempt == 3 {
			return err
		}
		logger.WarnContext(ctx, "retrying RPC", "event", "rpc_retry", "method", method, "attempt", attempt+1, "code", status.Code(err).String(), "backoff_ms", backoff.Milliseconds())
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return status.FromContextError(ctx.Err()).Err()
		case <-timer.C:
		}
		if attempt == 1 {
			backoff *= 2
		}
	}
	return nil
}

func transportFailure(err error) bool {
	code := status.Code(err)
	return code == codes.Unavailable || code == codes.DeadlineExceeded
}
