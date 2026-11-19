package queryattr

import (
	"golang.org/x/time/rate"
)

func resetEvents() {
	eventLimiter = rate.NewLimiter(rate.Every(eventInterval), eventBurst)
	suppressedTotal.Store(0)
	pending.Store(0)
}
