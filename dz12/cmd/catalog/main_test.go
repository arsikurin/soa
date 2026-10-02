package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The contract deliberately excludes all business endpoints.
func TestHTTPContract(t *testing.T) {
	// Exercise net/http as well as the router: net/http suppresses HEAD bodies.
	server := httptest.NewServer(newHandler())
	defer server.Close()
	for _, tc := range []struct {
		name, method, path string
		status             int
	}{
		{"health", http.MethodGet, "/health", http.StatusOK},
		{"head", http.MethodHead, "/health", http.StatusOK},
		{"root", http.MethodGet, "/", http.StatusNotFound},
		{"no business API", http.MethodGet, "/products", http.StatusNotFound},
		{"exact path", http.MethodGet, "/health/extra", http.StatusNotFound},
		{"unsupported method", http.MethodPost, "/health", http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := http.NewRequest(tc.method, server.URL+tc.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, tc.status)
			}
			if tc.status == http.StatusOK {
				if got := response.Header.Get("Content-Type"); got != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", got)
				}
				want := "{\"status\":\"ok\"}\n"
				if tc.method == http.MethodHead {
					want = ""
				}
				if got := string(body); got != want {
					t.Errorf("body = %q, want %q", got, want)
				}
			}
		})
	}
}
