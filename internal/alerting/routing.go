package alerting

import (
	"slices"
	"strings"
	"time"

	"sitelert/internal/config"
)

// ResolvedRoute contains the computed routing configuration for a service.
type ResolvedRoute struct {
	Channels []string
	Policy   ResolvedPolicy
	Valid    bool
}

// ResolvedPolicy contains the computed policy settings.
type ResolvedPolicy struct {
	FailureThreshold int
	Cooldown         time.Duration
	RecoveryAlert    bool
}

// compiledRoute is the internal representation of a route.
type compiledRoute struct {
	matchServiceIDs []string
	channels        []string
	policy          ResolvedPolicy
}

// Router resolves which channels and policies apply to services.
type Router struct {
	routes     []compiledRoute
	routeIndex map[string][]int // service_id -> route indices
}

// NewRouter creates a router from alerting configuration.
func NewRouter(cfg config.AlertingConfig) *Router {
	r := &Router{
		routeIndex: make(map[string][]int),
	}

	for i, route := range cfg.Routes {
		compiled := compiledRoute{
			matchServiceIDs: cleanStrings(route.Match.ServiceIDs),
			channels:        cleanStrings(route.Notify),
			policy:          compilePolicy(route.Policy),
		}
		r.routes = append(r.routes, compiled)

		// Index by service ID for fast lookup
		for _, id := range compiled.matchServiceIDs {
			if id != "" {
				r.routeIndex[id] = append(r.routeIndex[id], i)
			}
		}
	}

	return r
}

// Resolve finds all channels and merged policy for a service.
// When multiple routes match, policies are merged conservatively:
//   - failure_threshold: max (reduces spam)
//   - cooldown: max (reduces spam)
//   - recovery_alert: true if any route enables it
func (r *Router) Resolve(serviceID string) ResolvedRoute {
	indices := r.routeIndex[serviceID]
	if len(indices) == 0 {
		return ResolvedRoute{Valid: false}
	}

	var channels []string
	policy := ResolvedPolicy{FailureThreshold: 1} // baseline

	for i, idx := range indices {
		route := r.routes[idx]

		// Collect unique channels
		for _, ch := range route.channels {
			if !slices.Contains(channels, ch) {
				channels = append(channels, ch)
			}
		}

		// Merge policy (first route sets baseline, subsequent routes merge)
		if i == 0 {
			policy = route.policy
		} else {
			policy = mergePolicy(policy, route.policy)
		}
	}

	if len(channels) == 0 {
		return ResolvedRoute{Valid: false}
	}

	return ResolvedRoute{
		Channels: channels,
		Policy:   policy,
		Valid:    true,
	}
}

func compilePolicy(p config.RoutePolicy) ResolvedPolicy {
	resolved := ResolvedPolicy{
		FailureThreshold: 1,
		RecoveryAlert:    p.RecoveryAlert,
	}

	if p.FailureThreshold > 0 {
		resolved.FailureThreshold = p.FailureThreshold
	}

	if cooldown := strings.TrimSpace(p.Cooldown); cooldown != "" {
		if d, err := time.ParseDuration(cooldown); err == nil && d > 0 {
			resolved.Cooldown = d
		}
	}

	return resolved
}

func mergePolicy(base, other ResolvedPolicy) ResolvedPolicy {
	result := base

	if other.FailureThreshold > result.FailureThreshold {
		result.FailureThreshold = other.FailureThreshold
	}
	if other.Cooldown > result.Cooldown {
		result.Cooldown = other.Cooldown
	}
	if other.RecoveryAlert {
		result.RecoveryAlert = true
	}

	return result
}

// cleanStrings trims whitespace and removes empty strings.
func cleanStrings(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
