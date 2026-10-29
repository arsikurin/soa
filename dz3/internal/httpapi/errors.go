package httpapi

import (
	"context"
	"errors"
	"net/http"

	"flightbooking/internal/api"
	"github.com/go-faster/jx"
	"github.com/google/uuid"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func contractError(err error) *api.ErrorStatusCode {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		err = status.FromContextError(err).Err()
	}
	s := status.Convert(err)
	httpCode := 500
	code := "INTERNAL"
	message := "Internal service error"
	switch s.Code() {
	case codes.InvalidArgument:
		httpCode = 400
		code = "INVALID_ARGUMENT"
	case codes.NotFound:
		httpCode = 404
		code = "NOT_FOUND"
	case codes.AlreadyExists:
		httpCode = 409
		code = "ALREADY_EXISTS"
	case codes.Aborted:
		httpCode = 409
		code = "ABORTED"
	case codes.FailedPrecondition:
		httpCode = 409
		code = "FAILED_PRECONDITION"
	case codes.ResourceExhausted:
		httpCode = 409
		code = "RESOURCE_EXHAUSTED"
	case codes.Unavailable:
		httpCode = 503
		code = "UNAVAILABLE"
	case codes.DeadlineExceeded:
		httpCode = 503
		code = "DEADLINE_EXCEEDED"
	case codes.Unauthenticated:
		httpCode = 503
		code = "SERVICE_AUTH_FAILED"
	case codes.Canceled:
		httpCode = 408
		code = "CANCELLED"
	}
	if httpCode != 500 {
		message = s.Message()
	}
	result := &api.ErrorStatusCode{StatusCode: httpCode, Response: api.Error{ErrorCode: code, Message: message}}
	for _, detail := range s.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok {
			if info.Reason == "OPERATION_PENDING" || info.Reason == "CIRCUIT_OPEN" {
				result.Response.ErrorCode = info.Reason
			}
			if id, err := uuid.Parse(info.Metadata["booking_id"]); err == nil {
				result.Response.BookingID = api.NewOptUUID(id)
			}
		}
	}
	return result
}
func writeError(w http.ResponseWriter, err error) {
	value := contractError(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(value.StatusCode)
	var enc jx.Encoder
	value.Response.Encode(&enc)
	_, _ = w.Write(enc.Bytes())
}
