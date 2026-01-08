package alerting

import (
	"fmt"
	"net"
	"strings"
	"time"

	"sitelert/internal/checks"
	"sitelert/internal/config"
)

// AlertPayload contains formatted alert content for all channel types.
type AlertPayload struct {
	Kind           string // "down" | "recovery"
	WebhookMessage string // For Discord/Slack
	EmailSubject   string
	EmailBody      string
}

// MessageBuilder creates alert messages for different scenarios.
type MessageBuilder struct{}

// NewMessageBuilder creates a new message builder.
func NewMessageBuilder() *MessageBuilder {
	return &MessageBuilder{}
}

// DownAlert creates an alert payload for a service going down.
func (b *MessageBuilder) DownAlert(svc config.Service, res checks.Result, failures, threshold int) AlertPayload {
	return AlertPayload{
		Kind:           "down",
		WebhookMessage: b.formatDownWebhook(svc, res, failures, threshold, false),
		EmailSubject:   b.formatDownSubject(svc, false),
		EmailBody:      b.formatDownBody(svc, res, failures, threshold),
	}
}

// StillDownAlert creates an alert payload for a service that remains down.
func (b *MessageBuilder) StillDownAlert(svc config.Service, res checks.Result, failures, threshold int) AlertPayload {
	return AlertPayload{
		Kind:           "down",
		WebhookMessage: b.formatDownWebhook(svc, res, failures, threshold, true),
		EmailSubject:   b.formatDownSubject(svc, true),
		EmailBody:      b.formatDownBody(svc, res, failures, threshold),
	}
}

// RecoveryAlert creates an alert payload for a service recovering.
func (b *MessageBuilder) RecoveryAlert(svc config.Service, res checks.Result) AlertPayload {
	return AlertPayload{
		Kind:           "recovery",
		WebhookMessage: b.formatRecoveryWebhook(svc, res),
		EmailSubject:   fmt.Sprintf("[UP] %s (%s)", svc.Name, svc.ID),
		EmailBody:      b.formatRecoveryBody(svc, res),
	}
}

func (b *MessageBuilder) formatDownSubject(svc config.Service, stillDown bool) string {
	if stillDown {
		return fmt.Sprintf("[DOWN] %s (%s) (still down)", svc.Name, svc.ID)
	}
	return fmt.Sprintf("[DOWN] %s (%s)", svc.Name, svc.ID)
}

func (b *MessageBuilder) formatDownWebhook(svc config.Service, res checks.Result, failures, threshold int, reminder bool) string {
	var sb strings.Builder

	prefix := "🚨 DOWN"
	if reminder {
		prefix = "🚨 STILL DOWN"
	}

	// Header line
	sb.WriteString(fmt.Sprintf("%s: %s (%s) [%s]",
		prefix, svc.Name, svc.ID, strings.ToLower(svc.Type)))

	if threshold > 1 {
		sb.WriteString(fmt.Sprintf(" (failure=%d/%d)", failures, threshold))
	}
	sb.WriteString("\n")

	// Details line
	sb.WriteString(fmt.Sprintf("target=%s", targetForService(svc)))
	if res.StatusCode != 0 {
		sb.WriteString(fmt.Sprintf(" status=%d", res.StatusCode))
	}
	sb.WriteString(fmt.Sprintf(" latency=%dms at=%s",
		res.Latency.Milliseconds(), time.Now().Format(time.RFC3339)))

	if err := strings.TrimSpace(res.Error); err != "" {
		sb.WriteString(fmt.Sprintf(" err=%q", truncate(err, 180)))
	}

	return sb.String()
}

func (b *MessageBuilder) formatDownBody(svc config.Service, res checks.Result, failures, threshold int) string {
	var sb strings.Builder

	sb.WriteString("ALERT: SERVICE DOWN\n\n")
	sb.WriteString(fmt.Sprintf("Service: %s\n", svc.Name))
	sb.WriteString(fmt.Sprintf("ID: %s\n", svc.ID))
	sb.WriteString(fmt.Sprintf("Type: %s\n", strings.ToLower(svc.Type)))
	sb.WriteString(fmt.Sprintf("Target: %s\n", targetForService(svc)))

	if res.StatusCode != 0 {
		sb.WriteString(fmt.Sprintf("HTTP Status: %d\n", res.StatusCode))
	}
	sb.WriteString(fmt.Sprintf("Latency: %dms\n", res.Latency.Milliseconds()))

	if threshold > 1 {
		sb.WriteString(fmt.Sprintf("Consecutive failures: %d/%d\n", failures, threshold))
	}
	if err := strings.TrimSpace(res.Error); err != "" {
		sb.WriteString(fmt.Sprintf("Error: %s\n", err))
	}

	sb.WriteString(fmt.Sprintf("\nTime: %s\n", time.Now().Format(time.RFC3339)))
	sb.WriteString("\nNext steps:\n")
	sb.WriteString("- Check service health and recent deploys\n")
	sb.WriteString("- Verify DNS/network reachability\n")
	sb.WriteString("- Review logs / monitoring dashboards\n")

	return sb.String()
}

func (b *MessageBuilder) formatRecoveryWebhook(svc config.Service, res checks.Result) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("✅ UP: %s (%s) [%s]\n",
		svc.Name, svc.ID, strings.ToLower(svc.Type)))
	sb.WriteString(fmt.Sprintf("target=%s", targetForService(svc)))

	if res.StatusCode != 0 {
		sb.WriteString(fmt.Sprintf(" status=%d", res.StatusCode))
	}
	sb.WriteString(fmt.Sprintf(" latency=%dms at=%s",
		res.Latency.Milliseconds(), time.Now().Format(time.RFC3339)))

	return sb.String()
}

func (b *MessageBuilder) formatRecoveryBody(svc config.Service, res checks.Result) string {
	var sb strings.Builder

	sb.WriteString("RECOVERY: SERVICE UP\n\n")
	sb.WriteString(fmt.Sprintf("Service: %s\n", svc.Name))
	sb.WriteString(fmt.Sprintf("ID: %s\n", svc.ID))
	sb.WriteString(fmt.Sprintf("Type: %s\n", strings.ToLower(svc.Type)))
	sb.WriteString(fmt.Sprintf("Target: %s\n", targetForService(svc)))

	if res.StatusCode != 0 {
		sb.WriteString(fmt.Sprintf("HTTP Status: %d\n", res.StatusCode))
	}
	sb.WriteString(fmt.Sprintf("Latency: %dms\n", res.Latency.Milliseconds()))
	sb.WriteString(fmt.Sprintf("\nTime: %s\n", time.Now().Format(time.RFC3339)))

	return sb.String()
}

// targetForService returns the target URL/address for a service.
func targetForService(svc config.Service) string {
	if svc.IsHTTP() {
		return svc.URL
	}
	if svc.IsTCP() {
		return net.JoinHostPort(svc.Host, fmt.Sprintf("%d", svc.Port))
	}
	return ""
}

// truncate shortens a string to maxLen, adding ellipsis if truncated.
func truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
