package flight

import (
	"context"
	"time"

	"flightbooking/internal/rpc/pb"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const flightColumns = `f.id, f.flight_number, a.name, f.origin, f.destination,
f.departure_at, f.arrival_at, f.total_seats, f.available_seats, f.price_minor, f.status`

const flightTables = `flights f
JOIN flight_numbers n ON n.number = f.flight_number
JOIN airlines a ON a.id = n.airline_id`

type scanner interface {
	Scan(...any) error
}

func scanFlight(row scanner) (*pb.Flight, error) {
	result := new(pb.Flight)
	var departure, arrival time.Time
	var state string
	if err := row.Scan(&result.Id, &result.FlightNumber, &result.Airline, &result.Origin,
		&result.Destination, &departure, &arrival, &result.TotalSeats, &result.AvailableSeats,
		&result.PriceMinor, &state); err != nil {
		return nil, err
	}
	result.DepartureAt = timestamppb.New(departure)
	result.ArrivalAt = timestamppb.New(arrival)
	result.Status = pb.FlightStatus(pb.FlightStatus_value[state])
	return result, nil
}

func loadFlight(ctx context.Context, tx pgx.Tx, id string, lock bool) (*pb.Flight, error) {
	query := "SELECT " + flightColumns + " FROM " + flightTables + " WHERE f.id=$1"
	if lock {
		query += " FOR UPDATE OF f"
	}
	return scanFlight(tx.QueryRow(ctx, query, id))
}

func loadReservation(ctx context.Context, tx pgx.Tx, bookingID string) (*pb.SeatReservation, error) {
	result := new(pb.SeatReservation)
	var state string
	err := tx.QueryRow(ctx, `SELECT id, booking_id, flight_id, seat_count, status
FROM seat_reservations WHERE booking_id=$1 FOR UPDATE`, bookingID).
		Scan(&result.Id, &result.BookingId, &result.FlightId, &result.SeatCount, &state)
	if err != nil {
		return nil, err
	}
	result.Status = pb.ReservationStatus(pb.ReservationStatus_value[state])
	return result, nil
}

func lockBooking(ctx context.Context, tx pgx.Tx, bookingID string) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", bookingID)
	return err
}

func advanceRevision(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "UPDATE cache_revision SET revision=revision+1 WHERE id=1")
	return err
}

func (s *Service) readSnapshot(ctx context.Context, load func(pgx.Tx) error) (int64, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return 0, err
	}
	defer rollback(tx)
	var revision int64
	if err := tx.QueryRow(ctx, "SELECT revision FROM cache_revision WHERE id=1").Scan(&revision); err != nil {
		return 0, err
	}
	if err := load(tx); err != nil {
		return 0, err
	}
	return revision, tx.Commit(ctx)
}
