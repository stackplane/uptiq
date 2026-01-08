package config

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	// idRegex validates service IDs contain only safe characters
	idRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
)

// ValidationError contains multiple validation failures
type ValidationError struct {
	Errors []string
}

func (e ValidationError) Error() string {
	return "config validation failed:\n- " + strings.Join(e.Errors, "\n- ")
}

// Validate checks the entire configuration for errors
func (cfg *Config) Validate() error {
	v := &validator{}

	v.validateGlobal(cfg.Global)
	v.validateServices(cfg.Services)
	v.validateAlerting(cfg.Alerting)

	if len(v.errors) > 0 {
		sort.Strings(v.errors)
		return ValidationError{Errors: v.errors}
	}
	return nil
}

// validator collects validation errors
type validator struct {
	errors []string
}

func (v *validator) addError(format string, args ...any) {
	v.errors = append(v.errors, fmt.Sprintf(format, args...))
}

func (v *validator) validateGlobal(global GlobalConfig) {
	if _, _, err := net.SplitHostPort(global.ScrapeBind); err != nil {
		v.addError("global.scrape_bind must be a valid host:port (got %q): %v", global.ScrapeBind, err)
	}

	v.validateDuration("global.default_timeout", global.DefaultTimeout)
	v.validateDuration("global.default_interval", global.DefaultInterval)
	v.validateDuration("global.jitter", global.Jitter)

	if global.WorkerCount < MinWorkerCount || global.WorkerCount > MaxWorkerCount {
		v.addError("global.worker_count must be between %d and %d (got %d)", MinWorkerCount, MaxWorkerCount, global.WorkerCount)
	}
}

func (v *validator) validateDuration(field, value string) {
	if _, err := time.ParseDuration(value); err != nil {
		v.addError("%s must be a valid duration %q: %v", field, value, err)
	}
}

func (v *validator) validateServices(services []Service) {
	seen := make(map[string]struct{})

	for i, svc := range services {
		prefix := fmt.Sprintf("services[%d]", i)
		v.validateService(prefix, svc, seen)
	}
}

func (v *validator) validateService(prefix string, svc Service, seenIDs map[string]struct{}) {
	if svc.ID == "" {
		v.addError("%s.id is required", prefix)
	} else {
		if !idRegex.MatchString(svc.ID) {
			v.addError("%s.id %q contains invalid characters (use letters, numbers, underscores, or hyphens)", prefix, svc.ID)
		}
		if _, exists := seenIDs[svc.ID]; exists {
			v.addError("%s.id %q is duplicated", prefix, svc.ID)
		}
		seenIDs[svc.ID] = struct{}{}
	}

	if svc.Name == "" {
		v.addError("%s.name is required", prefix)
	}

	switch strings.ToLower(svc.Type) {
	case string(ServiceTypeHTTP):
		v.validateHTTPService(prefix, svc)
	case string(ServiceTypeTCP):
		v.validateTCPService(prefix, svc)
	default:
		v.addError("%s.type must be 'http' or 'tcp' (got %q)", prefix, svc.Type)
	}

	v.validateDuration(prefix+".interval", svc.Interval)
	v.validateDuration(prefix+".timeout", svc.Timeout)
}

func (v *validator) validateHTTPService(prefix string, svc Service) {
	if svc.URL == "" {
		v.addError("%s.url is required for type=http", prefix)
	}
}

func (v *validator) validateTCPService(prefix string, svc Service) {
	if svc.Host == "" {
		v.addError("%s.host is required for type=tcp", prefix)
	}
	if svc.Port < MinPort || svc.Port > MaxPort {
		v.addError("%s.port must be between %d and %d for type=tcp (got %d)", prefix, MinPort, MaxPort, svc.Port)
	}
}

func (v *validator) validateAlerting(alerting AlertingConfig) {
	v.validateChannels(alerting.Channels)
	v.validateRoutes(alerting.Routes, alerting.Channels)
}

func (v *validator) validateChannels(channels map[string]Channel) {
	for name, ch := range channels {
		if name == "" {
			v.addError("alerting.channels contains an empty name")
			continue
		}

		prefix := fmt.Sprintf("alerting.channels[%q]", name)
		v.validateChannel(prefix, name, ch)
	}
}

func (v *validator) validateChannel(prefix, name string, ch Channel) {
	switch ChannelType(strings.ToLower(ch.Type)) {
	case "":
		// Empty line is allowed (no alerting)
	case ChannelTypeDiscord, ChannelTypeSlack:
		if strings.TrimSpace(ch.WebhookURL) == "" {
			v.addError("%s.webhook_url is required for type=%s", prefix, ch.Type)
		}
	case ChannelTypeEmail:
		v.validateEmailChannel(prefix, name, ch)
	default:
		v.addError("%s.type must be 'discord', 'slack', or 'email' (got %q)", prefix, ch.Type)
	}
}

func (v *validator) validateEmailChannel(prefix, name string, ch Channel) {
	if strings.TrimSpace(ch.SMTPHost) == "" {
		v.addError("%s.smtp_host is required for type=email", prefix)
	}
	if ch.SMTPPort == 0 {
		v.addError("%s.smtp_port is required for type=email", prefix)
	}
	if strings.TrimSpace(ch.From) == "" {
		v.addError("%s.from is required for type=email", prefix)
	}
	if len(ch.To) == 0 {
		v.addError("%s.to must contain at least one recipient for type=email", prefix)
	}

	hasUsername := strings.TrimSpace(ch.Username) != ""
	hasPassword := strings.TrimSpace(ch.Password) != ""
	if hasUsername != hasPassword {
		v.addError("alerting.channels.%s: username and password must both be set or both be empty for type=email", name)
	}
}

func (v *validator) validateRoutes(routes []Route, channels map[string]Channel) {
	if len(routes) > 0 && len(channels) == 0 {
		v.addError("alerting.routes defined but no alerting.channels exist")
	}

	for i, r := range routes {
		prefix := fmt.Sprintf("alerting.routes[%d]", i)

		for _, chName := range r.Notify {
			if _, exists := channels[chName]; !exists {
				v.addError("%s.notify references undefined channel %q", prefix, chName)
			}
		}

		if r.Policy.Cooldown != "" {
			v.validateDuration(prefix+".policy.cooldown", r.Policy.Cooldown)
		}
	}
}

func isSafeID(id string) bool {
	return idRegex.MatchString(id)
}
