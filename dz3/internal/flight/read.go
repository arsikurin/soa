package flight

import (
	"context"
	"errors"
	"time"

	"flightbooking/internal/rpc/pb"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Service) GetFlight(ctx context.Context, request *pb.GetFlightRequest) (*pb.Flight, error) {
	id, err := validID(request.GetId())
	if err != nil {
		return nil, err
	}
	key := "flight:" + id
	result := new(pb.Flight)
	if hit, err := s.cached(ctx, key, result); hit || err != nil {
		return result, err
	}
	revision, err := s.readSnapshot(ctx, func(tx pgx.Tx) error {
		var err error
		result, err = loadFlight(ctx, tx, id, false)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "flight not found")
	}
	if err != nil {
		return nil, s.databaseError(ctx, "get_flight", err)
	}
	s.fillCache(ctx, key, revision, result)
	return result, nil
}

func (s *Service) SearchFlights(ctx context.Context, request *pb.SearchFlightsRequest) (*pb.SearchFlightsResponse, error) {
	origin, destination := request.GetOrigin(), request.GetDestination()
	if !validAirport(origin) || !validAirport(destination) || origin == destination {
		return nil, status.Error(codes.InvalidArgument, "distinct uppercase IATA airport codes are required")
	}
	day := "all"
	var departureDay time.Time
	if request.Date != nil {
		var err error
		departureDay, err = time.Parse("2006-01-02", request.GetDate())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "date must be YYYY-MM-DD")
		}
		day = request.GetDate()
	}
	key := "search:" + origin + ":" + destination + ":" + day
	result := &pb.SearchFlightsResponse{Flights: []*pb.Flight{}}
	if hit, err := s.cached(ctx, key, result); hit || err != nil {
		return result, err
	}
	revision, err := s.readSnapshot(ctx, func(tx pgx.Tx) error {
		query := "SELECT " + flightColumns + " FROM " + flightTables + " WHERE f.origin=$1 AND f.destination=$2 AND f.status='SCHEDULED'"
		args := []any{origin, destination}
		if !departureDay.IsZero() {
			query += " AND f.departure_at >= $3 AND f.departure_at < $4"
			args = append(args, departureDay, departureDay.AddDate(0, 0, 1))
		}
		query += " ORDER BY f.departure_at, f.id"
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			flight, err := scanFlight(rows)
			if err != nil {
				return err
			}
			result.Flights = append(result.Flights, flight)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, s.databaseError(ctx, "search_flights", err)
	}
	s.fillCache(ctx, key, revision, result)
	return result, nil
}
