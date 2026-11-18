package plugins_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	publicevents "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/events"

	"github.com/Silo-Server/silo-server/internal/apiv2"
	"github.com/Silo-Server/silo-server/internal/cache"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// TestEventCatalogMatchesProducers runs what each producer of a catalog event
// actually publishes through the dispatcher's projection into the SDK's
// public payload type, and checks the fields the producer set survive, IDs as
// strings. A producer that renames or retypes a field breaks the plugin
// contract; this catches it. Every catalog event needs a case, so an event
// added to the SDK catalog gets one too.
func TestEventCatalogMatchesProducers(t *testing.T) {
	at := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	season := 2
	libraryID := 3
	published := publishedByNotificationsHub(t)

	scan := func(status string) evt.Envelope {
		return envelope("scan", mustJSON(t, evt.ScanRun{ID: "scan-1", LibraryID: 7, Mode: "full", Trigger: "manual", Status: status, StartedAt: &at,
			Result: &evt.ScanRunResult{New: 3, Missing: 1}}))
	}
	session := envelope("session", mustJSON(t, apiv2.AdminPlaybackLifecycleEvent{SessionID: "s1", Seq: 4, At: apiv2.NewInstant(at), State: "playing",
		Session: &apiv2.AdminPlaybackSession{SessionID: "s1", UserID: "7", Username: "josh", ProfileID: "p1", MediaTitle: "Heat",
			StartedAt: apiv2.NewInstant(at), UpdatedAt: apiv2.NewInstant(at)}}))
	stopped := envelope("session", mustJSON(t, apiv2.AdminPlaybackLifecycleEvent{SessionID: "s1", Seq: 5, At: apiv2.NewInstant(at), State: "stopped",
		History: &apiv2.AdminPlaybackHistoryEntry{SessionID: "s1", UserID: "7", MediaTitle: "Heat",
			StartedAt: apiv2.NewInstant(at), EndedAt: apiv2.NewInstant(at.Add(time.Hour))}}))
	notification := envelope(publicevents.NotificationCreated, mustJSON(t, notifications.PayloadForRow(notifications.DeliveryRow{
		Delivery: notifications.Delivery{ID: "d1", Type: "episode.available", UserID: 7, ProfileID: "p1", LibraryID: &libraryID,
			CreatedAt: at},
		SeriesTitle: "Severance", SeasonNumber: &season,
	})))
	// The notifications dispatcher addresses the envelope to the account.
	notification.UserID = 7

	cases := map[string]struct {
		env   evt.Envelope
		check func(payload any) bool
	}{
		publicevents.CatalogLibraryChanged: {published[publicevents.CatalogLibraryChanged], func(p any) bool {
			return p.(*publicevents.LibraryChanged).LibraryID == "7" && p.(*publicevents.LibraryChanged).New == 3
		}},
		publicevents.CatalogItemChanged: {published[publicevents.CatalogItemChanged], func(p any) bool {
			item := p.(*publicevents.ItemChanged)
			return item.LibraryID == "7" && item.ContentID == "c1" && item.Change == "artwork"
		}},
		publicevents.SessionStarted: {session, sessionCheck("Heat")},
		publicevents.SessionPaused:  {session, sessionCheck("Heat")},
		publicevents.SessionResumed: {session, sessionCheck("Heat")},
		publicevents.SessionStopped: {stopped, func(p any) bool {
			history, err := p.(*publicevents.SessionEvent).DecodeHistory()
			return err == nil && history != nil && history.MediaTitle == "Heat" && history.EndedAt.Equal(at.Add(time.Hour))
		}},
		publicevents.ScanStarted:   {scan("running"), scanCheck},
		publicevents.ScanCompleted: {scan("completed"), scanCheck},
		publicevents.ScanFailed:    {scan("failed"), scanCheck},
		publicevents.ScanCancelled: {scan("canceled"), scanCheck},
		// job.created comes from a job the server queued itself: no creator.
		publicevents.JobCreated: {published[publicevents.JobCreated], func(p any) bool {
			job := p.(*publicevents.JobEvent)
			return job.ID == "j0" && job.CreatedByUserID == nil
		}},
		publicevents.JobCompleted: {published[publicevents.JobFailed], jobCheck},
		publicevents.JobFailed:    {published[publicevents.JobFailed], jobCheck},
		publicevents.JobCancelled: {published[publicevents.JobFailed], jobCheck},
		publicevents.NotificationCreated: {notification, func(p any) bool {
			n := p.(*publicevents.Notification)
			return n.ID == "d1" && n.UserID == "7" && n.ProfileID == "p1" && n.LibraryID != nil && *n.LibraryID == "3" &&
				n.SeriesTitle == "Severance" && n.SeasonNumber != nil && *n.SeasonNumber == 2 && n.CreatedAt.Equal(at)
		}},
	}

	for _, definition := range publicevents.Catalog() {
		tc, ok := cases[definition.Name]
		if !ok {
			t.Errorf("%s: no producer case; add one", definition.Name)
			continue
		}
		if len(tc.env.Data) == 0 {
			t.Errorf("%s: the producer published nothing", definition.Name)
			continue
		}
		tc.env.Event = definition.Name
		projected, err := plugins.PublicPayloadJSON(tc.env)
		if err != nil {
			t.Errorf("%s: %v", definition.Name, err)
			continue
		}
		payload := definition.NewPayload()
		if err := json.Unmarshal(projected, payload); err != nil || !tc.check(payload) {
			t.Errorf("%s: projected payload %s lost the producer's fields (%v)", definition.Name, projected, err)
		}
	}
}

