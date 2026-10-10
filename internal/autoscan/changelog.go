package autoscan

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

// MaxEventChangeRecords bounds how many per-change records one autoscan event
// keeps. A marker window or webhook can carry thousands of paths; the event's
// counters keep the full totals, and ChangesTruncated marks events whose log
// stops short of ChangesReturned.
const MaxEventChangeRecords = 50

const (
	maxChangeRecordPathLen   = 1024
	maxChangeRecordDetailLen = 300
)

// ChangeOutcome is what the host did with one change a source reported.
type ChangeOutcome string

const (
	// ChangeOutcomeQueued: the change created a new scan run.
	ChangeOutcomeQueued ChangeOutcome = "queued"
	// ChangeOutcomeJoined: the change was coalesced into a scan run for the
	// same scope that was already queued or running.
	ChangeOutcomeJoined ChangeOutcome = "joined"
	// ChangeOutcomeSuppressed: the debounce window had already claimed the
	// same path, so no scan was requested.
	ChangeOutcomeSuppressed ChangeOutcome = "suppressed"
	// ChangeOutcomeUnresolved: the path did not map to a scannable Silo
	// library location; Reason says why.
	ChangeOutcomeUnresolved ChangeOutcome = "unresolved"
	// ChangeOutcomeIgnored: the change was deliberately not scanned (an empty
	// path, or a file change that resolved to a whole library).
	ChangeOutcomeIgnored ChangeOutcome = "ignored"
	// ChangeOutcomeError: resolving or enqueueing failed internally.
	ChangeOutcomeError ChangeOutcome = "error"
)

// Change reason codes the host adds on top of scantrigger.Reason values, which
// unresolved changes carry verbatim.
const (
	ChangeReasonUnresolvable      = "unresolvable"
	ChangeReasonResolvesToLibrary = "resolves_to_library"
	ChangeReasonResolveFailed     = "resolve_failed"
	ChangeReasonEnqueueFailed     = "enqueue_failed"
	// ChangeReasonFollowUpScan marks a joined change whose scope was already
	// being scanned: a follow-up scan of the scope, queued when that scan
	// finishes, covers it, so the record names no run.
	ChangeReasonFollowUpScan = "follow_up_scan"
)

// ChangeRecord is one entry of an event's change log.
type ChangeRecord struct {
	SourcePath    string        `json:"source_path"`
	RewrittenPath string        `json:"rewritten_path"`
	Scope         ChangeScope   `json:"scope,omitempty"`
	Outcome       ChangeOutcome `json:"outcome"`
	Reason        string        `json:"reason,omitempty"`
	Detail        string        `json:"detail,omitempty"`
	LibraryID     int           `json:"library_id,omitempty"`
	TargetMode    string        `json:"target_mode,omitempty"`
	TargetPath    string        `json:"target_path,omitempty"`
	ScanRunID     string        `json:"scan_run_id,omitempty"`

	// pendingTarget is the scan-target key of a claimed change awaiting its
	// enqueue outcome; empty once the outcome is known.
	pendingTarget string
}

func newChangeRecords(raw, rewritten []Change) []ChangeRecord {
	records := make([]ChangeRecord, len(raw))
	for i := range raw {
		records[i] = ChangeRecord{
			SourcePath:    raw[i].SourcePath,
			RewrittenPath: rewritten[i].SourcePath,
			Scope:         raw[i].Scope,
		}
	}
	return records
}

func (r *ChangeRecord) setTarget(target scantrigger.Target) {
	if target.Folder != nil {
		r.LibraryID = target.Folder.ID
	}
	r.TargetMode = target.Mode
	r.TargetPath = target.Path
}

func (r *ChangeRecord) setOutcome(outcome ChangeOutcome, reason, detail string) {
	r.Outcome = outcome
	r.Reason = reason
	r.Detail = detail
	r.pendingTarget = ""
}

