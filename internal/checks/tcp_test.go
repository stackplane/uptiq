package checks

import (
	"context"
	"net"
	"testing"
	"time"

	"uptiq/internal/config"
)

func TestTCPChecker_Success(t *testing.T) {
	// Start a test TCP server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test server: %v", err)
	}
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Fatalf("close listener: %v", err)
		}
	})

	// Accept connections in background
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			if err := conn.Close(); err != nil {
				t.Logf("close conn: %v", err)
			}
		}
	}()

	addr := listener.Addr().(*net.TCPAddr)

	checker := NewTCPChecker()
	svc := config.Service{
		Type: "tcp",
		Host: "127.0.0.1",
		Port: addr.Port,
	}

	ctx := context.Background()
	result := checker.Check(ctx, svc)

	if !result.Success {
		t.Errorf("expected success, got failure: %s", result.Error)
	}
	if result.Latency <= 0 {
		t.Error("expected positive latency")
	}
	if result.StatusCode != 0 {
		t.Errorf("TCP check should have status 0, got %d", result.StatusCode)
	}
}

func TestTCPChecker_ConnectionRefused(t *testing.T) {
	checker := NewTCPChecker()
	svc := config.Service{
		Type: "tcp",
		Host: "127.0.0.1",
		Port: 59998, // Unlikely to be listening
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

func TestTCPChecker_Timeout(t *testing.T) {
	// Use a non-routable IP to cause timeout
	checker := NewTCPChecker()
	svc := config.Service{
		Type: "tcp",
		Host: "10.255.255.1", // Non-routable, should timeout
		Port: 80,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	result := checker.Check(ctx, svc)

	if result.Success {
		t.Error("expected timeout failure")
	}
	if result.Error == "" {
		t.Error("expected error message")
	}
}

func TestTCPChecker_InvalidHost(t *testing.T) {
	checker := NewTCPChecker()
	svc := config.Service{
		Type: "tcp",
		Host: "invalid.host.that.does.not.exist.local",
		Port: 80,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := checker.Check(ctx, svc)

	if result.Success {
		t.Error("expected DNS resolution failure")
	}
	if result.Error == "" {
		t.Error("expected error message")
	}
}

func TestTCPChecker_DifferentPorts(t *testing.T) {
	// Start multiple test servers on different ports
	ports := make([]int, 3)
	listeners := make([]net.Listener, 3)

	for i := range ports {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("failed to start test server %d: %v", i, err)
		}
		listeners[i] = listener
		ports[i] = listener.Addr().(*net.TCPAddr).Port

		// Accept connections
		go func(l net.Listener) {
			for {
				conn, err := l.Accept()
				if err != nil {
					return
				}
				conn.Close()
			}
		}(listener)
	}

	defer func() {
		for _, l := range listeners {
			if err := l.Close(); err != nil {
				t.Fatalf("close listener: %v", err)
			}
		}
	}()

	checker := NewTCPChecker()

	for i, port := range ports {
		svc := config.Service{
			Type: "tcp",
			Host: "127.0.0.1",
			Port: port,
		}

		result := checker.Check(context.Background(), svc)

		if !result.Success {
			t.Errorf("server %d (port %d): expected success, got failure: %s", i, port, result.Error)
		}
	}
}

func TestTCPChecker_IPv6(t *testing.T) {
	// Try to start a server on IPv6 localhost
	listener, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("IPv6 not available on this system")
	}
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Fatalf("close listener: %v", err)
		}
	})

	// Accept connections
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	addr := listener.Addr().(*net.TCPAddr)

	checker := NewTCPChecker()
	svc := config.Service{
		Type: "tcp",
		Host: "::1",
		Port: addr.Port,
	}

	result := checker.Check(context.Background(), svc)

	if !result.Success {
		t.Errorf("expected success for IPv6, got failure: %s", result.Error)
	}
}

func TestTCPChecker_ZeroPort(t *testing.T) {
	checker := NewTCPChecker()
	svc := config.Service{
		Type: "tcp",
		Host: "127.0.0.1",
		Port: 0,
	}

	result := checker.Check(context.Background(), svc)

	// Port 0 is invalid for connection
	if result.Success {
		t.Error("expected failure for port 0")
	}
}

func TestTCPChecker_ContextCancellation(t *testing.T) {
	checker := NewTCPChecker()
	svc := config.Service{
		Type: "tcp",
		Host: "10.255.255.1", // Non-routable
		Port: 80,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	result := checker.Check(ctx, svc)

	if result.Success {
		t.Error("expected failure due to cancelled context")
	}
}

func TestNewTCPChecker(t *testing.T) {
	checker := NewTCPChecker()
	if checker == nil {
		t.Fatal("NewTCPChecker returned nil")
	}
}

func TestTCPChecker_LatencyMeasurement(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test server: %v", err)
	}
	defer listener.Close()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	addr := listener.Addr().(*net.TCPAddr)

	checker := NewTCPChecker()
	svc := config.Service{
		Type: "tcp",
		Host: "127.0.0.1",
		Port: addr.Port,
	}

	// Run multiple checks and verify latency is reasonable
	for i := 0; i < 5; i++ {
		result := checker.Check(context.Background(), svc)

		if !result.Success {
			t.Errorf("check %d failed: %s", i, result.Error)
			continue
		}

		// Local connection should be very fast (< 100ms)
		if result.Latency > 100*time.Millisecond {
			t.Errorf("check %d: latency %v seems too high for local connection", i, result.Latency)
		}
		if result.Latency <= 0 {
			t.Errorf("check %d: latency should be positive", i)
		}
	}
}
