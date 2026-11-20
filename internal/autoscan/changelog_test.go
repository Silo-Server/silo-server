package autoscan

import (
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

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
