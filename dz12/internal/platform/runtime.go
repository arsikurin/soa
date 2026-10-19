package platform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

const MaxRequestBytes = 1 << 20
const MaxResponseBytes = 4 << 20

func Env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
func OpenDB(ctx context.Context) (*pgxpool.Pool, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, errors.New("DATABASE_URL is required for a domain service")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 24
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "10000"
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "5000"
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	ping, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err = db.Ping(ping); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
func Dial(address, server string) (*grpc.ClientConn, error) {
	cfg, err := ClientTLS(Env("TLS_DIR", "/tls"), server)
	if err != nil {
		return nil, err
	}
	return grpc.NewClient(address, grpc.WithTransportCredentials(credentials.NewTLS(cfg)), grpc.WithDisableRetry(), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(MaxResponseBytes), grpc.MaxCallSendMsgSize(MaxRequestBytes)), grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		call, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return invoker(call, method, req, reply, cc, opts...)
	}))
}
func ServeGRPC(ctx context.Context, role string, logger *slog.Logger, register func(*grpc.Server)) error {
	cfg, err := ServerTLS(Env("TLS_DIR", "/tls"))
	if err != nil {
		return err
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(cfg)), grpc.MaxRecvMsgSize(MaxRequestBytes), grpc.MaxSendMsgSize(MaxResponseBytes), grpc.MaxConcurrentStreams(64), grpc.ChainUnaryInterceptor(Authorize(role), BoundRequests(8)))
	register(server)
	h := health.NewServer()
	h.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(server, h)
	addr := Env("GRPC_ADDR", ":8081")
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	logger.Info("service listening", "service", role, "address", addr, "transport", "grpc+mtls")
	select {
	case err = <-done:
		return err
	case <-ctx.Done():
		h.Shutdown()
		stopped := make(chan struct{})
		go func() { server.GracefulStop(); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(10 * time.Second):
			server.Stop()
			<-stopped
		}
		return nil
	}
}
func ServeHTTP(ctx context.Context, handler http.Handler, logger *slog.Logger) error {
	addr := Env("HTTP_ADDR", ":8080")
	server := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 35 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	logger.Info("gateway listening", "address", addr)
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := server.Shutdown(stop)
		if err != nil {
			_ = server.Close()
		}
		return err
	}
}
func Healthcheck(role string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if role == "gateway" {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8080/health", nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return fmt.Errorf("gateway health status %d", res.StatusCode)
		}
		return nil
	}
	cc, err := Dial("127.0.0.1:8081", role)
	if err != nil {
		return err
	}
	defer cc.Close()
	result, err := healthpb.NewHealthClient(cc).Check(ctx, &healthpb.HealthCheckRequest{})
	if err == nil && result.Status != healthpb.HealthCheckResponse_SERVING {
		return errors.New("service is not ready")
	}
	return err
}
