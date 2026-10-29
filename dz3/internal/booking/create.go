package booking

import (
	"context"
	"errors"
	"math"
	"time"

	"flightbooking/internal/rpc/pb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Service) Create(ctx context.Context, input CreateInput, id uuid.UUID) (*Booking, error) {
	if err := validateInput(input, id); err != nil {
		return nil, err
	}
	lock, err := s.lock(ctx, id, false)
	if err != nil {
		return nil, s.databaseError(err)
	}
	defer lock.release()
	op, err := readOperation(ctx, lock.conn, id)
	if err == nil {
		return s.resumeCreate(ctx, lock, op, input)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, s.databaseError(err)
	}
	_, err = lock.conn.Exec(ctx, `INSERT INTO booking_operations (booking_id, user_id, flight_id, passenger_name, passenger_email, seat_count, kind, state) VALUES ($1,$2,$3,$4,$5,$6,'CREATE','PENDING')`, id, input.UserID, input.FlightID, input.PassengerName, input.PassengerEmail, input.SeatCount)
	if err != nil {
		return nil, s.databaseError(err)
	}
	flight, err := s.flights.GetFlight(ctx, &pb.GetFlightRequest{Id: input.FlightID.String()})
	if err != nil {
		return s.failBeforeReserve(lock, err)
	}
	if flight.GetPriceMinor() <= 0 || flight.GetPriceMinor() > math.MaxInt64/int64(input.SeatCount) {
		return s.failBeforeReserve(lock, status.Error(codes.FailedPrecondition, "flight price cannot be represented"))
	}
	_, err = s.flights.ReserveSeats(ctx, &pb.ReserveSeatsRequest{FlightId: input.FlightID.String(), BookingId: id.String(), SeatCount: input.SeatCount})
	if err != nil {
		return s.failCreate(lock, err)
	}
	booking, err := s.commitBooking(ctx, lock, input, flight.PriceMinor*int64(input.SeatCount))
	if err != nil {
		return s.failCreate(lock, s.databaseError(err))
	}
	return booking, nil
}

func (s *Service) failBeforeReserve(lock *operationLock, cause error) (*Booking, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		cause = status.FromContextError(cause).Err()
	}
	_, err := lock.conn.Exec(ctx, "UPDATE booking_operations SET state='FAILED', failure_code=$2, failure_message=$3, updated_at=now() WHERE booking_id=$1 AND state='PENDING'", lock.id, int32(status.Code(cause)), status.Convert(cause).Message())
	if err != nil {
		return nil, pendingError(lock.id)
	}
	return nil, cause
}

func (s *Service) resumeCreate(ctx context.Context, lock *operationLock, op *operation, input CreateInput) (*Booking, error) {
	if op.Input != input {
		return nil, status.Error(codes.AlreadyExists, "idempotency key already used for a different request")
	}
	booking, err := findBooking(ctx, lock.conn, op.ID)
	if err == nil {
		return booking, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, s.databaseError(err)
	}
	if op.State == "PENDING" {
		if _, err := s.recoverLocked(ctx, lock, op); err != nil {
			return nil, pendingError(op.ID)
		}
	}
	return nil, status.Error(codes.Code(op.FailureCode), op.FailureMessage)
}

func (s *Service) commitBooking(ctx context.Context, lock *operationLock, input CreateInput, total int64) (*Booking, error) {
	tx, err := lock.conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	booking, err := scanBooking(tx.QueryRow(ctx, `INSERT INTO bookings (id, user_id, flight_id, passenger_name, passenger_email, seat_count, total_price_minor, status) VALUES ($1,$2,$3,$4,$5,$6,$7,'CONFIRMED') RETURNING `+bookingColumns, lock.id, input.UserID, input.FlightID, input.PassengerName, input.PassengerEmail, input.SeatCount, total))
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "UPDATE booking_operations SET state='DONE', updated_at=now() WHERE booking_id=$1", lock.id); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return booking, nil
}

func (s *Service) failCreate(lock *operationLock, cause error) (*Booking, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		cause = status.FromContextError(cause).Err()
	}
	_, err := lock.conn.Exec(ctx, "UPDATE booking_operations SET failure_code=$2, failure_message=$3, updated_at=now() WHERE booking_id=$1 AND state='PENDING'", lock.id, int32(status.Code(cause)), status.Convert(cause).Message())
	if err != nil {
		s.logger.Error("recording booking failure failed", "booking_id", lock.id, "error", err)
	}
	id := lock.id
	lock.release()
	lock, err = s.lock(ctx, id, false)
	if err != nil {
		return nil, pendingError(id)
	}
	defer lock.release()
	op, err := readOperation(ctx, lock.conn, id)
	if err != nil {
		return nil, pendingError(id)
	}
	booking, err := s.recoverLocked(ctx, lock, op)
	if err != nil {
		s.logger.Warn("booking compensation pending", "booking_id", id, "error", err)
		return nil, pendingError(id)
	}
	if booking != nil {
		return booking, nil
	}
	return nil, cause
}
