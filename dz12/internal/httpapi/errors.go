package httpapi

import (
	"encoding/json"
	"errors"
	"github.com/go-faster/jx"
	"github.com/ogen-go/ogen/ogenerrors"
	"github.com/ogen-go/ogen/validate"
	"marketplace/internal/api"
	"marketplace/internal/fault"
	"net/http"
	"regexp"
	"strings"
)

func contractError(err error) *api.ErrorStatusCode {
	var f *fault.Error
	if !errors.As(err, &f) {
		if ogenerrors.ErrorCode(err) == 401 {
			f = fault.New(401, "TOKEN_INVALID", "Требуется access token")
		} else {
			f = fault.New(500, "INTERNAL_ERROR", "Внутренняя ошибка сервера")
		}
	}
	result := &api.ErrorStatusCode{StatusCode: f.Status, Response: api.Error{ErrorCode: f.Code, Message: f.Message}}
	if f.Details != nil {
		details := api.ErrorDetails{}
		for key, value := range f.Details {
			b, e := json.Marshal(value)
			if e == nil {
				details[key] = jx.Raw(b)
			}
		}
		result.Response.Details = api.NewOptErrorDetails(details)
	}
	return result
}
func writeError(w http.ResponseWriter, err error) {
	response := contractError(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	_ = json.NewEncoder(w).Encode(&response.Response)
}

var decodeField = regexp.MustCompile(`(?:decode field|unexpected field) "([a-zA-Z0-9_]+)"`)

func validationError(err error) *fault.Error {
	f := fault.Validation("body", "Некорректный JSON или тип поля")
	var fields []map[string]string
	var visit func(error, string)
	visit = func(err error, prefix string) {
		var ve *validate.Error
		if errors.As(err, &ve) {
			for _, item := range ve.Fields {
				field := item.Name
				if prefix != "" {
					field = prefix + "." + field
				}
				visit(item.Error, field)
			}
			return
		}
		var pe *ogenerrors.DecodeParamError
		if errors.As(err, &pe) {
			visit(pe.Err, pe.Name)
			return
		}
		if prefix != "" {
			fields = append(fields, map[string]string{"field": prefix, "message": err.Error()})
		}
	}
	visit(err, "")
	if len(fields) == 0 {
		matches := decodeField.FindAllStringSubmatch(err.Error(), -1)
		if len(matches) > 0 {
			names := make([]string, 0, len(matches))
			for _, m := range matches {
				names = append(names, m[1])
			}
			fields = append(fields, map[string]string{"field": strings.Join(names, "."), "message": "Некорректный тип, формат или неизвестное поле"})
		}
	}
	if len(fields) > 0 {
		f.Details["fields"] = fields
	}
	return f
}
