package httpapi

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/rs/cors"
)

func corsHandler(next http.Handler, logger *slog.Logger, allowedOrigins []string) http.Handler {
	origins := make([]string, 0, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		if origin = strings.TrimSpace(origin); origin != "" {
			origins = append(origins, origin)
		}
	}
	if len(origins) == 0 {
		return requestLog(logger, next)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions && r.Header.Get("Origin") != "" && r.Header.Get("Access-Control-Request-Method") != "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
	policy := cors.New(cors.Options{
		AllowedOrigins:     origins,
		AllowedMethods:     []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete},
		AllowedHeaders:     []string{"Authorization", "Content-Type", "Accept", "X-Request-Id"},
		ExposedHeaders:     []string{"X-Request-Id"},
		MaxAge:             600,
		OptionsPassthrough: true,
	})
	return policy.Handler(requestLog(logger, handler))
}
