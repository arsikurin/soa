package booking

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Service) Cancel(ctx context.Context, id uuid.UUID) (*Booking, error) {
	if id == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "booking_id is required")
	}
	lock, err := s.lock(ctx, id, false)
	if err != nil {
		return nil, s.databaseError(err)
	}
	defer lock.release()
	booking, err := findBooking(ctx, lock.conn, id)
	if err != nil {
		return nil, s.databaseError(err)
	}
	if booking.Status == "CANCELLED" {
		return booking, nil
	}
	_, err = lock.conn.Exec(ctx, "UPDATE booking_operations SET kind='CANCEL', state='PENDING', updated_at=now() WHERE booking_id=$1", id)
	if err != nil {
		return nil, s.databaseError(err)
	}
	booking, err = s.recoverLocked(ctx, lock, &operation{ID: id, Kind: "CANCEL", State: "PENDING"})
	if err != nil {
		s.logger.Warn("booking cancellation pending", "booking_id", id, "error", err)
		return nil, pendingError(id)
	}
	return booking, nil
}

func (s *Service) commitCancellation(ctx context.Context, lock *operationLock) (*Booking, error) {
	tx, err := lock.conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	booking, err := scanBooking(tx.QueryRow(ctx, "UPDATE bookings SET status='CANCELLED', updated_at=now() WHERE id=$1 RETURNING "+bookingColumns, lock.id))
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
