package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"marketplace/internal/auth"
)

func TestCORSPreflightSupportsBrowserCRUDAndIsLogged(t *testing.T) {
	for _, method := range []string{"GET", "HEAD", "POST", "PUT", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			var logs bytes.Buffer
			h, err := New(nil, nil, nil, nil, slog.New(slog.NewJSONHandler(&logs, nil)), "http://localhost:8082")
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("OPTIONS", "/products", nil)
			r.Header.Set("Origin", "http://localhost:8082")
			r.Header.Set("Access-Control-Request-Method", method)
			r.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
				t.Fatalf("preflight: %d %s", w.Code, w.Body.String())
			}
			if w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:8082" || !headerContains(w.Header(), "Access-Control-Allow-Methods", method) {
				t.Fatalf("missing CORS permissions: %v", w.Header())
			}
			for _, header := range []string{"Authorization", "Content-Type"} {
				if !headerContains(w.Header(), "Access-Control-Allow-Headers", header) {
					t.Fatalf("missing allowed header %s: %v", header, w.Header())
				}
			}
			var event map[string]any
			if err := json.Unmarshal(logs.Bytes(), &event); err != nil {
				t.Fatal(err)
			}
			if w.Header().Get("X-Request-Id") == "" || event["request_id"] != w.Header().Get("X-Request-Id") || event["method"] != "OPTIONS" || event["status_code"] != float64(204) {
				t.Fatalf("missing preflight log or request ID: %v", event)
			}
		})
	}
}

func TestCORSHeadersSurviveAuthenticationAndBodyValidation(t *testing.T) {
	tokens, err := auth.NewTokens("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
	}{
		{"health", "GET", "/health", "", 200},
		{"authentication required", "POST", "/products", `{}`, 401},
		{"invalid JSON", "POST", "/auth/login", `{`, 400},
		{"oversized body", "POST", "/products", strings.Repeat("x", maxBodyBytes+1), 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			h, err := New(nil, nil, nil, tokens, slog.New(slog.NewJSONHandler(&logs, nil)), " http://localhost:63342 ", "", "http://localhost:8082")
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Origin", "http://localhost:63342")
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:63342" {
				t.Fatalf("browser cannot read response: %d %v %s", w.Code, w.Header(), w.Body.String())
			}
			if !headerContains(w.Header(), "Access-Control-Expose-Headers", "X-Request-Id") || !headerContains(w.Header(), "Vary", "Origin") {
				t.Fatalf("missing exposed request ID or cache variation: %v", w.Header())
			}
			if w.Header().Get("Access-Control-Allow-Credentials") != "" {
				t.Fatal("cookie credentials must not be enabled")
			}
		})
	}
}

func TestCORSDoesNotGrantUnconfiguredBrowserAccess(t *testing.T) {
	for _, tc := range []struct {
		name, origin, method, headers string
		origins                       []string
	}{
		{"different origin", "https://example.com", "POST", "authorization", []string{"http://localhost:8082"}},
		{"different port", "http://localhost:8083", "POST", "authorization", []string{"http://localhost:8082"}},
		{"null origin", "null", "POST", "authorization", []string{"http://localhost:8082"}},
		{"unsupported method", "http://localhost:8082", "PATCH", "authorization", []string{"http://localhost:8082"}},
		{"unsupported header", "http://localhost:8082", "POST", "x-unexpected", []string{"http://localhost:8082"}},
		{"disabled", "http://localhost:8082", "POST", "authorization", nil},
		{"blank config", "http://localhost:8082", "POST", "authorization", []string{"", " "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			h, err := New(nil, nil, nil, nil, slog.New(slog.NewJSONHandler(&logs, nil)), tc.origins...)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("OPTIONS", "/products", nil)
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Access-Control-Request-Method", tc.method)
			r.Header.Set("Access-Control-Request-Headers", tc.headers)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatalf("unexpected CORS permission: %v", w.Header())
			}
		})
	}
}

func headerContains(headers http.Header, name, value string) bool {
	for _, line := range headers.Values(name) {
		for _, item := range strings.Split(line, ",") {
			if strings.EqualFold(strings.TrimSpace(item), value) {
				return true
			}
		}
	}
	return false
}
