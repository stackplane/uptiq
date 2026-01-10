package checks

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"uptiq/internal/config"
)

// HTTP checker configuration constants.
const (
	httpDialTimeout     = 5 * time.Second
	httpKeepAlive       = 30 * time.Second
	httpIdleConnTimeout = 90 * time.Second
	httpTLSTimeout      = 5 * time.Second
	httpContinueTimeout = 1 * time.Second
	httpMaxIdleConns    = 100
	httpMaxResponseBody = 1024 * 1024 // 1 MiB
	httpMinTLSVersion   = tls.VersionTLS12
)

// HTTPChecker performs HTTP/HTTPS connectivity checks.
type HTTPChecker struct {
	client *http.Client
}

// NewHTTPChecker creates a new HTTPChecker instance.
func NewHTTPChecker() *HTTPChecker {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   httpDialTimeout,
			KeepAlive: httpKeepAlive,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          httpMaxIdleConns,
		IdleConnTimeout:       httpIdleConnTimeout,
		TLSHandshakeTimeout:   httpTLSTimeout,
		ExpectContinueTimeout: httpContinueTimeout,
		TLSClientConfig: &tls.Config{
			MinVersion: httpMinTLSVersion,
		},
	}

	return &HTTPChecker{
		client: &http.Client{Transport: transport},
	}
}

func (c *HTTPChecker) Check(ctx context.Context, svc config.Service) Result {
	start := time.Now()

	req, err := c.buildRequest(ctx, svc)
	if err != nil {
		return Result{
			Success: false,
			Latency: time.Since(start),
			Error:   fmt.Sprintf("build request: %v", err),
		}
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return Result{
			Success: false,
			Latency: time.Since(start),
			Error:   err.Error(),
		}
	}
	defer func() { _ = resp.Body.Close() }()
	return c.evaluateResponse(resp, svc, start)
}

func (c *HTTPChecker) buildRequest(ctx context.Context, svc config.Service) (*http.Request, error) {
	method := strings.ToUpper(svc.Method)
	if method == "" {
		method = http.MethodGet
	}

	req, err := http.NewRequestWithContext(ctx, method, svc.URL, nil)
	if err != nil {
		return nil, err
	}

	for key, value := range svc.Headers {
		req.Header.Set(key, value)
	}

	return req, nil
}

func (c *HTTPChecker) evaluateResponse(resp *http.Response, svc config.Service, start time.Time) Result {
	result := Result{
		StatusCode: resp.StatusCode,
		Latency:    time.Since(start),
		Success:    true,
	}

	if err := c.validateStatusCode(resp.StatusCode, svc.ExpectedStatus); err != nil {
		result.Success = false
		result.Error = err.Error()
		return result
	}

	if err := c.validateBodyContent(resp.Body, svc.Contains); err != nil {
		result.Success = false
		result.Error = err.Error()
		return result
	}

	return result
}

func (c *HTTPChecker) validateStatusCode(actual int, expected []int) error {
	if len(expected) > 0 {
		for _, code := range expected {
			if actual == code {
				return nil
			}
		}

		return fmt.Errorf("unexpected status %d", actual)
	}

	if actual < 200 || actual >= 400 {
		return fmt.Errorf("unexpected status %d", actual)
	}

	return nil
}

func (c *HTTPChecker) validateBodyContent(body io.Reader, expected string) error {
	if strings.TrimSpace(expected) == "" {
		return nil
	}

	content, err := io.ReadAll(io.LimitReader(body, httpMaxResponseBody))
	if err != nil {
		return fmt.Errorf("read body: %v", err)
	}

	if !strings.Contains(string(content), expected) {
		return fmt.Errorf("response does not contain expected content")
	}

	return nil
}
