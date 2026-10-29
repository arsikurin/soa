package flight

import (
	"context"
	"errors"

	"flightbooking/internal/rpc/pb"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Service) UpdateFlight(ctx context.Context, request *pb.UpdateFlightRequest) (*pb.Flight, error) {
	id, err := validID(request.GetId())
	if err != nil {
		return nil, err
	}
	if request.GetPriceMinor() <= 0 || request.GetStatus() < pb.FlightStatus_SCHEDULED || request.GetStatus() > pb.FlightStatus_COMPLETED {
		return nil, status.Error(codes.InvalidArgument, "positive price_minor and valid flight status are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, s.databaseError(ctx, "update_begin", err)
	}
	defer rollback(tx)
	flight, err := loadFlight(ctx, tx, id, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "flight not found")
	}
	if err != nil {
		return nil, s.databaseError(ctx, "update_lookup", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE flights SET price_minor=$2, status=$3 WHERE id=$1", id, request.PriceMinor, request.Status.String()); err != nil {
		return nil, s.databaseError(ctx, "update_flight", err)
	}
	if err := advanceRevision(ctx, tx); err != nil {
		return nil, s.databaseError(ctx, "update_revision", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, s.databaseError(ctx, "update_commit", err)
	}
	flight.PriceMinor = request.PriceMinor
	flight.Status = request.Status
	s.invalidate(ctx, flight)
	return flight, nil
}
