package checks

import (
	"context"
	"fmt"
	"net"
	"sitelert/internal/config"
	"time"
)

// TCP checker configuration constants.
const (
	tcpDialTimeout = 5 * time.Second
	tcpKeepAlive   = 30 * time.Second
)

// TCPChecker performs TCP connectivity checks.
type TCPChecker struct {
	dialer net.Dialer
}

// NewTCPChecker creates a new TCPChecker instance.
func NewTCPChecker() *TCPChecker {
	return &TCPChecker{
		dialer: net.Dialer{
			Timeout:   tcpDialTimeout,
			KeepAlive: tcpKeepAlive,
		},
	}
}

func (c *TCPChecker) Check(ctx context.Context, svc config.Service) Result {
	start := time.Now()

	addr := net.JoinHostPort(svc.Host, fmt.Sprintf("%d", svc.Port))
	conn, err := c.dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return Result{
			Success: false,
			Latency: time.Since(start),
			Error:   err.Error(),
		}
	}

	_ = conn.Close()

	return Result{
		Success: true,
		Latency: time.Since(start),
	}
}
