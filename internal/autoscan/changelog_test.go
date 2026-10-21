package autoscan

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

func pollOneSource(t *testing.T, src Source, changes []Change, q Queuer, suppress Suppressor, resolver Resolver) EventFinish {
	t.Helper()
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources:  []Source{src},
	}
	prov := &fakeProvider{changes: map[string][]Change{src.CapabilityID: changes}, nextMarker: "m1"}
	svc := NewService(store, prov, passthroughConnRes{}, resolver, q, suppress, nil)
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(store.events) != 1 {
		t.Fatalf("finished events = %d, want 1", len(store.events))
	}
	return store.events[0]
}

func arrSource(rewrites ...PathRewrite) Source {
	return Source{ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", Enabled: true, PathRewrites: rewrites}
}

type reasonResolver struct{ fakeResolver }

func (reasonResolver) Resolve(_ context.Context, req scantrigger.Request) (*scantrigger.Target, error) {
	if strings.HasPrefix(req.Path, "/mnt/media/") {
		return fakeResolver{}.Resolve(context.Background(), req)
	}
	return nil, &scantrigger.RequestError{Message: "No library matches the given path", Reason: scantrigger.ReasonNoLibraryMatch}
}

func (reasonResolver) ResolveVanishedPath(context.Context, string, string) (*scantrigger.Target, error) {
	return nil, &scantrigger.RequestError{Message: "Path still exists", Reason: scantrigger.ReasonPathStillExists}
}

func TestChangeLogPrefersPrimaryReasonOverStillExistsFallback(t *testing.T) {
	event := pollOneSource(t, arrSource(),
		[]Change{{SourcePath: "/movies/Film/Film.mkv", Scope: ChangeScopeFile}},
		&recordingQueuer{}, allowSuppressor{}, reasonResolver{})
	got := event.Changes[0]
	if got.Outcome != ChangeOutcomeUnresolved || got.Reason != string(scantrigger.ReasonNoLibraryMatch) || got.Detail != "No library matches the given path" {
		t.Fatalf("change = %+v", got)
	}
}

func TestChangeLogRecordsRewriteTargetAndRunIDs(t *testing.T) {
	created := 1
	q := &recordingQueuer{createdCount: &created}
	event := pollOneSource(t, arrSource(PathRewrite{From: "/data/tv", To: "/mnt/media/tv"}),
		[]Change{
			{SourcePath: "/data/tv/ShowA/S01/E01.mkv", Scope: ChangeScopeFile},
			{SourcePath: "/data/tv/ShowB/S01/E01.mkv", Scope: ChangeScopeFile},
		},
		q, allowSuppressor{}, fakeResolver{})

	if event.Status != EventStatusSuccess || event.ScansCreated != 1 || event.ScansReused != 1 {
		t.Fatalf("event = %+v", event)
	}
	first, second := event.Changes[0], event.Changes[1]
	if first.SourcePath != "/data/tv/ShowA/S01/E01.mkv" || first.RewrittenPath != "/mnt/media/tv/ShowA/S01/E01.mkv" {
		t.Fatalf("first paths = %+v", first)
	}
	if first.Outcome != ChangeOutcomeQueued || first.ScanRunID != "run-0" || first.LibraryID != 7 ||
		first.TargetMode != scantrigger.ModeFile || first.TargetPath != "/mnt/media/tv/ShowA/S01/E01.mkv" {
		t.Fatalf("first outcome = %+v", first)
	}
	if second.Outcome != ChangeOutcomeJoined || second.ScanRunID != "run-1" {
		t.Fatalf("second outcome = %+v", second)
	}
}

func TestChangeLogMarksEnqueueFailure(t *testing.T) {
	event := pollOneSource(t, arrSource(),
		[]Change{{SourcePath: "/mnt/media/Show/S01/E01.mkv", Scope: ChangeScopeFile}},
		failingQueuer{}, allowSuppressor{}, fakeResolver{})
	if event.Status != EventStatusError {
		t.Fatalf("status = %q", event.Status)
	}
	got := event.Changes[0]
	if got.Outcome != ChangeOutcomeError || got.Reason != ChangeReasonEnqueueFailed {
		t.Fatalf("change = %+v", got)
	}
}

func TestChangeLogMarksTransientResolveFailure(t *testing.T) {
	event := pollOneSource(t, arrSource(),
		[]Change{
			{SourcePath: "/mnt/media/Show/S01/E01.mkv", Scope: ChangeScopeFile},
			{SourcePath: "/other/Show/S01/E01.mkv", Scope: ChangeScopeFile},
		},
		&recordingQueuer{}, allowSuppressor{}, mixedTransientResolver{})
	if event.Changes[0].Outcome != ChangeOutcomeQueued {
		t.Fatalf("resolved change = %+v", event.Changes[0])
	}
	got := event.Changes[1]
	if got.Outcome != ChangeOutcomeError || got.Reason != ChangeReasonResolveFailed || got.Detail == "" {
		t.Fatalf("failed change = %+v", got)
	}
}

func TestChangeLogIsBounded(t *testing.T) {
	changes := make([]Change, MaxEventChangeRecords+10)
	for i := range changes {
		changes[i] = Change{SourcePath: "/mnt/media/Show/E" + strconv.Itoa(i) + ".mkv", Scope: ChangeScopeFile}
	}
	changes[0].SourcePath = "/mnt/media/" + strings.Repeat("a", 2*maxChangeRecordPathLen) + ".mkv"
	event := pollOneSource(t, arrSource(), changes, &recordingQueuer{}, allowSuppressor{}, fakeResolver{})
	if len(event.Changes) != MaxEventChangeRecords || !event.ChangesTruncated {
		t.Fatalf("changes = %d truncated=%v", len(event.Changes), event.ChangesTruncated)
	}
	if event.ChangesReturned != len(changes) {
		t.Fatalf("changes_returned = %d, want %d", event.ChangesReturned, len(changes))
	}
	if n := len(event.Changes[0].SourcePath); n > maxChangeRecordPathLen {
		t.Fatalf("source path length = %d, want <= %d", n, maxChangeRecordPathLen)
	}
}

func TestChangeLogFollowsCollapsedLibraryScans(t *testing.T) {
	records := []ChangeRecord{{LibraryID: 3, TargetMode: scantrigger.ModeFile, TargetPath: "/x/a.mkv", pendingTarget: "3|file|/x/a.mkv"}}
	collapsePendingToLibraries(records)
	targets := collapseTargetsToLibraryScans([]scantrigger.Target{{Folder: &models.MediaFolder{ID: 3}, Mode: scantrigger.ModeFile, Path: "/x/a.mkv"}})
	applyEnqueueOutcomes(records, targets, []scantrigger.EnqueueOutcome{{RunID: "lib-run", Created: true}})
	if records[0].Outcome != ChangeOutcomeQueued || records[0].ScanRunID != "lib-run" || records[0].TargetMode != scantrigger.ModeLibrary || records[0].TargetPath != "" {
		t.Fatalf("record = %+v", records[0])
	}
}

// A change coalesced into a run that is already running is covered by the
// follow-up scan that run owes, not by the run itself, which may already have
// passed the path. The record must not name the running run.
func TestChangeLogJoinedRunningScanWaitsForFollowUp(t *testing.T) {
	target := scantrigger.Target{Folder: &models.MediaFolder{ID: 3}, Mode: scantrigger.ModeSubtree, Path: "/tv/Show"}
	key := scanTargetKey(target)
	records := []ChangeRecord{{pendingTarget: key}, {pendingTarget: key}}

	applyEnqueueOutcomes(records[:1], []scantrigger.Target{target}, []scantrigger.EnqueueOutcome{{RunID: "queued-run"}})
	if got := records[0]; got.Outcome != ChangeOutcomeJoined || got.Reason != "" || got.ScanRunID != "queued-run" {
		t.Fatalf("joined queued run: record = %+v", got)
	}

	applyEnqueueOutcomes(records[1:], []scantrigger.Target{target}, []scantrigger.EnqueueOutcome{{RunID: "running-run", FollowUp: true}})
	if got := records[1]; got.Outcome != ChangeOutcomeJoined || got.Reason != ChangeReasonFollowUpScan || got.ScanRunID != "" {
		t.Fatalf("joined running run: record = %+v", got)
	}
}

// Postgres jsonb rejects NUL, so one NUL in a reported path would fail the
// event's final update and leave it running.
func TestBoundChangeRecordsReplacesNUL(t *testing.T) {
	records, _ := boundChangeRecords([]ChangeRecord{{
		SourcePath:    "/tv/a\x00b.mkv",
		RewrittenPath: "/mnt/a\x00b.mkv",
		TargetPath:    "/mnt/a\x00b.mkv",
		Detail:        "bad\x00detail",
	}})
	got := records[0]
	for name, value := range map[string]string{"source": got.SourcePath, "rewritten": got.RewrittenPath, "target": got.TargetPath, "detail": got.Detail} {
		if strings.ContainsRune(value, 0) {
			t.Fatalf("%s path still contains NUL: %q", name, value)
		}
	}
	if got.SourcePath != "/tv/a\uFFFDb.mkv" {
		t.Fatalf("source path = %q", got.SourcePath)
	}
}
