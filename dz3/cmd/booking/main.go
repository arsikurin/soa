package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"flightbooking/internal/app"
	"flightbooking/internal/booking"
	"flightbooking/internal/httpapi"
	"flightbooking/internal/rpc/pb"
	"flightbooking/internal/transport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	logger := app.Logger()
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := app.Healthcheck(app.Env("HTTP_ADDR", ":8090")); err != nil {
			os.Exit(1)
		}
		return
	}
	if err := run(logger); err != nil {
		logger.Error("booking_stopped", "error", err)
		os.Exit(1)
	}
}
func run(logger *slog.Logger) error {
	ctx, cancel := app.Context()
	defer cancel()
	cfg, err := transport.ConfigFromEnv()
	if err != nil {
		return err
	}
	interceptor, err := transport.NewClientInterceptor(cfg, logger)
	if err != nil {
		return err
	}
	interval, err := app.Duration("RECOVERY_INTERVAL", time.Second)
	if err != nil {
		return err
	}
	pool, err := app.Pool(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	conn, err := grpc.NewClient(app.Env("FLIGHT_ADDR", "localhost:50061"), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDisableRetry(), grpc.WithUnaryInterceptor(interceptor))
	if err != nil {
		return err
	}
	defer conn.Close()
	flights := pb.NewFlightServiceClient(conn)
	service := booking.New(pool, flights, logger)
	workerCtx, stopWorker := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); service.RunRecovery(workerCtx, interval) }()
	defer func() { stopWorker(); <-workerDone }()
	handler, err := httpapi.New(service, flights, logger)
	if err != nil {
		return err
	}
	return app.ServeHTTP(ctx, app.Env("HTTP_ADDR", ":8090"), handler, logger)
}
