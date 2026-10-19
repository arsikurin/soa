package main

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"marketplace/internal/auth"
	"marketplace/internal/catalog"
	"marketplace/internal/httpapi"
	"marketplace/internal/orders"
	"marketplace/internal/platform"
	"marketplace/internal/rpc"
)

func run(logger *slog.Logger) error {
	role := platform.Env("SERVICE_ROLE", "gateway")
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		return platform.Healthcheck(role)
	}
	if role == "certificates" {
		return platform.GenerateCertificates(platform.Env("CERT_ROOT", "/certs"))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if role == "gateway" {
		tokens, err := auth.NewTokens(os.Getenv("JWT_SECRET"))
		if err != nil {
			return err
		}
		users, err := platform.Dial(platform.Env("USERS_ADDR", "users:8081"), "users")
		if err != nil {
			return err
		}
		defer users.Close()
		catalog, err := platform.Dial(platform.Env("CATALOG_ADDR", "catalog:8081"), "catalog")
		if err != nil {
			return err
		}
		defer catalog.Close()
		orders, err := platform.Dial(platform.Env("ORDERS_ADDR", "orders:8081"), "orders")
		if err != nil {
			return err
		}
		defer orders.Close()
		origins := strings.Split(os.Getenv("CORS_ALLOWED_ORIGINS"), ",")
		h, err := httpapi.New(rpc.NewCatalogClient(catalog), rpc.NewOrdersClient(orders), rpc.NewUsersClient(users), tokens, logger, origins...)
		if err != nil {
			return err
		}
		return platform.ServeHTTP(ctx, h, logger)
	}
	if role != "users" && role != "catalog" && role != "orders" {
		return errors.New("SERVICE_ROLE must be gateway, users, catalog, orders or certificates")
	}
	db, err := platform.OpenDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	if role == "catalog" || role == "orders" {
		var capacity int
		if err = db.QueryRow(ctx, "SELECT current_setting('max_prepared_transactions')::integer").Scan(&capacity); err != nil {
			return err
		}
		if capacity < 1 {
			return errors.New("max_prepared_transactions must be enabled")
		}
	}
	switch role {
	case "users":
		tokens, err := auth.NewTokens(os.Getenv("JWT_SECRET"))
		if err != nil {
			return err
		}
		svc := auth.NewService(db, tokens)
		start, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err = svc.BootstrapAdmin(start, os.Getenv("BOOTSTRAP_ADMIN_EMAIL"), os.Getenv("BOOTSTRAP_ADMIN_PASSWORD")); err != nil {
			return err
		}
		return platform.ServeGRPC(ctx, role, logger, func(s *grpc.Server) { rpc.RegisterUsersServer(s, svc) })
	case "catalog":
		users, err := platform.Dial(platform.Env("USERS_ADDR", "users:8081"), "users")
		if err != nil {
			return err
		}
		defer users.Close()
		svc := catalog.NewService(db, rpc.NewUsersClient(users))
		return platform.ServeGRPC(ctx, role, logger, func(s *grpc.Server) { rpc.RegisterCatalogServer(s, svc) })
	case "orders":
		minutes, err := strconv.ParseFloat(platform.Env("ORDER_RATE_LIMIT_MINUTES", "1"), 64)
		if err != nil || math.IsNaN(minutes) || math.IsInf(minutes, 0) || minutes < 0 || minutes > 1440 {
			return errors.New("ORDER_RATE_LIMIT_MINUTES must be between 0 and 1440")
		}
		catalog, err := platform.Dial(platform.Env("CATALOG_ADDR", "catalog:8081"), "catalog")
		if err != nil {
			return err
		}
		defer catalog.Close()
		svc := orders.NewService(db, rpc.NewCatalogClient(catalog), time.Duration(minutes*float64(time.Minute)))
		recoveryCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); svc.RunRecovery(recoveryCtx) }()
		defer func() { cancel(); <-done }()
		return platform.ServeGRPC(ctx, role, logger, func(s *grpc.Server) { rpc.RegisterOrdersServer(s, svc) })
	}
	return nil
}
func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("service failed", "error", err.Error())
		os.Exit(1)
	}
}
