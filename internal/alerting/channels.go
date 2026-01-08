package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"sitelert/internal/config"
)

// Sender configuration constants.
const (
	webhookTimeout = 7 * time.Second
)

// ChannelSender sends alerts to notification channels.
type ChannelSender struct {
	client *http.Client
}

// NewChannelSender creates a new channel sender.
func NewChannelSender() *ChannelSender {
	return &ChannelSender{
		client: &http.Client{Timeout: webhookTimeout},
	}
}

// SendResult contains the outcome of a send operation.
type SendResult struct {
	Success bool
	Error   error
}

// Send dispatches an alert to a channel based on its type.
func (s *ChannelSender) Send(ctx context.Context, ch config.Channel, payload AlertPayload) SendResult {
	var err error

	switch config.ChannelType(strings.ToLower(strings.TrimSpace(ch.Type))) {
	case config.ChannelTypeDiscord:
		err = s.sendDiscord(ctx, ch.WebhookURL, payload.WebhookMessage)
	case config.ChannelTypeSlack:
		err = s.sendSlack(ctx, ch.WebhookURL, payload.WebhookMessage)
	case config.ChannelTypeEmail:
		err = s.sendEmail(ctx, ch, payload)
	default:
		err = fmt.Errorf("unsupported channel type: %s", ch.Type)
	}

	return SendResult{
		Success: err == nil,
		Error:   err,
	}
}

func (s *ChannelSender) sendDiscord(ctx context.Context, webhookURL, message string) error {
	if strings.TrimSpace(webhookURL) == "" {
		return errors.New("empty discord webhook_url")
	}
	return s.postJSON(ctx, webhookURL, map[string]string{"content": message})
}

func (s *ChannelSender) sendSlack(ctx context.Context, webhookURL, message string) error {
	if strings.TrimSpace(webhookURL) == "" {
		return errors.New("empty slack webhook_url")
	}
	return s.postJSON(ctx, webhookURL, map[string]string{"text": message})
}

func (s *ChannelSender) sendEmail(ctx context.Context, ch config.Channel, payload AlertPayload) error {
	subject := payload.EmailSubject
	body := payload.EmailBody

	// Fall back to webhook message if email-specific content is missing
	if strings.TrimSpace(subject) == "" {
		subject = "[ALERT] " + payload.Kind
	}
	if strings.TrimSpace(body) == "" {
		body = payload.WebhookMessage
	}

	return sendEmail(ctx, ch, subject, body)
}

func (s *ChannelSender) postJSON(ctx context.Context, url string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("non-2xx status: %s", resp.Status)
	}

	return nil
}
