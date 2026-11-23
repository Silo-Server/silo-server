package taskmanager

import "time"

// SetRunLeaseTiming shortens run lease timing for tests. Call it before Start.
func (m *TaskManager) SetRunLeaseTiming(renewInterval, staleAfter time.Duration) {
	m.leaseRenewInterval = renewInterval
	m.leaseStaleAfter = staleAfter
}
