package autoscan

import (
	"context"
	"errors"
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

func TestIngestChangesSuppressedDuplicates(t *testing.T) {
	store := webhookTestStore()
	q := &recordingQueuer{}
	svc := NewService(store, &fakeProvider{}, passthroughConnRes{}, fakeResolver{}, q, denySuppressor{}, nil)

	result, err := svc.IngestChanges(context.Background(), ChangeIngest{
		SourceID:          "s1",
		ProviderEventType: "Download",
		Changes:           []Change{{SourcePath: "/mnt/media/Movie/movie.mkv", Scope: ChangeScopeFile}},
	})
	if err != nil {
		t.Fatalf("IngestChanges: %v", err)
	}
	if result.Suppressed != 1 || result.Enqueued != 0 || result.Unresolved {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(q.enqueued) != 0 {
		t.Fatalf("suppressed delivery must not enqueue, got %+v", q.enqueued)
	}
	if len(store.events) != 1 || store.events[0].Status != EventStatusSuccess {
		t.Fatalf("finished events = %+v, want one success", store.events)
	}
}

func TestPollOnceSkipsWebhookSources(t *testing.T) {
	store := webhookTestStore()
	prov := &fakeProvider{paths: map[string][]string{BuiltinArrWebhookCapabilityID: {"/mnt/media/Movie/movie.mkv"}}}
	q := &recordingQueuer{}
	svc := newService(store, prov, q, allowSuppressor{})

	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if prov.calls != 0 {
		t.Fatalf("provider calls = %d, want 0 for webhook sources", prov.calls)
	}
	if len(store.createdEvents) != 0 || len(q.enqueued) != 0 {
		t.Fatalf("webhook sources must be skipped by polling entirely (events=%d, enqueued=%d)",
			len(store.createdEvents), len(q.enqueued))
	}
}
