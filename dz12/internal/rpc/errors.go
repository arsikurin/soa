package rpc

import (
	"context"
	"encoding/json"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"marketplace/internal/fault"
	"marketplace/internal/rpc/pb"
)

func toRPCError(err error) error {
	if err == nil {
		return nil
	}
	var f *fault.Error
	if errors.As(err, &f) {
		details, marshalErr := json.Marshal(f.Details)
		if marshalErr != nil {
			return status.Error(codes.Internal, "Внутренняя ошибка сервиса")
		}
		code := codes.Internal
		switch f.Status {
		case 400:
			code = codes.InvalidArgument
		case 401:
			code = codes.Unauthenticated
		case 403:
			code = codes.PermissionDenied
		case 404:
			code = codes.NotFound
		case 409:
			code = codes.FailedPrecondition
		case 429:
			code = codes.ResourceExhausted
		case 503:
			code = codes.Unavailable
		}
		s, e := status.New(code, f.Message).WithDetails(&pb.BusinessError{ErrorCode: f.Code, Message: f.Message, HttpStatus: int32(f.Status), DetailsJson: details})
		if e == nil {
			return s.Err()
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "Время ожидания сервиса истекло")
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "Запрос отменён")
	}
	return status.Error(codes.Internal, "Внутренняя ошибка сервиса")
}

func fromRPCError(err error) error {
	if err == nil {
		return nil
	}
	s := status.Convert(err)
	for _, raw := range s.Details() {
		if detail, ok := raw.(*pb.BusinessError); ok && detail.HttpStatus >= 400 && detail.HttpStatus <= 599 && detail.ErrorCode != "" {
			f := fault.New(int(detail.HttpStatus), detail.ErrorCode, detail.Message)
			if len(detail.DetailsJson) > 0 && json.Unmarshal(detail.DetailsJson, &f.Details) != nil {
				return invalidResponse()
			}
			return f
		}
	}
	switch s.Code() {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled, codes.ResourceExhausted:
		return fault.New(503, "SERVICE_UNAVAILABLE", "Сервис временно недоступен")
	case codes.Unauthenticated:
		return fault.New(401, "TOKEN_INVALID", "Требуется действительная учётная запись")
	case codes.PermissionDenied:
		return fault.Denied()
	default:
		return fault.New(500, "INTERNAL_ERROR", "Внутренняя ошибка сервиса")
	}
}
