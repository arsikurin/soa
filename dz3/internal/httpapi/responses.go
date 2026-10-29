package httpapi

import (
	"fmt"

	"flightbooking/internal/api"
	"flightbooking/internal/booking"
	"flightbooking/internal/rpc/pb"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func money(amount int64) string { return fmt.Sprintf("%d.%02d", amount/100, amount%100) }
func bookingResponse(b *booking.Booking) *api.Booking {
	return &api.Booking{ID: b.ID, UserID: b.UserID, FlightID: b.FlightID, PassengerName: b.PassengerName, PassengerEmail: b.PassengerEmail, SeatCount: int(b.SeatCount), TotalPrice: money(b.TotalPriceMinor), Currency: "RUB", Status: api.BookingStatus(b.Status), CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt}
}
func flightResponse(f *pb.Flight) (*api.Flight, error) {
	if f == nil || f.DepartureAt == nil || f.ArrivalAt == nil {
		return nil, status.Error(codes.Internal, "Invalid Flight response")
	}
	id, err := uuid.Parse(f.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, "Invalid flight ID")
	}
	return &api.Flight{ID: id, FlightNumber: f.FlightNumber, Airline: f.Airline, Origin: f.Origin, Destination: f.Destination, DepartureAt: f.DepartureAt.AsTime(), ArrivalAt: f.ArrivalAt.AsTime(), TotalSeats: int(f.TotalSeats), AvailableSeats: int(f.AvailableSeats), Price: money(f.PriceMinor), Currency: "RUB", Status: api.FlightStatus(f.Status.String())}, nil
}
