package booking

import (
	"context"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"flightbooking/internal/rpc/pb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type CreateInput struct {
	UserID         uuid.UUID
	FlightID       uuid.UUID
	PassengerName  string
	PassengerEmail string
	SeatCount      int32
}

type Booking struct {
	ID              uuid.UUID
	UserID          uuid.UUID
	FlightID        uuid.UUID
	PassengerName   string
	PassengerEmail  string
	Status          string
	SeatCount       int32
	TotalPriceMinor int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type Service struct {
	pool    *pgxpool.Pool
	flights pb.FlightServiceClient
	logger  *slog.Logger
}

func New(pool *pgxpool.Pool, flights pb.FlightServiceClient, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{pool: pool, flights: flights, logger: logger}
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Booking, error) {
	if id == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "booking_id is required")
	}
	booking, err := findBooking(ctx, s.pool, id)
	return booking, s.databaseError(err)
}

func (s *Service) List(ctx context.Context, userID uuid.UUID) ([]Booking, error) {
	if userID == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}
	rows, err := s.pool.Query(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE user_id=$1 ORDER BY created_at DESC, id", userID)
	if err != nil {
		return nil, s.databaseError(err)
	}
	defer rows.Close()
	bookings := make([]Booking, 0)
	for rows.Next() {
		booking, err := scanBooking(rows)
		if err != nil {
			return nil, s.databaseError(err)
		}
		bookings = append(bookings, *booking)
	}
	return bookings, s.databaseError(rows.Err())
}

func validateInput(input CreateInput, id uuid.UUID) error {
	if id == uuid.Nil || input.UserID == uuid.Nil || input.FlightID == uuid.Nil {
		return status.Error(codes.InvalidArgument, "booking_id, user_id and flight_id are required")
	}
	if strings.TrimSpace(input.PassengerName) == "" || len([]rune(input.PassengerName)) > 255 {
		return status.Error(codes.InvalidArgument, "passenger_name must contain between 1 and 255 characters")
	}
	address, err := mail.ParseAddress(input.PassengerEmail)
	if err != nil || address.Address != input.PassengerEmail || len(input.PassengerEmail) > 254 {
		return status.Error(codes.InvalidArgument, "passenger_email must be a valid email address")
	}
	if input.SeatCount <= 0 || input.SeatCount > 999 {
		return status.Error(codes.InvalidArgument, "seat_count must be between 1 and 999")
	}
	return nil
}
