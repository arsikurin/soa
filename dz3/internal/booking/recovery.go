package booking

import (
	"context"
	"errors"
	"time"

	"flightbooking/internal/rpc/pb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Service) Recover(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, "SELECT booking_id FROM booking_operations WHERE state='PENDING' ORDER BY updated_at, booking_id")
	if err != nil {
		return s.databaseError(err)
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return s.databaseError(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return s.databaseError(err)
	}
	var failures []error
	for _, id := range ids {
		if err := s.recoverOne(ctx, id); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *Service) recoverOne(ctx context.Context, id uuid.UUID) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	lock, err := s.lock(ctx, id, true)
	if err != nil {
		return s.databaseError(err)
	}
	if lock == nil {
		return nil
	}
	defer lock.release()
	op, err := readOperation(ctx, lock.conn, id)
	if err != nil {
		return s.databaseError(err)
	}
	if op.State != "PENDING" {
		return nil
	}
	_, err = s.recoverLocked(ctx, lock, op)
	if err != nil {
		s.logger.Warn("booking recovery failed", "booking_id", id, "kind", op.Kind, "error", err)
	}
	return err
}

func (s *Service) recoverLocked(ctx context.Context, lock *operationLock, op *operation) (*Booking, error) {
	booking, err := findBooking(ctx, lock.conn, op.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if op.Kind == "CREATE" && err == nil {
		_, err = lock.conn.Exec(ctx, "UPDATE booking_operations SET state='DONE', updated_at=now() WHERE booking_id=$1", op.ID)
		return booking, err
	}
	if op.Kind == "CANCEL" && err != nil {
		return nil, err
	}
	if op.State == "FAILED" {
		return nil, nil
	}
	if _, err := s.flights.ReleaseReservation(ctx, &pb.ReleaseReservationRequest{BookingId: op.ID.String()}); err != nil {
		return nil, err
	}
	if op.Kind == "CANCEL" {
		return s.commitCancellation(ctx, lock)
	}
	_, err = lock.conn.Exec(ctx, "UPDATE booking_operations SET state='FAILED', updated_at=now() WHERE booking_id=$1", op.ID)
	if err == nil {
		s.logger.Info("booking reservation compensated", "booking_id", op.ID)
	}
	return nil, err
}

func (s *Service) RunRecovery(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := s.Recover(ctx); err != nil && ctx.Err() == nil {
			s.logger.Warn("booking recovery pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func pendingError(id uuid.UUID) error {
	result, err := status.New(codes.Unavailable, "booking operation pending recovery").WithDetails(&errdetails.ErrorInfo{Reason: "OPERATION_PENDING", Domain: "booking", Metadata: map[string]string{"booking_id": id.String()}})
	if err != nil {
		return status.Error(codes.Unavailable, "booking operation pending recovery")
	}
	return result.Err()
}
