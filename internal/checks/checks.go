package checks

import (
	"context"
	"sitelert/internal/config"
	"time"
)

// Result contains the outcome of a connectivity check.
type Result struct {
	Success    bool
	StatusCode int // HTTP status code (0 for non-HTTP checks)
	Latency    time.Duration
	Error      string
}

// Checker performs health checks on services.
type Checker interface {
	Check(ctx context.Context, svc config.Service) Result
}

// Factory creates appropriate checkers for different service types.
type Factory struct {
	http *HTTPChecker
	tcp  *TCPChecker
}

// NewFactory creates a new Checker factory with initialized checkers.
func NewFactory() *Factory {
	return &Factory{
		http: NewHTTPChecker(),
		tcp:  NewTCPChecker(),
	}
}

// CheckerFor returns the appropriate Checker for the given service type.
func (f *Factory) CheckerFor(svc config.Service) Checker {
	if svc.IsHTTP() {
		return f.http
	}
	if svc.IsTCP() {
		return f.tcp
	}
	return nil
}

// Check performs a health check for the given service.
func (f *Factory) Check(ctx context.Context, svc config.Service) Result {
	checker := f.CheckerFor(svc)
	if checker == nil {
		return Result{
			Success: false,
			Latency: 0,
			Error:   "unsupported service type: " + svc.Type,
		}
	}

	return checker.Check(ctx, svc)
}
