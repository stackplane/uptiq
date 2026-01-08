package alerting

import (
	"sync"
	"time"
)

// AlertState represents the current state of a service.
type AlertState string

const (
	StateUnknown AlertState = "UNKNOWN"
	StateUp      AlertState = "UP"
	StateDown    AlertState = "DOWN"
)

// ServiceState tracks the alert state for a single service.
type ServiceState struct {
	State               AlertState
	ConsecutiveFailures int
	LastDownAlertAt     time.Time
	DownNotified        bool // Whether we sent a DOWN alert for current outage
	LastResultAt        time.Time
}

// StateManager manages alert state for all services.
type StateManager struct {
	mu    sync.Mutex
	state map[string]*ServiceState
}

// NewStateManager creates a new state manager.
func NewStateManager() *StateManager {
	return &StateManager{
		state: make(map[string]*ServiceState),
	}
}

// Get returns the state for a service, creating it if needed.
func (m *StateManager) Get(serviceID string) *ServiceState {
	m.mu.Lock()
	defer m.mu.Unlock()

	if st, ok := m.state[serviceID]; ok {
		return st
	}

	st := &ServiceState{State: StateUnknown}
	m.state[serviceID] = st
	return st
}

// WithState executes a function while holding the state lock.
func (m *StateManager) WithState(serviceID string, fn func(*ServiceState)) {
	m.mu.Lock()
	defer m.mu.Unlock()

	st := m.state[serviceID]
	if st == nil {
		st = &ServiceState{State: StateUnknown}
		m.state[serviceID] = st
	}
	fn(st)
}
