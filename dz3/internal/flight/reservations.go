package flight

import (
	"context"
	"errors"

	"flightbooking/internal/rpc/pb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Service) ReserveSeats(ctx context.Context, request *pb.ReserveSeatsRequest) (*pb.SeatReservation, error) {
	flightID, err := validID(request.GetFlightId())
	if err != nil {
		return nil, err
	}
	bookingID, err := validID(request.GetBookingId())
	if err != nil {
		return nil, err
	}
	if request.GetSeatCount() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "seat_count must be positive")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, s.databaseError(ctx, "reserve_begin", err)
	}
	defer rollback(tx)
	if err := lockBooking(ctx, tx, bookingID); err != nil {
		return nil, s.databaseError(ctx, "reserve_lock", err)
	}
	existing, err := loadReservation(ctx, tx, bookingID)
	if err == nil {
		if existing.FlightId != flightID || existing.SeatCount != request.SeatCount {
			return nil, status.Error(codes.AlreadyExists, "booking_id already has different reservation parameters")
		}
		if existing.Status != pb.ReservationStatus_ACTIVE {
			return nil, status.Error(codes.FailedPrecondition, "reservation has already been released")
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, s.databaseError(ctx, "reserve_lookup", err)
	}
	var fenced bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM reservation_fences WHERE booking_id=$1)", bookingID).Scan(&fenced); err != nil {
		return nil, s.databaseError(ctx, "reserve_fence", err)
	}
	if fenced {
		return nil, status.Error(codes.FailedPrecondition, "booking_id has already been released")
	}
	flight, err := loadFlight(ctx, tx, flightID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "flight not found")
	}
	if err != nil {
		return nil, s.databaseError(ctx, "reserve_flight", err)
	}
	if flight.Status != pb.FlightStatus_SCHEDULED {
		return nil, status.Error(codes.FailedPrecondition, "flight is not scheduled")
	}
	if flight.AvailableSeats < request.SeatCount {
		return nil, status.Error(codes.ResourceExhausted, "not enough available seats")
	}
	reservation := &pb.SeatReservation{Id: uuid.NewString(), BookingId: bookingID, FlightId: flightID, SeatCount: request.SeatCount, Status: pb.ReservationStatus_ACTIVE}
	if _, err := tx.Exec(ctx, "UPDATE flights SET available_seats=available_seats-$2 WHERE id=$1", flightID, request.SeatCount); err != nil {
		return nil, s.databaseError(ctx, "reserve_seats", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO seat_reservations (id, booking_id, flight_id, seat_count, status)
VALUES ($1, $2, $3, $4, 'ACTIVE')`, reservation.Id, bookingID, flightID, request.SeatCount); err != nil {
		return nil, s.databaseError(ctx, "reserve_insert", err)
	}
	if err := advanceRevision(ctx, tx); err != nil {
		return nil, s.databaseError(ctx, "reserve_revision", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, s.databaseError(ctx, "reserve_commit", err)
	}
	s.invalidate(ctx, flight)
	return reservation, nil
}

func (s *Service) ReleaseReservation(ctx context.Context, request *pb.ReleaseReservationRequest) (*pb.ReleaseReservationResponse, error) {
	bookingID, err := validID(request.GetBookingId())
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, s.databaseError(ctx, "release_begin", err)
	}
	defer rollback(tx)
	if err := lockBooking(ctx, tx, bookingID); err != nil {
		return nil, s.databaseError(ctx, "release_lock", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO reservation_fences (booking_id) VALUES ($1) ON CONFLICT DO NOTHING", bookingID); err != nil {
		return nil, s.databaseError(ctx, "release_fence", err)
	}
	reservation, err := loadReservation(ctx, tx, bookingID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, s.databaseError(ctx, "release_lookup", err)
	}
	if errors.Is(err, pgx.ErrNoRows) || reservation.Status != pb.ReservationStatus_ACTIVE {
		if err := tx.Commit(ctx); err != nil {
			return nil, s.databaseError(ctx, "release_commit", err)
		}
		return &pb.ReleaseReservationResponse{}, nil
	}
	flight, err := loadFlight(ctx, tx, reservation.FlightId, true)
	if err != nil {
		return nil, s.databaseError(ctx, "release_flight", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE flights SET available_seats=available_seats+$2 WHERE id=$1", reservation.FlightId, reservation.SeatCount); err != nil {
		return nil, s.databaseError(ctx, "release_seats", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE seat_reservations SET status='RELEASED', updated_at=now() WHERE id=$1", reservation.Id); err != nil {
		return nil, s.databaseError(ctx, "release_status", err)
	}
	if err := advanceRevision(ctx, tx); err != nil {
		return nil, s.databaseError(ctx, "release_revision", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, s.databaseError(ctx, "release_commit", err)
	}
	s.invalidate(ctx, flight)
	return &pb.ReleaseReservationResponse{Released: true}, nil
}
