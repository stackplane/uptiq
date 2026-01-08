// Package alerting provides alert routing and dispatch.
package alerting

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"sitelert/internal/checks"
	"sitelert/internal/config"
)

// Engine manages alert state and dispatches notifications.
type Engine struct {
	log      *slog.Logger
	channels map[string]config.Channel
	router   *Router
	state    *StateManager
	sender   *ChannelSender
	messages *MessageBuilder
}

// NewEngine creates an alerting engine from configuration.
func NewEngine(cfg config.AlertingConfig, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}

	return &Engine{
		log:      log,
		channels: cfg.Channels,
		router:   NewRouter(cfg),
		state:    NewStateManager(),
		sender:   NewChannelSender(),
		messages: NewMessageBuilder(),
	}
}

// HandleResult processes a check result and sends alerts as needed.
func (e *Engine) HandleResult(svc config.Service, res checks.Result) {
	route := e.router.Resolve(svc.ID)
	if !route.Valid {
		return // No routing configured for this service
	}

	now := time.Now()
	var payload *AlertPayload

	e.state.WithState(svc.ID, func(st *ServiceState) {
		st.LastResultAt = now

		if res.Success {
			payload = e.handleSuccess(svc, res, st, route)
		} else {
			payload = e.handleFailure(svc, res, st, route, now)
		}
	})

	if payload != nil {
		e.dispatch(route.Channels, svc, *payload)
	}
}

func (e *Engine) handleSuccess(svc config.Service, res checks.Result, st *ServiceState, route ResolvedRoute) *AlertPayload {
	st.ConsecutiveFailures = 0

	// Send recovery alert if transitioning from DOWN and we had notified
	if st.State == StateDown && route.Policy.RecoveryAlert && st.DownNotified {
		payload := e.messages.RecoveryAlert(svc, res)
		st.DownNotified = false
		st.State = StateUp
		return &payload
	}

	st.DownNotified = false
	st.State = StateUp
	return nil
}

func (e *Engine) handleFailure(svc config.Service, res checks.Result, st *ServiceState, route ResolvedRoute, now time.Time) *AlertPayload {
	st.ConsecutiveFailures++

	// Not yet considered "down" until threshold reached
	if st.ConsecutiveFailures < route.Policy.FailureThreshold {
		if st.State == StateUnknown {
			st.State = StateUp
		}
		return nil
	}

	// Threshold reached: service is DOWN
	wasDown := st.State == StateDown
	st.State = StateDown

	canSendAlert := e.canSendDownAlert(st, route.Policy, now)

	// First DOWN alert of an outage
	if !st.DownNotified && canSendAlert {
		st.DownNotified = true
		st.LastDownAlertAt = now
		payload := e.messages.DownAlert(svc, res, st.ConsecutiveFailures, route.Policy.FailureThreshold)
		return &payload
	}

	// Reminder while still down (cooldown elapsed)
	if wasDown && st.DownNotified && canSendAlert {
		st.LastDownAlertAt = now
		payload := e.messages.StillDownAlert(svc, res, st.ConsecutiveFailures, route.Policy.FailureThreshold)
		return &payload
	}

	return nil
}

func (e *Engine) canSendDownAlert(st *ServiceState, policy ResolvedPolicy, now time.Time) bool {
	if policy.Cooldown <= 0 {
		return true
	}
	if st.LastDownAlertAt.IsZero() {
		return true
	}
	return now.Sub(st.LastDownAlertAt) >= policy.Cooldown
}

func (e *Engine) dispatch(channelNames []string, svc config.Service, payload AlertPayload) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, name := range channelNames {
		ch, ok := e.channels[name]
		if !ok {
			e.log.Warn("alert channel missing",
				"channel", name,
				"service_id", svc.ID,
				"service_name", svc.Name,
			)
			continue
		}

		result := e.sender.Send(ctx, ch, payload)
		e.logSendResult(name, ch, svc, payload.Kind, result)
	}
}

func (e *Engine) logSendResult(channelName string, ch config.Channel, svc config.Service, kind string, result SendResult) {
	channelType := strings.ToLower(strings.TrimSpace(ch.Type))
	baseFields := []any{
		"channel", channelName,
		"service_id", svc.ID,
		"service_name", svc.Name,
		"kind", kind,
	}

	// Add type-specific fields
	switch channelType {
	case "email":
		authConfigured := strings.TrimSpace(ch.Username) != "" || strings.TrimSpace(ch.Password) != ""
		baseFields = append(baseFields,
			"smtp_host", ch.SMTPHost,
			"smtp_port", ch.SMTPPort,
			"auth", authConfigured,
			"to_count", len(ch.To),
		)
	}

	if result.Success {
		e.log.Info(fmt.Sprintf("%s alert sent", channelType), baseFields...)
	} else {
		baseFields = append(baseFields, "error", result.Error.Error())
		e.log.Warn(fmt.Sprintf("%s send failed", channelType), baseFields...)
	}
}
