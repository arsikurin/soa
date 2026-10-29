package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type responseWriter struct {
	http.ResponseWriter
	code int
}

func (w *responseWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
		w.ResponseWriter.WriteHeader(code)
	}
}
func (w *responseWriter) Write(data []byte) (int, error) {
	if w.code == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(data)
}
func requestLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := uuid.NewString()
		w.Header().Set("X-Request-Id", id)
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		tracked := &responseWriter{ResponseWriter: w}
		next.ServeHTTP(tracked, r)
		if tracked.code == 0 {
			tracked.code = 200
		}
		logger.Info("http_request", "request_id", id, "method", r.Method, "endpoint", r.URL.Path, "status_code", tracked.code, "duration_ms", time.Since(start).Milliseconds())
	})
}
