package booking

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const bookingColumns = "id, user_id, flight_id, passenger_name, passenger_email, seat_count, total_price_minor, status, created_at, updated_at"

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type operation struct {
	ID             uuid.UUID
	Input          CreateInput
	Kind           string
	State          string
	FailureCode    int32
	FailureMessage string
}

type operationLock struct {
	conn *pgxpool.Conn
	id   uuid.UUID
}

func (s *Service) lock(ctx context.Context, id uuid.UUID, try bool) (*operationLock, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	query := "SELECT pg_advisory_lock(hashtextextended($1, 0))"
	if try {
		query = "SELECT pg_try_advisory_lock(hashtextextended($1, 0))"
		var acquired bool
		err = conn.QueryRow(ctx, query, id.String()).Scan(&acquired)
		if err == nil && !acquired {
			conn.Release()
			return nil, nil
		}
	} else {
		_, err = conn.Exec(ctx, query, id.String())
	}
	if err != nil {
		_ = conn.Conn().Close(context.Background())
		conn.Release()
		return nil, err
	}
	return &operationLock{conn: conn, id: id}, nil
}

func (l *operationLock) release() {
	if l.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := l.conn.Exec(ctx, "SELECT pg_advisory_unlock(hashtextextended($1, 0))", l.id.String()); err != nil {
		_ = l.conn.Conn().Close(ctx)
	}
	l.conn.Release()
	l.conn = nil
}

func readOperation(ctx context.Context, query rowQuerier, id uuid.UUID) (*operation, error) {
	op := &operation{ID: id}
	err := query.QueryRow(ctx, `SELECT user_id, flight_id, passenger_name, passenger_email, seat_count, kind, state, failure_code, failure_message FROM booking_operations WHERE booking_id=$1`, id).Scan(
		&op.Input.UserID, &op.Input.FlightID, &op.Input.PassengerName, &op.Input.PassengerEmail, &op.Input.SeatCount, &op.Kind, &op.State, &op.FailureCode, &op.FailureMessage,
	)
	return op, err
}

func scanBooking(row pgx.Row) (*Booking, error) {
	booking := new(Booking)
	err := row.Scan(&booking.ID, &booking.UserID, &booking.FlightID, &booking.PassengerName, &booking.PassengerEmail, &booking.SeatCount, &booking.TotalPriceMinor, &booking.Status, &booking.CreatedAt, &booking.UpdatedAt)
	return booking, err
}

func findBooking(ctx context.Context, query rowQuerier, id uuid.UUID) (*Booking, error) {
	return scanBooking(query.QueryRow(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE id=$1", id))
}

func (s *Service) databaseError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return status.Error(codes.NotFound, "booking not found")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	s.logger.Error("booking database operation failed", "error", err)
	return status.Error(codes.Unavailable, "booking storage unavailable")
}
