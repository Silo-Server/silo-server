package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

// adminAlertEvents maps failure event names to their notification titles.
// Keys are verified against real publishers:
//   - "job.failed"  → hub.go PublishJob on ChannelJobs (models.AdminJob payload)
//   - "scan.failed" → libraries.go markScanFailed on ChannelScans (evt.ScanRun payload)
//
// Absent (no real event): task.failed (only task.updated exists), plugin failure events.
var adminAlertEvents = map[string]string{
	"job.failed":  "Background job failed",
	"scan.failed": "Library scan failed",
}

// adminAlertChannels lists the only channels whose envelopes the admin matcher
// will process. Events arriving on any other channel are ignored even if the
// event name matches.
var adminAlertChannels = map[evt.EventChannel]bool{
	evt.ChannelJobs:  true,
	evt.ChannelScans: true,
	evt.ChannelTasks: true, // no task failure event exists yet (only task.updated); kept so a future task.failed event needs only a map entry
}

// matchAdmin fans failure events out to all admin users. A day-bucketed
// dedup_ref throttles repeats of the same failure to one notification per hour.
func (m *Materializer) matchAdmin(ctx context.Context, env evt.Envelope) error {
	if !adminAlertChannels[env.Channel] {
		return nil
	}

	title, ok := adminAlertEvents[env.Event]
	if !ok {
		return nil
	}

	// Best-effort decode of the failure payload to extract id and kind.
	// For job.failed the payload is models.AdminJob (json tags: "id", "job_type").
	// For scan.failed the payload is evt.ScanRun (json tags: "id", "mode").
	var payload struct {
		ID      string `json:"id"`
		JobType string `json:"job_type"` // models.AdminJob
		Mode    string `json:"mode"`     // evt.ScanRun
	}
	if len(env.Data) > 0 {
		_ = json.Unmarshal(env.Data, &payload)
	}

	// source is the "kind" of failure — job_type for jobs, mode for scans.
	source := payload.JobType
	if source == "" {
		source = payload.Mode
	}
	if source == "" {
		source = env.Event
	}

	body := source
	if payload.ID != "" {
		body = fmt.Sprintf("%s (%s)", source, payload.ID)
	}

	link := "/admin/tasks"

	// Hourly dedup bucket: one alert per (event, source) per clock-hour.
	dedupBucket := time.Now().UTC().Format("2006010215")
	dedupRef := fmt.Sprintf("%s:%s:%s", env.Event, source, dedupBucket)

	admins, err := m.svc.store.AdminUserIDs(ctx)
	if err != nil {
		return fmt.Errorf("matchAdmin: AdminUserIDs: %w", err)
	}

	for _, userID := range admins {
		if err := m.svc.Create(ctx, CreateInput{
			UserID:      userID,
			Category:    CategoryAdmin,
			Type:        env.Event,
			Title:       title,
			Body:        body,
			Link:        link,
			SourceEvent: env.Event,
			DedupRef:    dedupRef,
		}); err != nil {
			return fmt.Errorf("matchAdmin: Create for user %d: %w", userID, err)
		}
	}
	return nil
}
