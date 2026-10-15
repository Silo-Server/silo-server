package nodecontrol

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/logthrottle"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

// reportFailureLogInterval spaces the warning about failing reports: the
// node reports every second, and a core that is down would otherwise fill the
// node's log with one line per report.
const reportFailureLogInterval = time.Minute

// conflictWarnAfter is how many reports in a row the core may refuse as not
// newer (409) before the reporter warns. One refusal is a retried or
// reordered report; this many in a row mean none of this node's reports pass
// the fence. Either the clock was set back across a restart (the boot id is
// below the last one the core accepted, which it remembers for
// reportFenceTTL), or a second process runs with this node's key.
const conflictWarnAfter = 10

// Reporter sends a transcode node's sessions and stream telemetry to the core
// every ReportInterval, in place of the node writing them to Redis.
type Reporter struct {
	client *Client
	bootID string
	seq    atomic.Uint64
	// sessions lists the node's active sessions, newest first.
	sessions func() []nodesessions.SessionInfo
	// telemetry sweeps the node's stream telemetry; nil sends none.
	telemetry func() streamtelemetry.Snapshot

	// warnings spaces the failure and conflict warnings, keyed by kind.
	warnings  *logthrottle.Keyed[string]
	failures  int
	conflicts int
}

// NewReporter returns the reporter of the node client signs for. The boot id
// is now, in Unix nanoseconds: a restarted node's reports always pass the
// core's fence, and replays of its previous process's never do.
func NewReporter(client *Client, sessions func() []nodesessions.SessionInfo, telemetry func() streamtelemetry.Snapshot) *Reporter {
	return &Reporter{
		client:    client,
		bootID:    strconv.FormatInt(time.Now().UnixNano(), 10),
		sessions:  sessions,
		telemetry: telemetry,
		warnings:  logthrottle.New[string](reportFailureLogInterval),
	}
}

// Run reports every ReportInterval until ctx ends. A report the core refuses
// as not newer (409) is passed over, since the next one carries a higher
// sequence; conflictWarnAfter refusals in a row, and any other failure, are
// logged at most once a minute.
func (r *Reporter) Run(ctx context.Context) {
	ticker := time.NewTicker(ReportInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		r.reportOnce(ctx)
	}
}

func (r *Reporter) reportOnce(ctx context.Context) {
	body, err := r.next()
	if err == nil {
		callCtx, cancel := context.WithTimeout(ctx, requestTimeout)
		err = r.client.PostReport(callCtx, body)
		cancel()
	}
	if err == nil || ctx.Err() != nil {
		r.failures, r.conflicts = 0, 0
		return
	}
	if Status(err) == http.StatusConflict {
		r.failures = 0
		r.conflicts++
		if r.conflicts < conflictWarnAfter {
			return
		}
		if _, ok := r.warnings.Allow("conflict"); ok {
			slog.WarnContext(ctx, "the core keeps refusing this node's reports as not newer than one it accepted; "+
				"either this node's clock was set back across a restart, which clears within minutes, or another process runs with this node's key",
				"component", "nodecontrol", "consecutive_conflicts", r.conflicts)
		}
		return
	}
	r.conflicts = 0
	r.failures++
	if _, ok := r.warnings.Allow("failure"); ok {
		slog.WarnContext(ctx, "report to the core failed; the admin views miss this node's sessions until a report succeeds",
			"component", "nodecontrol", "consecutive_failures", r.failures, "error", err)
	}
}

// next builds the next report, within MaxBodyBytes: when the newest
// MaxReportSessions sessions and MaxReportTransfers transfers do not fit, it
// halves both until they do.
func (r *Reporter) next() ([]byte, error) {
	sessions := r.sessions()
	var snapshot streamtelemetry.Snapshot
	if r.telemetry != nil {
		snapshot = r.telemetry()
	}
	report := Report{BootID: r.bootID, Seq: r.seq.Add(1)}
	maxSessions, maxTransfers := MaxReportSessions, MaxReportTransfers
	for {
		report.Truncated = len(sessions) > maxSessions
		report.Sessions = sessions[:min(len(sessions), maxSessions)]
		report.Telemetry = nil
		if r.telemetry != nil {
			wire, omitted, err := streamtelemetry.EncodeNodeSnapshot(snapshot, maxSessions, maxTransfers)
			if err != nil {
				return nil, err
			}
			report.Telemetry = &wire
			report.Truncated = report.Truncated || omitted
		}
		body, err := json.Marshal(report)
		if err != nil {
			return nil, err
		}
		if len(body) <= MaxBodyBytes || (maxSessions == 0 && maxTransfers == 0) {
			return body, nil
		}
		maxSessions, maxTransfers = maxSessions/2, maxTransfers/2
	}
}

// Flush sends one report now. Call it once Run has returned, at shutdown, so
// the core drops the sessions the node ended instead of letting their records
// expire.
func (r *Reporter) Flush(ctx context.Context) error {
	body, err := r.next()
	if err != nil {
		return err
	}
	return r.client.PostReport(ctx, body)
}
