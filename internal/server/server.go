// Package server provides HTTP endpoints for health checks and metrics.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ErrServerClosed is returned when the server is shut down.
var ErrServerClosed = http.ErrServerClosed

// Server configuration constants.
const (
	readHeaderTimeout = 5 * time.Second
)

// Server provides HTTP endpoints.
type Server struct {
	httpServer *http.Server
	log        *slog.Logger
}

// New creates a new server.
func New(addr string, log *slog.Logger, reg *prometheus.Registry) *Server {
	if log == nil {
		log = slog.Default()
	}
	if reg == nil {
		reg = prometheus.NewRegistry()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthHandler)
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	return &Server{
		httpServer: &http.Server{
			Addr:              addr,
			Handler:           withRequestLogging(mux, log),
			ReadHeaderTimeout: readHeaderTimeout,
		},
		log: log,
	}
}

// ListenAndServe starts the server.
func (s *Server) ListenAndServe() error {
	if s.httpServer == nil {
		return errors.New("server not initialized")
	}
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}

// Addr returns the server's address.
func (s *Server) Addr() string {
	if s.httpServer == nil {
		return ""
	}
	return s.httpServer.Addr
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

func withRequestLogging(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Debug("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}
