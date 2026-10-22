package queryattr

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/Silo-Server/silo-server/internal/telemetry"
)

const (
	eventInterval   = time.Second
	eventBurst      = 10
	eventCauseSlow  = "slow"
	eventCauseLarge = "large"
)

var eventLimiter = rate.NewLimiter(rate.Every(eventInterval), eventBurst)
var suppressedTotal, pending atomic.Uint64

func (o *Op) event(ctx context.Context, err error, ns int64, now time.Time, inRequest bool) {
	if o.quiet {
		return
	}
	slow := ns > int64(100*time.Millisecond)
	large := o.args > 1000 || o.maxArray > 1000 || o.kind == KindBatch && o.statements > 100
	var cause string
	switch {
	case slow && large:
		cause = eventCauseSlow + "," + eventCauseLarge
	case slow:
		cause = eventCauseSlow
	case large:
		cause = eventCauseLarge
	default:
		return
	}
	if !eventLimiter.AllowN(now, 1) {
		suppressedTotal.Add(1)
		pending.Add(1)
		return
	}
	write(ctx, slog.LevelInfo, "slow or large database query",
		slog.String("component", "database"), slog.String("cause", cause), slog.String(siteLabel, o.Site()), slog.String("shape", o.Shape()),
		slog.Int64("ns", ns), slog.Int("statements", o.statements), slog.Int("args", o.args), slog.Int("max_array", o.maxArray),
		slog.String("outcome", telemetry.Outcome(err)), slog.Bool("request", inRequest), slog.Uint64("suppressed", pending.Swap(0)))
}
