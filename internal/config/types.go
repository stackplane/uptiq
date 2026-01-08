package config

// Config is the root configuration structure for sitelert.
type Config struct {
	Global   GlobalConfig   `yaml:"global"`
	Services []Service      `yaml:"services"`
	Alerting AlertingConfig `yaml:"alerting"`
}

// GlobalConfig contains daemon-wide settings.
type GlobalConfig struct {
	ScrapeBind      string `yaml:"scrape_bind"`
	LogLevel        string `yaml:"log_level"`
	DefaultTimeout  string `yaml:"default_timeout"`
	DefaultInterval string `yaml:"default_interval"`
	WorkerCount     int    `yaml:"worker_count"`
	Jitter          string `yaml:"jitter"`
}

// ServiceType represents the type of service check.
type ServiceType string

const (
	ServiceTypeHTTP ServiceType = "http"
	ServiceTypeTCP  ServiceType = "tcp"
)

type Service struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name"`
	Type string `yaml:"type"` // "http" or "tcp"

	// HTTP-specific fields
	URL            string            `yaml:"url"`
	Method         string            `yaml:"method"`
	ExpectedStatus []int             `yaml:"expected_status"`
	Contains       string            `yaml:"contains"`
	Headers        map[string]string `yaml:"headers"`

	// TCP-specific fields
	Host string `yaml:"host"`
	Port int    `yaml:"port"`

	Interval string `yaml:"interval"`
	Timeout  string `yaml:"timeout"`
}

// IsHTTP returns true if the service type is HTTP.
func (s Service) IsHTTP() bool {
	return ServiceType(s.Type) == ServiceTypeHTTP
}

// IsTCP returns true if the service type is TCP.
func (s Service) IsTCP() bool {
	return ServiceType(s.Type) == ServiceTypeTCP
}

// AlertingConfig holds all alerting-related configuration.
type AlertingConfig struct {
	Channels map[string]Channel `yaml:"channels"`
	Routes   []Route            `yaml:"routes"`
}

// ChannelType represents the type of notification channel.
type ChannelType string

const (
	ChannelTypeDiscord ChannelType = "discord"
	ChannelTypeSlack   ChannelType = "slack"
	ChannelTypeEmail   ChannelType = "email"
)

// Channel supports multiple types; keep a superset of fields.
type Channel struct {
	Type       string `yaml:"type"` // "discord" | "slack" | "email"
	WebhookURL string `yaml:"webhook_url"`

	// Email-specific fields
	SMTPHost string   `yaml:"smtp_host"`
	SMTPPort int      `yaml:"smtp_port"`
	Username string   `yaml:"username"`
	Password string   `yaml:"password"`
	From     string   `yaml:"from"`
	To       []string `yaml:"to"`
}

// Route defines how alerts are routed based on service matches.
type Route struct {
	Match  RouteMatch  `yaml:"match"`
	Policy RoutePolicy `yaml:"policy"`
	Notify []string    `yaml:"notify"`
}

// RouteMatch specifies which services a route applies to.
type RouteMatch struct {
	ServiceIDs []string `yaml:"service_ids"`
}

// RoutePolicy controls alerting behavior for a route.
type RoutePolicy struct {
	FailureThreshold int    `yaml:"failure_threshold"`
	Cooldown         string `yaml:"cooldown"`
	RecoveryAlert    bool   `yaml:"recovery_alert"`
}
