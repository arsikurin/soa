package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"flightbooking/internal/app"
	"flightbooking/internal/flight"
	"flightbooking/internal/rpc/pb"
	"flightbooking/internal/transport"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
)

func main() {
	logger := app.Logger()
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := app.Healthcheck(app.Env("GRPC_ADDR", ":50051")); err != nil {
			os.Exit(1)
		}
		return
	}
	if err := run(logger); err != nil {
		logger.Error("flight_stopped", "error", err)
		os.Exit(1)
	}
}
func run(logger *slog.Logger) error {
	ctx, cancel := app.Context()
	defer cancel()
	key := os.Getenv("FLIGHT_API_KEY")
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("FLIGHT_API_KEY is required")
	}
	ttl, err := app.Duration("CACHE_TTL", 5*time.Minute)
	if err != nil {
		return err
	}
	if ttl < 5*time.Minute || ttl > 10*time.Minute {
		return fmt.Errorf("CACHE_TTL must be between 5m and 10m")
	}
	pool, err := app.Pool(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	cache := redis.NewFailoverClient(&redis.FailoverOptions{MasterName: app.Env("REDIS_MASTER", "flight-cache"), SentinelAddrs: strings.Split(app.Env("REDIS_SENTINELS", "localhost:26379"), ","), DialTimeout: 300 * time.Millisecond, ReadTimeout: 300 * time.Millisecond, WriteTimeout: 300 * time.Millisecond, MaxRetries: -1})
	defer cache.Close()
	server := grpc.NewServer(grpc.UnaryInterceptor(transport.AuthInterceptor(key)), grpc.MaxRecvMsgSize(1<<20))
	pb.RegisterFlightServiceServer(server, flight.New(pool, cache, logger, ttl))
	listener, err := net.Listen("tcp", app.Env("GRPC_ADDR", ":50051"))
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		logger.Info("grpc_listen", "address", listener.Addr().String())
		done <- server.Serve(listener)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		stopped := make(chan struct{})
		go func() { server.GracefulStop(); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(8 * time.Second):
			server.Stop()
		}
		return nil
	}
}
