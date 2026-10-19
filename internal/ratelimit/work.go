package ratelimit

import (
	"context"
	"time"
)

// WorkLimit caps charged permits in both aligned fixed windows. It has no
// additional burst allowance and is independent of the HTTP limiter settings.
type WorkLimit struct {
	PerSecond int `json:"per_second"`
	PerMinute int `json:"per_minute"`
}

// WorkResult grants a permit only when Allowed is true and Unavailable is false.
// An unavailable or canceled decision may have spent credit; it must not be
// retried as an uncharged operation or refunded.
type WorkResult struct {
	Allowed     bool
	Unavailable bool
	RetryAfter  time.Duration
}

// WorkStore atomically checks and increments both windows for one scope. Stores
// must honor ctx; callers may share one deadline across several scope decisions.
// Separate calls are conservative, with no cross-scope transaction or refunds.
type WorkStore interface {
	AllowWork(context.Context, string, WorkLimit) WorkResult
}

func validWorkLimit(limit WorkLimit) bool {
	// Keep integer increments exact in Lua as well as on supported Go platforms.
	return limit.PerSecond > 0 && limit.PerSecond <= MaxBurst &&
		limit.PerMinute > 0 && limit.PerMinute <= MaxBurst
}

func workUnavailable() WorkResult { return WorkResult{Unavailable: true} }
