package checks

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"uptiq/internal/config"
)

func TestHTTPChecker_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("OK")); err != nil {
			t.Fatalf("failed to write response: %v", err)
		}
	}))
	defer server.Close()

	checker := NewHTTPChecker()
	svc := config.Service{
		Type: "http",
		URL:  server.URL,
	}

	ctx := context.Background()
	result := checker.Check(ctx, svc)

	if !result.Success {
		t.Errorf("expected success, got failure: %s", result.Error)
	}
	if result.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", result.StatusCode)
	}
	if result.Latency <= 0 {
		t.Error("expected positive latency")
	}
}

func TestHTTPChecker_ExpectedStatus(t *testing.T) {
	tests := []struct {
		name           string
		serverStatus   int
		expectedStatus []int
		shouldSucceed  bool
	}{
		{
			name:           "exact match",
			serverStatus:   200,
			expectedStatus: []int{200},
			shouldSucceed:  true,
		},
		{
			name:           "one of many",
			serverStatus:   201,
			expectedStatus: []int{200, 201, 204},
			shouldSucceed:  true,
		},
		{
			name:           "no match",
			serverStatus:   404,
			expectedStatus: []int{200, 201},
			shouldSucceed:  false,
		},
		{
			name:           "empty expected uses default 2xx/3xx",
			serverStatus:   200,
			expectedStatus: nil,
			shouldSucceed:  true,
		},
		{
			name:           "empty expected rejects 4xx",
			serverStatus:   400,
			expectedStatus: nil,
			shouldSucceed:  false,
		},
		{
			name:           "redirect accepted by default",
			serverStatus:   302,
			expectedStatus: nil,
			shouldSucceed:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.serverStatus)
			}))
			defer server.Close()

			checker := NewHTTPChecker()
			svc := config.Service{
				Type:           "http",
				URL:            server.URL,
				ExpectedStatus: tc.expectedStatus,
			}

			result := checker.Check(context.Background(), svc)

			if result.Success != tc.shouldSucceed {
				t.Errorf("Success = %v, want %v (error: %s)", result.Success, tc.shouldSucceed, result.Error)
			}
		})
	}
}

func TestHTTPChecker_ContainsValidation(t *testing.T) {
	tests := []struct {
		name          string
		responseBody  string
		contains      string
		shouldSucceed bool
	}{
		{
			name:          "contains match",
			responseBody:  `{"status": "healthy", "version": "1.0"}`,
			contains:      `"status": "healthy"`,
			shouldSucceed: true,
		},
		{
			name:          "contains no match",
			responseBody:  `{"status": "unhealthy"}`,
			contains:      `"status": "healthy"`,
			shouldSucceed: false,
		},
		{
			name:          "empty contains always succeeds",
			responseBody:  "anything",
			contains:      "",
			shouldSucceed: true,
		},
		{
			name:          "whitespace contains treated as empty",
			responseBody:  "anything",
			contains:      "   ",
			shouldSucceed: true,
		},
		{
			name:          "partial match",
			responseBody:  "Hello, World!",
			contains:      "World",
			shouldSucceed: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				if _, err := w.Write([]byte(tc.responseBody)); err != nil {
					t.Fatalf("failed to write response: %v", err)
				}
			}))
			defer server.Close()

			checker := NewHTTPChecker()
			svc := config.Service{
				Type:     "http",
				URL:      server.URL,
				Contains: tc.contains,
			}

			result := checker.Check(context.Background(), svc)

			if result.Success != tc.shouldSucceed {
				t.Errorf("Success = %v, want %v (error: %s)", result.Success, tc.shouldSucceed, result.Error)
			}
		})
	}
}

func TestHTTPChecker_Methods(t *testing.T) {
	tests := []struct {
		method string
	}{
		{"GET"},
		{"POST"},
		{"PUT"},
		{"DELETE"},
		{"HEAD"},
		{"PATCH"},
		{""}, // Empty should default to GET
	}

	for _, tc := range tests {
		t.Run(tc.method, func(t *testing.T) {
			var receivedMethod string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedMethod = r.Method
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			checker := NewHTTPChecker()
			svc := config.Service{
				Type:   "http",
				URL:    server.URL,
				Method: tc.method,
			}

			result := checker.Check(context.Background(), svc)

			expectedMethod := tc.method
			if expectedMethod == "" {
				expectedMethod = "GET"
			}

			if receivedMethod != expectedMethod {
				t.Errorf("received method %q, want %q", receivedMethod, expectedMethod)
			}
			if !result.Success {
				t.Errorf("request failed: %s", result.Error)
			}
		})
	}
}

