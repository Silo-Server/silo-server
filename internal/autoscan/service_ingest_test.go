package autoscan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

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

type recordingRemovals struct {
	paths   []string
	folders []string
	err     error
}

func (r *recordingRemovals) ConfirmFilesRemoved(_ context.Context, paths []string) (int, error) {
	r.paths = append(r.paths, paths...)
	return len(paths), r.err
}

func (r *recordingRemovals) ConfirmFilesRemovedUnder(_ context.Context, roots []string) (int, error) {
	r.folders = append(r.folders, roots...)
	return len(roots), r.err
}

// Only files arr reported deleting that are gone from a directory that still
// exists are confirmed. A file or symlink still at the path, or a path whose
// directory is gone too (possibly a lost mount), keeps the removal grace.
func TestIngestChangesConfirmsArrRemovedFiles(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present.mkv")
	if err := os.WriteFile(present, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(dir, "gone.mkv")
	goneDir := filepath.Join(dir, "missing-season", "gone.mkv")
	// A dangling symlink is still something at the path.
	dangling := filepath.Join(dir, "dangling.mkv")
	if err := os.Symlink(filepath.Join(dir, "target-removed.mkv"), dangling); err != nil {
		t.Fatal(err)
	}

	removals := &recordingRemovals{}
	svc := newService(webhookTestStore(), &fakeProvider{}, &recordingQueuer{}, allowSuppressor{})
	svc.SetRemovalConfirmer(removals)

	if _, err := svc.IngestChanges(context.Background(), ChangeIngest{
		SourceID:          "s1",
		ProviderEventType: "Download",
		Changes: []Change{
			{SourcePath: filepath.Join(dir, "new.mkv"), Scope: ChangeScopeFile},
			{SourcePath: gone, Scope: ChangeScopeFile, Removed: true},
			{SourcePath: present, Scope: ChangeScopeFile, Removed: true},
			{SourcePath: goneDir, Scope: ChangeScopeFile, Removed: true},
			{SourcePath: dangling, Scope: ChangeScopeFile, Removed: true},
			{SourcePath: filepath.Join(dir, "unreported.mkv"), Scope: ChangeScopeFile},
		},
	}); err != nil {
		t.Fatalf("IngestChanges: %v", err)
	}
	if len(removals.paths) != 1 || removals.paths[0] != gone {
		t.Fatalf("confirmed = %v, want only %s", removals.paths, gone)
	}
}

// A movie or series folder arr deleted is confirmed as a folder once its
// parent directory exists, whether the folder is already gone or arr is still
// removing it. A folder whose parent is gone too (possibly a lost mount), or a
// path that is a file rather than a folder, keeps the removal grace.
func TestIngestChangesConfirmsArrRemovedFolders(t *testing.T) {
	root := t.TempDir()
	gone := filepath.Join(root, "Gone Movie (2020)")
	deleting := filepath.Join(root, "Deleting Movie (2021)")
	if err := os.Mkdir(deleting, 0o700); err != nil {
		t.Fatal(err)
	}
	notFolder := filepath.Join(root, "Not A Folder (2023)")
	if err := os.WriteFile(notFolder, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	orphaned := filepath.Join(root, "unmounted", "Orphan Movie (2022)")
	linkedRoot := filepath.Join(root, "linked")
	if err := os.Symlink(t.TempDir(), linkedRoot); err != nil {
		t.Fatal(err)
	}
	goneLinked := filepath.Join(linkedRoot, "Linked Movie (2024)")

	removals := &recordingRemovals{}
	svc := newService(webhookTestStore(), &fakeProvider{}, &recordingQueuer{}, allowSuppressor{})
	svc.SetRemovalConfirmer(removals)

	if _, err := svc.IngestChanges(context.Background(), ChangeIngest{
		SourceID:          "s1",
		ProviderEventType: "MovieDelete",
		Changes: []Change{
			{SourcePath: gone, Scope: ChangeScopeSubtree, Removed: true},
			{SourcePath: deleting, Scope: ChangeScopeSubtree, Removed: true},
			{SourcePath: notFolder, Scope: ChangeScopeSubtree, Removed: true},
			{SourcePath: orphaned, Scope: ChangeScopeSubtree, Removed: true},
			{SourcePath: goneLinked, Scope: ChangeScopeSubtree, Removed: true},
		},
	}); err != nil {
		t.Fatalf("IngestChanges: %v", err)
	}
	want := []string{gone, deleting, goneLinked}
	if !slices.Equal(removals.folders, want) || len(removals.paths) != 0 {
		t.Fatalf("confirmed folders = %v, files = %v, want folders %v", removals.folders, removals.paths, want)
	}
}

// Confirming is a shortcut: when it fails, the delivery still completes, and
// the files wait out the grace as before.
func TestIngestChangesSurvivesConfirmFailure(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone.mkv")
	store := webhookTestStore()
	removals := &recordingRemovals{err: errors.New("db down")}
	svc := newService(store, &fakeProvider{}, &recordingQueuer{}, allowSuppressor{})
	svc.SetRemovalConfirmer(removals)

	result, err := svc.IngestChanges(context.Background(), ChangeIngest{
		SourceID:          "s1",
		ProviderEventType: "EpisodeFileDelete",
		Changes:           []Change{{SourcePath: gone, Scope: ChangeScopeFile, Removed: true}},
	})
	if err != nil || result.Pending {
		t.Fatalf("result = %+v, err = %v, want a completed delivery", result, err)
	}
	if len(removals.paths) != 1 || len(store.completed) != 1 {
		t.Fatalf("confirm attempts=%v completed=%v, want one attempt and the delivery completed", removals.paths, store.completed)
	}
}