func sessionCheck(title string) func(any) bool {
	return func(p any) bool {
		session, err := p.(*publicevents.SessionEvent).DecodeSession()
		return err == nil && session != nil && session.MediaTitle == title && session.UserID == "7" && p.(*publicevents.SessionEvent).Seq > 0
	}
}

func scanCheck(p any) bool {
	scan := p.(*publicevents.ScanEvent)
	return scan.LibraryID == "7" && scan.Result != nil && scan.Result.New == 3 && scan.StartedAt != nil
}

func jobCheck(p any) bool {
	job := p.(*publicevents.JobEvent)
	return job.ID == "j1" && job.JobType == "library_refresh" && job.Status == "failed" && job.ErrorMessage == "boom" &&
		job.CreatedByUserID != nil && *job.CreatedByUserID == "9"
}

func envelope(event string, data []byte) evt.Envelope {
	return evt.Envelope{Event: event, Data: data}
}

// publishedByNotificationsHub runs the catalog and job publishers that build
// their payloads internally and returns each envelope by event name.
func publishedByNotificationsHub(t *testing.T) map[string]evt.Envelope {
	t.Helper()
	ctx := context.Background()
	hub := notifications.NewHub("api", &cache.NoopEventBus{})
	envelopes, unsubscribe := hub.EventsHub().Subscribe()
	defer unsubscribe()

	if err := hub.PublishCatalogLibraryChanged(ctx, notifications.LibraryChangeEvent{LibraryID: 7, Reason: "scan", New: 3}); err != nil {
		t.Fatal(err)
	}
	if err := hub.PublishCatalogItemChanged(ctx, notifications.MetadataUpdateEvent{LibraryID: 7, ContentID: "c1", Change: "artwork"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	queued := &models.AdminJob{ID: "j0", JobType: "library_refresh", Status: "queued", RequestedAt: now, UpdatedAt: now}
	if err := hub.PublishJob(ctx, notifications.TypeJobCreated, queued); err != nil {
		t.Fatal(err)
	}
	failed := &models.AdminJob{ID: "j1", JobType: "library_refresh", Status: "failed", ErrorMessage: "boom", CreatedByUserID: 9,
		RequestPayload: json.RawMessage(`{"library_id":7}`), RequestedAt: now, UpdatedAt: now}
	if err := hub.PublishJob(ctx, notifications.TypeJobFailed, failed); err != nil {
		t.Fatal(err)
	}

	out := map[string]evt.Envelope{}
	for {
		select {
		case env := <-envelopes:
			if _, seen := out[env.Event]; !seen {
				out[env.Event] = env
			}
		default:
			return out
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