func TestHTTPChecker_Headers(t *testing.T) {
	var receivedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	checker := NewHTTPChecker()
	svc := config.Service{
		Type: "http",
		URL:  server.URL,
		Headers: map[string]string{
			"Authorization": "Bearer token123",
			"X-Custom":      "custom-value",
			"Content-Type":  "application/json",
		},
	}

	result := checker.Check(context.Background(), svc)

	if !result.Success {
		t.Errorf("request failed: %s", result.Error)
	}

	if receivedHeaders.Get("Authorization") != "Bearer token123" {
		t.Errorf("Authorization header = %q, want %q", receivedHeaders.Get("Authorization"), "Bearer token123")
	}
	if receivedHeaders.Get("X-Custom") != "custom-value" {
		t.Errorf("X-Custom header = %q, want %q", receivedHeaders.Get("X-Custom"), "custom-value")
	}
	if receivedHeaders.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type header = %q, want %q", receivedHeaders.Get("Content-Type"), "application/json")
	}
}

func TestHTTPChecker_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	checker := NewHTTPChecker()
	svc := config.Service{
		Type: "http",
		URL:  server.URL,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result := checker.Check(ctx, svc)

	if result.Success {
		t.Error("expected timeout failure")
	}
	if result.Error == "" {
		t.Error("expected error message")
	}
}

func TestHTTPChecker_ConnectionError(t *testing.T) {
	checker := NewHTTPChecker()
	svc := config.Service{
		Type: "http",
		URL:  "http://localhost:59999", // Unlikely to be listening
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := checker.Check(ctx, svc)

	if result.Success {
		t.Error("expected connection failure")
	}
	if result.Error == "" {
		t.Error("expected error message")
	}
	if result.Latency <= 0 {
		t.Error("expected positive latency even on failure")
	}
}

func TestHTTPChecker_InvalidURL(t *testing.T) {
	checker := NewHTTPChecker()
	svc := config.Service{
		Type: "http",
		URL:  "://invalid-url",
	}

	result := checker.Check(context.Background(), svc)

	if result.Success {
		t.Error("expected failure for invalid URL")
	}
	if !strings.Contains(result.Error, "build request") {
		t.Errorf("error should mention build request: %s", result.Error)
	}
}

func TestHTTPChecker_ValidateStatusCode(t *testing.T) {
	checker := NewHTTPChecker()

	tests := []struct {
		name     string
		actual   int
		expected []int
		hasError bool
	}{
		{"match in list", 200, []int{200, 201}, false},
		{"no match", 404, []int{200, 201}, true},
		{"empty list 2xx", 200, nil, false},
		{"empty list 3xx", 301, nil, false},
		{"empty list 4xx", 400, nil, true},
		{"empty list 5xx", 500, nil, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checker.validateStatusCode(tc.actual, tc.expected)
			if tc.hasError && err == nil {
				t.Error("expected error")
			}
			if !tc.hasError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestHTTPChecker_ValidateBodyContent(t *testing.T) {
	checker := NewHTTPChecker()

	tests := []struct {
		name     string
		body     string
		expected string
		hasError bool
	}{
		{"contains match", "hello world", "world", false},
		{"no match", "hello world", "foo", true},
		{"empty expected", "anything", "", false},
		{"whitespace expected", "anything", "   ", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := io.NopCloser(strings.NewReader(tc.body))
			err := checker.validateBodyContent(reader, tc.expected)
			if tc.hasError && err == nil {
				t.Error("expected error")
			}
			if !tc.hasError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestNewHTTPChecker(t *testing.T) {
	checker := NewHTTPChecker()
	if checker == nil {
		t.Fatal("NewHTTPChecker returned nil")
	}
	if checker.client == nil {
		t.Error("checker client is nil")
	}
}
