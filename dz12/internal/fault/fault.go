package fault

type Error struct {
	Code    string
	Message string
	Status  int
	Details map[string]any
}

func (e *Error) Error() string { return e.Message }
func New(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}
func Validation(field, message string) *Error {
	e := New(400, "VALIDATION_ERROR", "Некорректные данные запроса")
	e.Details = map[string]any{"fields": []map[string]string{{"field": field, "message": message}}}
	return e
}
func Denied() *Error {
	return New(403, "ACCESS_DENIED", "Недостаточно прав для этой операции")
}
