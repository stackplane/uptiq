package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var ErrServerClosed = http.ErrServerClosed

type Server struct {
	httpServer *http.Server
	log        *slog.Logger
}

func NewServer(addr string, log *slog.Logger, reg *prometheus.Registry) *Server {
	if log == nil {
		log = slog.Default()
	}
	if reg == nil {
		reg = prometheus.NewRegistry()
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})

	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	s := &http.Server{
		Addr:              addr,
		Handler:           withRequestLogging(mux, log),
		ReadHeaderTimeout: 5 * time.Second,
	}

	return &Server{
		httpServer: s,
		log:        log,
	}
}

func (s *Server) ListenAndServe() error {
	if s.httpServer == nil {
		return errors.New("Server not initialized")
	}
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
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

func (s *Server) String() string {
	if s.httpServer == nil {
		return "server(nil)"
	}
	return fmt.Sprintf("server(%s)", s.httpServer.Addr)
}
