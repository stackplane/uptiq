package config

const (
	DefaultScrapeBind  = "0.0.0.0:8080"
	DefaultLogLevel    = "info"
	DefaultTimeout     = "5s"
	DefaultInterval    = "30s"
	DefaultWorkerCount = 10
	DefaultJitter      = "0s"
	DefaultHTTPMethod  = "GET"

	MinWorkerCount = 1
	MaxWorkerCount = 1000
	MinPort        = 1
	MaxPort        = 65535
)

func applyDefaults(cfg *Config) {
	applyGlobalDefaults(&cfg.Global)
	applyServiceDefaults(cfg)
}

func applyGlobalDefaults(global *GlobalConfig) {
	if global.ScrapeBind == "" {
		global.ScrapeBind = DefaultScrapeBind
	}
	if global.LogLevel == "" {
		global.LogLevel = DefaultLogLevel
	}
	if global.DefaultTimeout == "" {
		global.DefaultTimeout = DefaultTimeout
	}
	if global.DefaultInterval == "" {
		global.DefaultInterval = DefaultInterval
	}
	if global.WorkerCount == 0 {
		global.WorkerCount = DefaultWorkerCount
	}
	if global.Jitter == "" {
		global.Jitter = DefaultJitter
	}
}

func applyServiceDefaults(cfg *Config) {
	for i := range cfg.Services {
		svc := &cfg.Services[i]

		if svc.Timeout == "" {
			svc.Timeout = cfg.Global.DefaultTimeout
		}
		if svc.Interval == "" {
			svc.Interval = cfg.Global.DefaultInterval
		}
		if svc.Method == "" && svc.IsHTTP() {
			svc.Method = DefaultHTTPMethod
		}
	}
}