// setUnresolved records a resolver rejection. primary is the first resolve
// error; fallback, when non-nil, is the vanished-path retry's error. The retry
// rejecting a path because it "still exists" says nothing about why the first
// attempt failed, so the primary error explains the change in that case.
func (r *ChangeRecord) setUnresolved(primary, fallback error) {
	err := primary
	if fallback != nil {
		var fbErr *scantrigger.RequestError
		if !errors.As(fallback, &fbErr) || fbErr.Reason != scantrigger.ReasonPathStillExists || primary == nil {
			err = fallback
		}
	}
	reason, detail := ChangeReasonUnresolvable, ""
	var reqErr *scantrigger.RequestError
	if errors.As(err, &reqErr) {
		if reqErr.Reason != "" {
			reason = string(reqErr.Reason)
		}
		detail = reqErr.Message
	}
	r.setOutcome(ChangeOutcomeUnresolved, reason, detail)
}

func scanTargetKey(target scantrigger.Target) string {
	folderID := 0
	if target.Folder != nil {
		folderID = target.Folder.ID
	}
	return fmt.Sprintf("%d|%s|%s", folderID, target.Mode, target.Path)
}

// applyEnqueueOutcomes resolves every pending record against the outcome of
// the target it was claimed for. outcomes is aligned with targets; when the
// queue reported no per-target outcome (an event-less enqueue) the change is
// recorded as queued without a run id.
func applyEnqueueOutcomes(records []ChangeRecord, targets []scantrigger.Target, outcomes []scantrigger.EnqueueOutcome) {
	byKey := make(map[string]scantrigger.EnqueueOutcome, len(outcomes))
	for i, target := range targets {
		if i < len(outcomes) {
			byKey[scanTargetKey(target)] = outcomes[i]
		}
	}
	for i := range records {
		rec := &records[i]
		if rec.pendingTarget == "" {
			continue
		}
		outcome, ok := byKey[rec.pendingTarget]
		if !ok {
			rec.setOutcome(ChangeOutcomeQueued, "", "")
			continue
		}
		switch {
		case outcome.Created:
			rec.setOutcome(ChangeOutcomeQueued, "", "")
		case outcome.FollowUp:
			// The running run may already have passed this path; naming it
			// would point at a result that leaves the change out.
			rec.setOutcome(ChangeOutcomeJoined, ChangeReasonFollowUpScan, "")
			continue
		default:
			rec.setOutcome(ChangeOutcomeJoined, "", "")
		}
		rec.ScanRunID = outcome.RunID
	}
}

// collapsePendingToLibraries rewrites pending records to the library-scan
// targets collapseTargetsToLibraryScans produced for an oversized window.
func collapsePendingToLibraries(records []ChangeRecord) {
	for i := range records {
		rec := &records[i]
		if rec.pendingTarget == "" {
			continue
		}
		rec.TargetMode = scantrigger.ModeLibrary
		rec.TargetPath = ""
		rec.pendingTarget = fmt.Sprintf("%d|%s|", rec.LibraryID, scantrigger.ModeLibrary)
	}
}

// failPending marks records still awaiting an enqueue outcome as failed.
func failPending(records []ChangeRecord) {
	for i := range records {
		if records[i].pendingTarget != "" {
			records[i].setOutcome(ChangeOutcomeError, ChangeReasonEnqueueFailed, "")
		}
	}
}

// sanitizeChangeRecordString truncates s to limit bytes and replaces NUL, which
// Postgres jsonb rejects; one NUL in a reported path would otherwise fail the
// whole event update and leave the event running.
func sanitizeChangeRecordString(s string, limit int) string {
	return truncateUTF8(strings.ReplaceAll(s, "\x00", "\uFFFD"), limit)
}

// boundChangeRecords caps the log at MaxEventChangeRecords entries and long
// strings at a fixed size so one event row stays small.
func boundChangeRecords(records []ChangeRecord) ([]ChangeRecord, bool) {
	truncated := len(records) > MaxEventChangeRecords
	if truncated {
		records = records[:MaxEventChangeRecords]
	}
	out := make([]ChangeRecord, len(records))
	for i, rec := range records {
		rec.SourcePath = sanitizeChangeRecordString(rec.SourcePath, maxChangeRecordPathLen)
		rec.RewrittenPath = sanitizeChangeRecordString(rec.RewrittenPath, maxChangeRecordPathLen)
		rec.TargetPath = sanitizeChangeRecordString(rec.TargetPath, maxChangeRecordPathLen)
		rec.Detail = sanitizeChangeRecordString(rec.Detail, maxChangeRecordDetailLen)
		rec.pendingTarget = ""
		out[i] = rec
	}
	return out, truncated
}
