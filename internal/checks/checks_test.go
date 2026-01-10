package checks

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"uptiq/internal/config"
)

func TestFactory_CheckerFor(t *testing.T) {
	factory := NewFactory()

	tests := []struct {
		name        string
		service     config.Service
		checkerType string
	}{
		{
			name:        "http service",
			service:     config.Service{Type: "http"},
			checkerType: "*checks.HTTPChecker",
		},
		{
			name:        "tcp service",
			service:     config.Service{Type: "tcp"},
			checkerType: "*checks.TCPChecker",
		},
		{
			name:        "unknown service",
			service:     config.Service{Type: "grpc"},
			checkerType: "<nil>",
		},
		{
			name:        "empty type",
			service:     config.Service{Type: ""},
			checkerType: "<nil>",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checker := factory.CheckerFor(tc.service)

			var gotType string
			if checker == nil {
				gotType = "<nil>"
			} else {
				switch checker.(type) {
				case *HTTPChecker:
					gotType = "*checks.HTTPChecker"
				case *TCPChecker:
					gotType = "*checks.TCPChecker"
				default:
					gotType = "unknown"
				}
			}

			if gotType != tc.checkerType {
				t.Errorf("CheckerFor() returned %s, want %s", gotType, tc.checkerType)
			}
		})
	}
}

func TestFactory_Check_HTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	factory := NewFactory()
	svc := config.Service{
		Type: "http",
		URL:  server.URL,
	}

	result := factory.Check(context.Background(), svc)

	if !result.Success {
		t.Errorf("expected success, got failure: %s", result.Error)
	}
}

func TestFactory_Check_TCP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test server: %v", err)
	}
	defer func() {
		err := listener.Close()
		if err != nil {
			t.Fatalf("failed to close test server: %v", err)
		}
	}()

	errCh := make(chan error, 1)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				err := conn.Close()
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
			}()

		}
	}()

	addr := listener.Addr().(*net.TCPAddr)

	factory := NewFactory()
	svc := config.Service{
		Type: "tcp",
		Host: "127.0.0.1",
		Port: addr.Port,
	}

	result := factory.Check(context.Background(), svc)

	if !result.Success {
		t.Errorf("expected success, got failure: %s", result.Error)
	}

	select {
	case e := <-errCh:
		t.Fatalf("failed to close connection: %v", e)
	case <-time.After(200 * time.Millisecond):
		// no error reported from goroutine within timeout
	}
}

func TestFactory_Check_UnsupportedType(t *testing.T) {
	factory := NewFactory()
	svc := config.Service{
		Type: "websocket",
	}

	result := factory.Check(context.Background(), svc)

	if result.Success {
		t.Error("expected failure for unsupported type")
	}
	if result.Error == "" {
		t.Error("expected error message")
	}
	if result.Latency != 0 {
		t.Errorf("expected 0 latency for unsupported type, got %v", result.Latency)
	}
}

func TestNewFactory(t *testing.T) {
	factory := NewFactory()

	if factory == nil {
		t.Fatal("NewFactory returned nil")
	}
	if factory.http == nil {
		t.Error("factory.http is nil")
	}
	if factory.tcp == nil {
		t.Error("factory.tcp is nil")
	}
}

func TestFactory_SameCheckerInstance(t *testing.T) {
	factory := NewFactory()

	// Multiple calls should return the same checker instances
	http1 := factory.CheckerFor(config.Service{Type: "http"})
	http2 := factory.CheckerFor(config.Service{Type: "http"})
	tcp1 := factory.CheckerFor(config.Service{Type: "tcp"})
	tcp2 := factory.CheckerFor(config.Service{Type: "tcp"})

	if http1 != http2 {
		t.Error("expected same HTTP checker instance")
	}
	if tcp1 != tcp2 {
		t.Error("expected same TCP checker instance")
	}
}

func TestResult_Fields(t *testing.T) {
	result := Result{
		Success:    true,
		StatusCode: 200,
		Latency:    100,
		Error:      "",
	}

	if !result.Success {
		t.Error("Success should be true")
	}
	if result.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", result.StatusCode)
	}
	if result.Latency != 100 {
		t.Errorf("Latency = %v, want 100", result.Latency)
	}
	if result.Error != "" {
		t.Errorf("Error = %q, want empty", result.Error)
	}
}

func TestResult_Failure(t *testing.T) {
	result := Result{
		Success:    false,
		StatusCode: 500,
		Latency:    50,
		Error:      "internal server error",
	}

	if result.Success {
		t.Error("Success should be false")
	}
	if result.Error != "internal server error" {
		t.Errorf("Error = %q, want 'internal server error'", result.Error)
	}
}

func TestFactory_Integration(t *testing.T) {
	// Test HTTP check
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{"status":"ok"}`)); err != nil {
			t.Fatalf("failed to write response: %v", err)
		}
	}))
	defer httpServer.Close()

	// Test TCP check
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start TCP server: %v", err)
	}
	t.Cleanup(func() {
		if err := tcpListener.Close(); err != nil {
			t.Fatalf("close listener: %v", err)
		}
	})

	go func() {
		for {
			conn, err := tcpListener.Accept()
			if err != nil {
				return
			}
			if err := conn.Close(); err != nil {
				t.Fatalf("close conn: %v", err)
			}
		}
	}()

	tcpAddr := tcpListener.Addr().(*net.TCPAddr)

	factory := NewFactory()

	// Check HTTP service
	httpSvc := config.Service{
		Type:     "http",
		URL:      httpServer.URL,
		Contains: "status",
	}
	httpResult := factory.Check(context.Background(), httpSvc)
	if !httpResult.Success {
		t.Errorf("HTTP check failed: %s", httpResult.Error)
	}

	// Check TCP service
	tcpSvc := config.Service{
		Type: "tcp",
		Host: "127.0.0.1",
		Port: tcpAddr.Port,
	}
	tcpResult := factory.Check(context.Background(), tcpSvc)
	if !tcpResult.Success {
		t.Errorf("TCP check failed: %s", tcpResult.Error)
	}
}
