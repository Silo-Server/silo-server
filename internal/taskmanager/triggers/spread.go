package triggers

import (
	"math/rand/v2"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

var (
	_ taskmanager.SpreadableTrigger = (*DailyTrigger)(nil)
	_ taskmanager.SpreadableTrigger = (*WeeklyTrigger)(nil)
)

// spreadStart delays a scheduled slot by a random offset in [0, spread)
// (taskmanager.StartSpreader). Callers pick the slot strictly after now
// before adding the offset, so a rearm inside the spread window waits for
// the next slot and a task runs at most once per slot.
func spreadStart(slot time.Time, spread time.Duration) time.Time {
	if spread <= 0 {
		return slot
	}
	return slot.Add(rand.N(spread)) //nolint:gosec // start jitter, not a secret
}
