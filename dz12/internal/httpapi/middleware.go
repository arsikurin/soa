package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"
	"io"
	"log/slog"
	"marketplace/internal/fault"
	"net/http"
	"strings"
	"time"
)

const maxBodyBytes = 1 << 20

type requestMetadata struct{ UserID any }
type requestKey struct{}

func requestInfo(ctx context.Context) *requestMetadata {
	m, _ := ctx.Value(requestKey{}).(*requestMetadata)
	return m
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
		w.ResponseWriter.WriteHeader(code)
	}
}
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func requestLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := uuid.NewString()
		info := new(requestMetadata)
		ctx := context.WithValue(r.Context(), requestKey{}, info)
		md, _ := metadata.FromOutgoingContext(ctx)
		md = md.Copy()
		md.Set("x-request-id", id)
		r = r.WithContext(metadata.NewOutgoingContext(ctx, md))
		w.Header().Set("X-Request-Id", id)
		sw := &statusWriter{ResponseWriter: w}
		var body any
		defer func() {
			if recover() != nil {
				if sw.status == 0 {
					writeError(sw, fault.New(500, "INTERNAL_ERROR", "Внутренняя ошибка сервера"))
				}
			}
			status := sw.status
			if status == 0 {
				status = 200
			}
			args := []any{"request_id", id, "method", r.Method, "endpoint", r.URL.Path, "status_code", status, "duration_ms", float64(time.Since(start).Microseconds()) / 1000, "user_id", info.UserID, "timestamp", start.UTC().Format(time.RFC3339Nano)}
			if body != nil {
				args = append(args, "request_body", body)
			}
			logger.InfoContext(r.Context(), "api request", args...)
		}()
		r.Body = http.MaxBytesReader(sw, r.Body, maxBodyBytes)
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				body = "[body omitted]"
				writeError(sw, fault.Validation("body", "Размер тела превышает 1 MiB или тело не удалось прочитать"))
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(raw))
			if err := checkNumberEncoding(raw); err != nil {
				body = "[invalid JSON encoding omitted]"
				writeError(sw, err)
				return
			}
			body = safeBody(raw)
		}
		next.ServeHTTP(sw, r)
	})
}
func safeBody(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "[invalid JSON omitted]"
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return "[invalid JSON omitted]"
	}
	var redact func(any)
	redact = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, item := range v {
				key := strings.ToLower(k)
				if strings.Contains(key, "password") || strings.Contains(key, "token") || strings.Contains(key, "secret") || key == "authorization" {
					v[k] = "[REDACTED]"
				} else {
					redact(item)
				}
			}
		case []any:
			for _, item := range v {
				redact(item)
			}
		}
	}
	redact(value)
	return value
}
