package flight

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"time"

	"flightbooking/internal/rpc/pb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Service struct {
	pb.UnimplementedFlightServiceServer
	pool   *pgxpool.Pool
	cache  *redis.Client
	logger *slog.Logger
	ttl    time.Duration
}

func New(pool *pgxpool.Pool, cache *redis.Client, logger *slog.Logger, ttl time.Duration) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &Service{pool: pool, cache: cache, logger: logger, ttl: ttl}
}

func validID(value string) (string, error) {
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		return "", status.Error(codes.InvalidArgument, "a nonzero UUID is required")
	}
	return id.String(), nil
}

func validAirport(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, letter := range value {
		if letter < 'A' || letter > 'Z' {
			return false
		}
	}
	return true
}

func (s *Service) databaseError(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	s.logger.ErrorContext(ctx, "database_error", "operation", operation, "error", err)
	var networkError net.Error
	var postgresError *pgconn.PgError
	if errors.As(err, &networkError) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || pgconn.SafeToRetry(err) || errors.Is(err, pgx.ErrTxCommitRollback) {
		return status.Error(codes.Unavailable, "flight database is unavailable")
	}
	if errors.As(err, &postgresError) && (strings.HasPrefix(postgresError.Code, "08") || strings.HasPrefix(postgresError.Code, "53") || postgresError.Code == "57P01" || postgresError.Code == "40001" || postgresError.Code == "40P01") {
		return status.Error(codes.Unavailable, "flight database is unavailable")
	}
	return status.Error(codes.Internal, "flight database operation failed")
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
