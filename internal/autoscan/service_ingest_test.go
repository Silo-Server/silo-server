package autoscan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

func webhookTestStore() *fakeStore {
	return &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID:           "s1",
			PluginID:     BuiltinArrWebhookPluginID,
			CapabilityID: BuiltinArrWebhookCapabilityID,
			Enabled:      true,
			DeliveryMode: DeliveryModeWebhook,
		}},
	}
}

func TestIngestChangesEnqueuesLikePolling(t *testing.T) {
	store := webhookTestStore()
	prov := &fakeProvider{}
	q := &recordingQueuer{}
	svc := newService(store, prov, q, allowSuppressor{})

	result, err := svc.IngestChanges(context.Background(), ChangeIngest{
		SourceID:          "s1",
		ProviderEventType: "Download",
		Changes: []Change{
			{SourcePath: "/mnt/media/Show/Season 01/e01.mkv", Scope: ChangeScopeFile},
			{SourcePath: "/mnt/media/Show/Season 01/e02.mkv", Scope: ChangeScopeFile},
		},
	})
	if err != nil {
		t.Fatalf("IngestChanges: %v", err)
	}
	if result.Enqueued != 2 || result.Suppressed != 0 || result.Unresolved {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(q.enqueued) != 2 {
		t.Fatalf("enqueued = %+v, want 2 file targets", q.enqueued)
	}
	if prov.calls != 0 {
		t.Fatalf("provider must never be invoked for webhook ingestion, got %d calls", prov.calls)
	}
	if len(store.advanced) != 0 {
		t.Fatalf("webhook ingestion must not advance markers, got %+v", store.advanced)
	}
	if len(store.createdEvents) != 1 {
		t.Fatalf("created events = %+v, want 1", store.createdEvents)
	}
	created := store.createdEvents[0]
	if created.DeliveryMode != DeliveryModeWebhook || created.ProviderEventType != "Download" || !created.SkipRunningCheck {
		t.Fatalf("webhook event create misconfigured: %+v", created)
	}
	if len(store.events) != 1 || store.events[0].Status != EventStatusSuccess {
		t.Fatalf("finished events = %+v, want one success", store.events)
	}
	if store.events[0].MarkerAfter != "" {
		t.Fatalf("webhook events must not carry markers, got %q", store.events[0].MarkerAfter)
	}
	if len(store.deliveries) != 1 || len(store.completed) != 1 {
		t.Fatalf("durable lifecycle deliveries=%d completed=%v", len(store.deliveries), store.completed)
	}
}

type failOnceResolver struct {
	calls int
}

func (r *failOnceResolver) Resolve(ctx context.Context, req scantrigger.Request) (*scantrigger.Target, error) {
	r.calls++
	if r.calls == 1 {
		return nil, errors.New("temporary resolver failure")
	}
	return (fakeResolver{}).Resolve(ctx, req)
}

func (r *failOnceResolver) ResolveMissingSubtree(ctx context.Context, path, trigger string) (*scantrigger.Target, error) {
	return (fakeResolver{}).ResolveMissingSubtree(ctx, path, trigger)
}

func (r *failOnceResolver) ResolveVanishedPath(ctx context.Context, path, trigger string) (*scantrigger.Target, error) {
	return (fakeResolver{}).ResolveVanishedPath(ctx, path, trigger)
}

func (r *failOnceResolver) ResolveVanishedFile(ctx context.Context, path, trigger string) (*scantrigger.Target, error) {
	return r.ResolveVanishedPath(ctx, path, trigger)
}

func TestRetryPendingWebhookDeliveriesRecoversTransientFailure(t *testing.T) {
	store := webhookTestStore()
	queue := &recordingQueuer{}
	resolver := &failOnceResolver{}
	svc := NewService(store, &fakeProvider{}, passthroughConnRes{}, resolver, queue, allowSuppressor{}, nil)

	result, err := svc.IngestChanges(context.Background(), ChangeIngest{
		SourceID:          "s1",
		ProviderEventType: "Download",
		Changes:           []Change{{SourcePath: "/mnt/media/Movie/movie.mkv", Scope: ChangeScopeFile}},
	})
	if err != nil || !result.Pending {
		t.Fatalf("initial result = %+v, err = %v, want pending", result, err)
	}

	processed, err := svc.RetryPendingWebhookDeliveries(context.Background(), 10)
	if err != nil {
		t.Fatalf("RetryPendingWebhookDeliveries: %v", err)
	}
	if processed != 1 || len(queue.enqueued) != 1 {
		t.Fatalf("processed=%d enqueued=%d, want 1", processed, len(queue.enqueued))
	}
	if len(store.completed) != 1 {
		t.Fatalf("completed = %v, want retried delivery", store.completed)
	}
	if store.webhookErrors["s1"] != "" {
		t.Fatalf("webhook error not cleared: %q", store.webhookErrors["s1"])
	}
}

func TestIngestChangesRejectsPollSource(t *testing.T) {
	store := webhookTestStore()
	store.sources[0].DeliveryMode = DeliveryModePoll
	svc := newService(store, &fakeProvider{}, &recordingQueuer{}, allowSuppressor{})

	if _, err := svc.ingestChangesNow(context.Background(), ChangeIngest{SourceID: "s1"}); err == nil {
		t.Fatal("webhook consume must reject non-webhook sources")
	}
}

func TestIngestChangesUnresolvedPathsAreBenign(t *testing.T) {
	store := webhookTestStore()
	svc := NewService(store, &fakeProvider{}, passthroughConnRes{}, unresolvableResolver{}, &recordingQueuer{}, allowSuppressor{}, nil)

	result, err := svc.IngestChanges(context.Background(), ChangeIngest{
		SourceID:          "s1",
		ProviderEventType: "Download",
		Changes:           []Change{{SourcePath: "/outside/library/file.mkv", Scope: ChangeScopeFile}},
	})
	if err != nil {
		t.Fatalf("unresolved paths must not be an error: %v", err)
	}
	if !result.Unresolved || result.Enqueued != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(store.events) != 1 || store.events[0].Status != EventStatusUnresolved {
		t.Fatalf("finished events = %+v, want one unresolved", store.events)
	}
}

func TestIngestChangesTransientResolveFailureQueuesRetry(t *testing.T) {
	store := webhookTestStore()
	svc := NewService(store, &fakeProvider{}, passthroughConnRes{}, transientFailureResolver{}, &recordingQueuer{}, allowSuppressor{}, nil)

	result, err := svc.IngestChanges(context.Background(), ChangeIngest{
		SourceID:          "s1",
		ProviderEventType: "Download",
		Changes:           []Change{{SourcePath: "/mnt/media/Movie/movie.mkv", Scope: ChangeScopeFile}},
	})
	if err != nil {
		t.Fatalf("durably accepted delivery must not depend on sender retry: %v", err)
	}
	if !result.Pending {
		t.Fatalf("result = %+v, want pending retry", result)
	}
	if len(store.events) != 1 || store.events[0].Status != EventStatusError {
		t.Fatalf("finished events = %+v, want one error", store.events)
	}
	if store.recorded["s1"] == "" {
		t.Fatal("transient failure must be recorded on the source")
	}
	if len(store.retried) != 1 || store.retried[0].SourceID != "s1" {
		t.Fatalf("durable retries = %+v, want source s1", store.retried)
	}
	if store.webhookErrors["s1"] == "" {
		t.Fatal("transient failure must be visible on the webhook endpoint")
	}
}

// rootResolver resolves subtree paths below root, a real directory, so the
// removal wait can be exercised against the filesystem.
type rootResolver struct {
	fakeResolver
	root string
}

func (r rootResolver) ResolveMissingSubtree(_ context.Context, path, trigger string) (*scantrigger.Target, error) {
	if !strings.HasPrefix(path, r.root+string(filepath.Separator)) {
		return nil, &scantrigger.RequestError{Status: 400, Code: "bad_request", Message: "outside media folders"}
	}
	return &scantrigger.Target{Folder: &models.MediaFolder{ID: 7}, Mode: scantrigger.ModeSubtree, Path: path, Trigger: trigger}, nil
}

// Radarr and Sonarr send a folder-delete webhook before they delete the
// folder. The delivery waits, without recording an error, until the folder is
// gone, and only then scans it.
func TestIngestChangesWaitsForRemovedFolder(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "Film (2024)")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	store := webhookTestStore()
	store.sources[0].PathRewrites = []PathRewrite{{From: "/movies", To: root}}
	queue := &recordingQueuer{}
	svc := NewService(store, &fakeProvider{}, passthroughConnRes{}, rootResolver{root: root}, queue, allowSuppressor{}, nil)
	ingest := ChangeIngest{
		SourceID:          "s1",
		ProviderEventType: "MovieDelete",
		Changes:           []Change{{SourcePath: "/movies/Film (2024)", Scope: ChangeScopeSubtree, Removed: true}},
		ReceivedAt:        time.Now(),
	}

	result, err := svc.IngestChanges(context.Background(), ingest)
	if err != nil || result.Pending || !result.AwaitingRemoval {
		t.Fatalf("folder still present: result = %+v, err = %v, want awaiting removal and not a failure", result, err)
	}
	if _, err := svc.RetryPendingWebhookDeliveries(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if len(queue.enqueued) != 0 || len(store.createdEvents) != 0 {
		t.Fatalf("scanned a folder still on disk: enqueued=%+v events=%+v", queue.enqueued, store.createdEvents)
	}
	if len(store.webhookErrors) != 0 || len(store.recorded) != 0 {
		t.Fatalf("waiting recorded an error: webhook=%v source=%v", store.webhookErrors, store.recorded)
	}
	// Waiting is not a failure: the recheck neither carries an error nor
	// backs off as attempts accumulate.
	if len(store.retryDelays) != 2 || store.retryDelays[1] != store.retryDelays[0] || store.retryMessages[0] != "" || store.retryMessages[1] != "" {
		t.Fatalf("rechecks = %v %q, want two equal delays and no error", store.retryDelays, store.retryMessages)
	}
	// A delivery still waiting after a few minutes is checked less often.
	store.retried[0].ReceivedAt = time.Now().Add(-10 * time.Minute)
	if _, err := svc.RetryPendingWebhookDeliveries(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if len(store.retryDelays) != 3 || store.retryDelays[2] <= store.retryDelays[1] || store.retryMessages[2] != "" {
		t.Fatalf("rechecks = %v %q, want a longer delay for an older delivery", store.retryDelays, store.retryMessages)
	}

	if err := os.Remove(folder); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RetryPendingWebhookDeliveries(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if len(queue.enqueued) != 1 || queue.enqueued[0].Mode != scantrigger.ModeSubtree || queue.enqueued[0].Path != folder {
		t.Fatalf("enqueued = %+v, want one subtree scan of %s", queue.enqueued, folder)
	}
	if len(store.completed) != 1 {
		t.Fatalf("completed = %v, want the delivery completed", store.completed)
	}
}

// A delivery stops waiting once the wait expires and never waits for a path
// outside every library. A folder whose parent is gone too (a lost mount
// below the library root) is not confirmed deleted, so it keeps waiting; a
// deleted folder arr names with a trailing separator is confirmed.
func TestIngestChangesRemovalWaitBounds(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "Film (2024)")
	outside := t.TempDir()
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name         string
		path         string
		receivedAt   time.Time
		wantWaiting  bool
		wantEnqueued int
	}{
		{"wait expired", folder, time.Now().Add(-removalWait - time.Minute), false, 1},
		{"outside every library", outside, time.Now(), false, 0},
		{"parent gone too", filepath.Join(root, "Show", "Season 01"), time.Now(), true, 0},
		{"gone, sent with a trailing separator", filepath.Join(root, "Deleted (2024)") + string(filepath.Separator), time.Now(), false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := webhookTestStore()
			queue := &recordingQueuer{}
			svc := NewService(store, &fakeProvider{}, passthroughConnRes{}, rootResolver{root: root}, queue, allowSuppressor{}, nil)
			result, err := svc.IngestChanges(context.Background(), ChangeIngest{
				SourceID:          "s1",
				ProviderEventType: "MovieDelete",
				Changes:           []Change{{SourcePath: tc.path, Scope: ChangeScopeSubtree, Removed: true}},
				ReceivedAt:        tc.receivedAt,
			})
			wantCompleted := 1
			if tc.wantWaiting {
				wantCompleted = 0
			}
			if err != nil || result.Pending || result.AwaitingRemoval != tc.wantWaiting || len(queue.enqueued) != tc.wantEnqueued || len(store.completed) != wantCompleted {
				t.Fatalf("result = %+v, err = %v, enqueued = %+v, completed = %v; want waiting=%v, %d enqueued, %d completed",
					result, err, queue.enqueued, store.completed, tc.wantWaiting, tc.wantEnqueued, wantCompleted)
			}
		})
	}
}
