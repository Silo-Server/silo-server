package notifications

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestPublishCatalogItemChangedAlsoPublishesLegacyMetadataUpdated(t *testing.T) {
	hub := NewHub("test", &cache.NoopEventBus{})
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	err := hub.PublishCatalogItemChanged(context.Background(), MetadataUpdateEvent{
		LibraryID: 12,
		ContentID: "item-1",
		Change:    "metadata_updated",
	})
	if err != nil {
		t.Fatalf("PublishCatalogItemChanged() error = %v", err)
	}

	got := map[Type]Envelope{}
	for len(got) < 2 {
		select {
		case event := <-events:
			got[event.Type] = event
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for events, got %#v", got)
		}
	}

	for _, eventType := range []Type{TypeCatalogItemChanged, TypeMetadataUpdated} {
		event, ok := got[eventType]
		if !ok {
			t.Fatalf("missing event %q in %#v", eventType, got)
		}
		if event.LibraryID != 12 || event.ContentID != "item-1" {
			t.Fatalf("event %q payload = library %d content %q", eventType, event.LibraryID, event.ContentID)
		}
	}
}

func TestPublishStorageTransitionJobOmitsPrivateDetails(t *testing.T) {
	hub := NewHub("test", &cache.NoopEventBus{})
	events, unsubscribe := hub.EventsHub().Subscribe()
	defer unsubscribe()
	job := &models.AdminJob{
		ID: "transition", JobType: storageTransitionJobType, Status: "running", RequestedAt: time.Now().UTC(),
		RequestPayload: json.RawMessage(`{"secret":"private request"}`),
		ResultPayload:  json.RawMessage(`{"phase":"copying","verified_objects":7,"source_identity":"private bucket","skipped_keys":["private object"]}`),
		Message:        "copying private object", ErrorMessage: "private endpoint",
		ArtifactBucket: "private artifact bucket", ArtifactKey: "private artifact key", PublicURL: "private public URL",
	}
	if err := hub.PublishJob(t.Context(), TypeJobProgress, job); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		payload := string(event.Data)
		for _, private := range []string{"private request", "private bucket", "private object", "private endpoint", "private artifact", "private public URL"} {
			if strings.Contains(payload, private) {
				t.Fatalf("job event leaked %q: %s", private, payload)
			}
		}
		var projected models.AdminJob
		if err := json.Unmarshal(event.Data, &projected); err != nil {
			t.Fatal(err)
		}
		var receipt storageTransitionEventResult
		if err := json.Unmarshal(projected.ResultPayload, &receipt); err != nil {
			t.Fatal(err)
		}
		if !event.AdminOnly || projected.ID != job.ID || projected.JobType != job.JobType || projected.Status != job.Status || receipt.Phase != "copying" || receipt.VerifiedObjects != 7 {
			t.Fatalf("safe job event = %+v result=%+v", projected, receipt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for storage job event")
	}
	if job.Message != "copying private object" {
		t.Fatal("publishing mutated the diagnostic job row")
	}
}

func TestSafeStorageTransitionJobWaitsForCurrentClaim(t *testing.T) {
	job := &models.AdminJob{
		ID: "requeued", JobType: storageTransitionJobType, Status: "queued", ClaimGeneration: 1,
		ResultPayload: json.RawMessage(`{"phase":"copying","verified_objects":7,"claim_generation":1}`),
	}
	assertResult := func(wantPhase string, wantVerified int, wantManual bool) {
		t.Helper()
		safe := SafeStorageTransitionJob(job)
		var result storageTransitionEventResult
		if err := json.Unmarshal(safe.ResultPayload, &result); err != nil {
			t.Fatal(err)
		}
		if result.Phase != wantPhase || result.VerifiedObjects != wantVerified || result.ManualRestartRequired != wantManual || safe.ProgressCurrent != wantVerified {
			t.Fatalf("safe result for status %q and claim %d = %+v, progress %d", job.Status, job.ClaimGeneration, result, safe.ProgressCurrent)
		}
	}
	assertResult("queued", 0, false)
	job.Status = "running"
	job.ClaimGeneration = 2
	assertResult("checking_target", 0, false)
	job.ResultPayload = json.RawMessage(`{"phase":"copying","verified_objects":2,"claim_generation":2}`)
	assertResult("copying", 2, false)
	job.ClaimGeneration = 1
	job.ResultPayload = json.RawMessage(`{"phase":"copying","verified_objects":7}`)
	assertResult("copying", 7, false)
	job.ClaimGeneration = 2
	assertResult("checking_target", 0, false)
	job.Status = "queued"
	job.ResultPayload = json.RawMessage(`{"phase":"restart_pending","verified_objects":7,"manual_restart_required":true,"commit_outcome_unknown":true}`)
	assertResult("queued", 0, false)
	job.Status = "completed"
	assertResult("restart_pending", 7, true)
	job.Status = "running"
	job.ClaimGeneration = 3
	job.ResultPayload = json.RawMessage(`{"phase":"restart_pending","verified_objects":7,"claim_generation":3,"restart_required":true,"manual_restart_required":true}`)
	assertResult("restart_pending", 7, true)
}
