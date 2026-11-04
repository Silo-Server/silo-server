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
)

// TestEventCatalogMatchesProducers projects what each producer of a catalog
// event actually publishes into the SDK's public payload type, and checks a
// field the producer set survives. A producer that renames or retypes a field
// breaks the plugin contract; this catches it. Every catalog event needs a
// case, so an event added to the SDK catalog gets one too.
func TestEventCatalogMatchesProducers(t *testing.T) {
	at := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	season := 2
	published := publishedByNotificationsHub(t)

	scan := func(status string) []byte {
		return mustJSON(t, evt.ScanRun{ID: "scan-1", LibraryID: 7, Mode: "full", Trigger: "manual", Status: status, StartedAt: &at,
			Result: &evt.ScanRunResult{New: 3, Missing: 1}})
	}
	session := mustJSON(t, apiv2.AdminPlaybackLifecycleEvent{SessionID: "s1", Seq: 4, At: apiv2.NewInstant(at), State: "playing",
		Session: &apiv2.AdminPlaybackSession{SessionID: "s1", UserID: "7", Username: "josh", ProfileID: "p1", MediaTitle: "Heat",
			StartedAt: apiv2.NewInstant(at), UpdatedAt: apiv2.NewInstant(at)}})
	stopped := mustJSON(t, apiv2.AdminPlaybackLifecycleEvent{SessionID: "s1", Seq: 5, At: apiv2.NewInstant(at), State: "stopped",
		History: &apiv2.AdminPlaybackHistoryEntry{SessionID: "s1", UserID: "7", MediaTitle: "Heat",
			StartedAt: apiv2.NewInstant(at), EndedAt: apiv2.NewInstant(at.Add(time.Hour))}})
	notification := mustJSON(t, notifications.PayloadForRow(notifications.DeliveryRow{
		Delivery:    notifications.Delivery{ID: "d1", Type: "episode.available", UserID: 7, ProfileID: "p1", CreatedAt: at},
		SeriesTitle: "Severance", SeasonNumber: &season,
	}))

	cases := map[string]struct {
		raw   []byte
		check func(payload any) bool
	}{
		publicevents.CatalogLibraryChanged: {published[publicevents.CatalogLibraryChanged], func(p any) bool {
			return p.(*publicevents.LibraryChanged).LibraryID == 7 && p.(*publicevents.LibraryChanged).New == 3
		}},
		publicevents.CatalogItemChanged: {published[publicevents.CatalogItemChanged], func(p any) bool {
			return p.(*publicevents.ItemChanged).ContentID == "c1" && p.(*publicevents.ItemChanged).Change == "artwork"
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
		publicevents.JobCreated:    {published[publicevents.JobFailed], jobCheck},
		publicevents.JobCompleted:  {published[publicevents.JobFailed], jobCheck},
		publicevents.JobFailed:     {published[publicevents.JobFailed], jobCheck},
		publicevents.JobCancelled:  {published[publicevents.JobFailed], jobCheck},
		publicevents.NotificationCreated: {notification, func(p any) bool {
			n := p.(*publicevents.Notification)
			return n.ID == "d1" && n.SeriesTitle == "Severance" && n.SeasonNumber != nil && *n.SeasonNumber == 2 && n.CreatedAt.Equal(at)
		}},
	}

	for _, definition := range publicevents.Catalog() {
		tc, ok := cases[definition.Name]
		if !ok {
			t.Errorf("%s: no producer case; add one", definition.Name)
			continue
		}
		if len(tc.raw) == 0 {
			t.Errorf("%s: the producer published nothing", definition.Name)
			continue
		}
		projected, err := publicevents.Project(definition.Name, tc.raw)
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
	return scan.LibraryID == 7 && scan.Result != nil && scan.Result.New == 3 && scan.StartedAt != nil
}

func jobCheck(p any) bool {
	job := p.(*publicevents.JobEvent)
	return job.ID == "j1" && job.JobType == "library_refresh" && job.Status == "failed" && job.ErrorMessage == "boom"
}

// publishedByNotificationsHub runs the catalog and job publishers that build
// their payloads internally and returns each envelope's data by event name.
func publishedByNotificationsHub(t *testing.T) map[string][]byte {
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
	job := &models.AdminJob{ID: "j1", JobType: "library_refresh", Status: "failed", ErrorMessage: "boom",
		RequestPayload: json.RawMessage(`{"library_id":7}`), RequestedAt: now, UpdatedAt: now}
	if err := hub.PublishJob(ctx, notifications.TypeJobFailed, job); err != nil {
		t.Fatal(err)
	}

	out := map[string][]byte{}
	for {
		select {
		case env := <-envelopes:
			if _, seen := out[env.Event]; !seen {
				out[env.Event] = env.Data
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
