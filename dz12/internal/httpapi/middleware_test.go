package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestLoggingMasksSecretsAndKeepsIdentity(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	id := uuid.NewString()
	h := requestLog(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, _ := io.ReadAll(r.Body); !bytes.Contains(b, []byte("secret-pass")) {
			t.Error("middleware changed request body")
		}
		requestInfo(r.Context()).UserID = id
		w.WriteHeader(201)
	}))
	r := httptest.NewRequest("POST", "/auth/register", strings.NewReader(`{"email":"a@example.com","password":"secret-pass","nested":[{"refresh_token":"secret-token"}]}`))
	r.Header.Set("X-Request-Id", "untrusted")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if _, err := uuid.Parse(w.Header().Get("X-Request-Id")); err != nil {
		t.Fatal("request id is not generated UUID")
	}
	if strings.Contains(output.String(), "secret-pass") || strings.Contains(output.String(), "secret-token") {
		t.Fatalf("secret leaked: %s", output.String())
	}
	var event map[string]any
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event["status_code"] != float64(201) || event["user_id"] != id || event["method"] != "POST" || event["endpoint"] != "/auth/register" || event["timestamp"] == nil || event["duration_ms"] == nil {
		t.Fatalf("bad log %+v", event)
	}
	if event["request_id"] != w.Header().Get("X-Request-Id") {
		t.Fatal("log/header id mismatch")
	}
}
func TestMalformedBodyNeverLeaksPassword(t *testing.T) {
	var output bytes.Buffer
	h := requestLog(slog.New(slog.NewJSONHandler(&output, nil)), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/auth/login", strings.NewReader(`{"password":"secret-pass`)))
	if strings.Contains(output.String(), "secret-pass") {
		t.Fatal("malformed body leaked")
	}
}
func TestRequestLimitAndRecoveryUseContractError(t *testing.T) {
	for _, tc := range []struct {
		name, body, code string
		status           int
		panic            bool
	}{
		{"oversized", strings.Repeat("x", (1<<20)+1), "VALIDATION_ERROR", 400, false},
		{"panic", "{}", "INTERNAL_ERROR", 500, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := requestLog(slog.New(slog.NewJSONHandler(io.Discard, nil)), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.panic {
					panic("private detail")
				}
				t.Error("oversized body reached handler")
			}))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", "/products", strings.NewReader(tc.body)))
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) || strings.Contains(w.Body.String(), "private detail") {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
	if requestInfo(context.Background()) != nil {
		t.Fatal("unexpected request info")
	}
}

func TestRequestIDPropagatesToOutgoingRPCMetadata(t *testing.T) {
	h := requestLog(slog.New(slog.NewJSONHandler(io.Discard, nil)), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		md, _ := metadata.FromOutgoingContext(r.Context())
		values := md.Get("x-request-id")
		if len(values) != 1 || values[0] == "" {
			t.Error("request ID missing from outgoing RPC metadata")
			return
		}
		_, _ = w.Write([]byte(values[0]))
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	if w.Body.String() != w.Header().Get("X-Request-Id") || w.Body.Len() == 0 {
		t.Fatalf("HTTP and RPC request IDs differ: %q %q", w.Body.String(), w.Header().Get("X-Request-Id"))
	}
}
