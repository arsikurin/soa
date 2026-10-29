package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"flightbooking/internal/api"
	"flightbooking/internal/booking"
	"flightbooking/internal/rpc/pb"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Bookings interface {
	Create(context.Context, booking.CreateInput, uuid.UUID) (*booking.Booking, error)
	Get(context.Context, uuid.UUID) (*booking.Booking, error)
	List(context.Context, uuid.UUID) ([]booking.Booking, error)
	Cancel(context.Context, uuid.UUID) (*booking.Booking, error)
}
type server struct {
	bookings Bookings
	flights  pb.FlightServiceClient
}

func New(bookings Bookings, flights pb.FlightServiceClient, logger *slog.Logger) (http.Handler, error) {
	h := &server{bookings: bookings, flights: flights}
	generated, err := api.NewServer(h, api.WithErrorHandler(func(ctx context.Context, w http.ResponseWriter, r *http.Request, err error) {
		writeError(w, status.Error(codes.InvalidArgument, "Invalid request: "+err.Error()))
	}), api.WithNotFound(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, status.Error(codes.NotFound, "Endpoint not found"))
	}))
	if err != nil {
		return nil, err
	}
	return requestLog(logger, generated), nil
}
func (s *server) Health(context.Context) (*api.Health, error) { return &api.Health{Status: "ok"}, nil }
func (s *server) CreateBooking(ctx context.Context, req *api.BookingCreate, params api.CreateBookingParams) (*api.Booking, error) {
	key := uuid.New()
	if params.IdempotencyKey.Set {
		key = params.IdempotencyKey.Value
	}
	result, err := s.bookings.Create(ctx, booking.CreateInput{UserID: req.UserID, FlightID: req.FlightID, PassengerName: req.PassengerName, PassengerEmail: req.PassengerEmail, SeatCount: int32(req.SeatCount)}, key)
	if err != nil {
		return nil, err
	}
	return bookingResponse(result), nil
}
func (s *server) GetBooking(ctx context.Context, params api.GetBookingParams) (*api.Booking, error) {
	result, err := s.bookings.Get(ctx, params.ID)
	if err != nil {
		return nil, err
	}
	return bookingResponse(result), nil
}
func (s *server) CancelBooking(ctx context.Context, params api.CancelBookingParams) (*api.Booking, error) {
	result, err := s.bookings.Cancel(ctx, params.ID)
	if err != nil {
		return nil, err
	}
	return bookingResponse(result), nil
}
func (s *server) ListBookings(ctx context.Context, params api.ListBookingsParams) (api.BookingList, error) {
	rows, err := s.bookings.List(ctx, params.UserID)
	if err != nil {
		return nil, err
	}
	out := make(api.BookingList, 0, len(rows))
	for i := range rows {
		out = append(out, *bookingResponse(&rows[i]))
	}
	return out, nil
}
func (s *server) GetFlight(ctx context.Context, params api.GetFlightParams) (*api.Flight, error) {
	result, err := s.flights.GetFlight(ctx, &pb.GetFlightRequest{Id: params.ID.String()})
	if err != nil {
		return nil, err
	}
	return flightResponse(result)
}
func (s *server) SearchFlights(ctx context.Context, params api.SearchFlightsParams) (api.FlightList, error) {
	req := &pb.SearchFlightsRequest{Origin: params.Origin, Destination: params.Destination}
	if params.Date.Set {
		day := params.Date.Value.Format("2006-01-02")
		req.Date = &day
	}
	result, err := s.flights.SearchFlights(ctx, req)
	if err != nil {
		return nil, err
	}
	out := make(api.FlightList, 0, len(result.Flights))
	for _, row := range result.Flights {
		value, err := flightResponse(row)
		if err != nil {
			return nil, err
		}
		out = append(out, *value)
	}
	return out, nil
}
func (s *server) NewError(ctx context.Context, err error) *api.ErrorStatusCode {
	return contractError(err)
}
