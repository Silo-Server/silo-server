package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/autoscan"
	"github.com/danielgtaylor/huma/v2"
)

type AdminAutoscanEventsService interface {
	ReadAdminAutoscanEvents(context.Context, autoscan.EventListFilter) ([]autoscan.EventWithRuns, int, error)
}
type AdminAutoscanEventStatus string

func (AdminAutoscanEventStatus) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Enum: []any{"", autoscan.EventStatusRunning, autoscan.EventStatusSuccess, autoscan.EventStatusError, autoscan.EventStatusUnresolved}}
}

const autoscanEventIDDescending = "id_desc"

type AdminAutoscanEventsInput struct {
	SourceID string                   `query:"source_id" maxLength:"256"`
	Limit    int                      `query:"limit" default:"50" minimum:"1" maximum:"200"`
	Cursor   string                   `query:"cursor" maxLength:"8192"`
	Status   AdminAutoscanEventStatus `query:"status"`
	Search   string                   `query:"q" maxLength:"1024"`
}
type AdminAutoscanEventsOutput struct{ Body AdminAutoscanEventsPage }
type AdminAutoscanEventsPage struct {
	Items []AdminAutoscanEvent `json:"items"`
	Page  *PageInfo            `json:"page"`
	Total int                  `json:"total" minimum:"0" doc:"Separate live count; not a snapshot of the page."`
}
type AdminAutoscanEventRun struct {
	ID           string                   `json:"id"`
	LibraryID    string                   `json:"library_id"`
	Mode         string                   `json:"mode"`
	Path         string                   `json:"path,omitempty"`
	Trigger      string                   `json:"trigger"`
	Status       string                   `json:"status"`
	RequestedAt  *Instant                 `json:"requested_at,omitempty"`
	StartedAt    *Instant                 `json:"started_at,omitempty"`
	CompletedAt  *Instant                 `json:"completed_at,omitempty"`
	ErrorMessage string                   `json:"error_message,omitempty"`
	Result       *AdminAutoscanScanResult `json:"result,omitempty" doc:"Outcome counters of a completed run; absent until the run completes."`
}

// AdminAutoscanEventChange is one change an autoscan event received and what
// the host did with it.
type AdminAutoscanEventChange struct {
	SourcePath    string `json:"source_path" doc:"Path exactly as the source reported it."`
	RewrittenPath string `json:"rewritten_path" doc:"Path after the source's path rewrites; equal to source_path when no rewrite matched."`
	Scope         string `json:"scope,omitempty" doc:"How the source scoped the change: auto, file, or subtree."`
	Outcome       string `json:"outcome" enum:"queued,joined,suppressed,unresolved,ignored,error" doc:"queued: created a new scan run. joined: coalesced into a scan run for the same scope that was already queued or running. suppressed: the debounce window had already claimed this path. unresolved: the path did not map to a scannable library location (see reason). ignored: deliberately not scanned (see reason). error: resolving or enqueueing failed internally."`
	Reason        string `json:"reason,omitempty" doc:"Machine-readable cause for unresolved, ignored, and error outcomes, such as no_library_match, library_root_offline, unsupported_extension, resolves_to_library, resolve_failed, or enqueue_failed. A joined change carries follow_up_scan when its scope was already being scanned. Treat unknown values as opaque."`
	Detail        string `json:"detail,omitempty" doc:"Human-readable detail for the reason, when available."`
	LibraryID     string `json:"library_id,omitempty" doc:"Library the change resolved to, when it resolved."`
	TargetMode    string `json:"target_mode,omitempty" doc:"Scan mode of the resolved target: library, subtree, or file."`
	TargetPath    string `json:"target_path,omitempty" doc:"Scope path of the resolved scan target; empty for a whole-library scan."`
	ScanRunID     string `json:"scan_run_id,omitempty" doc:"Scan run that covers the change, for queued and joined outcomes. Absent for a joined change with reason follow_up_scan: the run for its scope was already running, and a follow-up scan of the same scope, queued when that run finishes, covers it."`
}

type AdminAutoscanEvent struct {
	ID               string                     `json:"id"`
	SourceID         *string                    `json:"source_id"`
	PluginID         string                     `json:"plugin_id"`
	CapabilityID     string                     `json:"capability_id"`
	StartedAt        Instant                    `json:"started_at"`
	CompletedAt      Instant                    `json:"completed_at"`
	DurationMS       int64                      `json:"duration_ms"`
	Status           string                     `json:"status"`
	DeliveryMode     string                     `json:"delivery_mode"`
	ProviderEvent    string                     `json:"provider_event_type,omitempty"`
	ChangesReturned  int                        `json:"changes_returned"`
	ChangesResolved  int                        `json:"changes_resolved"`
	TargetsClaimed   int                        `json:"targets_claimed"`
	ScansCreated     int                        `json:"scans_created"`
	ScansReused      int                        `json:"scans_reused"`
	ScansSuppressed  int                        `json:"scans_suppressed"`
	ErrorMessage     string                     `json:"error_message,omitempty"`
	ScanRuns         []AdminAutoscanEventRun    `json:"scan_runs"`
	Changes          []AdminAutoscanEventChange `json:"changes" maxItems:"50" doc:"Changes the event received, in the order reported, capped at 50 entries. Empty for events recorded before change logging existed."`
	ChangesTruncated bool                       `json:"changes_truncated" doc:"True when the event received more changes than changes lists; changes_returned holds the full count."`
}

func registerAdminAutoscanEvents(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := Operation{Operation: humaOp("GET", Prefix+"/admin/autoscan/events", "listAdminAutoscanEvents", "admin-autoscan", "Read bounded SQL offset pages with a separate live count. Completed timestamp descending/id descending order; associated runs fully enumerated; signed continuation is not a snapshot or keyset guarantee."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, in *AdminAutoscanEventsInput) (*AdminAutoscanEventsOutput, error) {
		if reg.deps.AdminAutoscanEvents == nil {
			return nil, unavailable("autoscan event history")
		}
		filter := autoscan.EventListFilter{Limit: in.Limit, Status: autoscan.EventStatus(in.Status), Search: strings.TrimSpace(in.Search), SourceID: strings.TrimSpace(in.SourceID)}
		encoded, _ := json.Marshal([]any{filter.SourceID, filter.Status, filter.Search, filter.Limit})
		scope := CursorScope{OperationID: "listAdminAutoscanEvents", Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx) + "/" + viewerScopeDigest(ctx), Filter: string(encoded), Sort: "completed_desc", Tiebreaker: autoscanEventIDDescending}
		var pos struct{ Offset int }
		if in.Cursor != "" {
			if problem := cursors.Decode(scope, in.Cursor, &pos); problem != nil {
				return nil, problem
			}
		}
		if pos.Offset < 0 || pos.Offset > 1000000000 {
			return nil, NewProblem(TypeInvalidCursor, "Invalid event history position.")
		}
		filter.Offset = pos.Offset
		rows, total, err := reg.deps.AdminAutoscanEvents.ReadAdminAutoscanEvents(ctx, filter)
		if errors.Is(err, handlers.ErrAdminAutoscanEventsUnavailable) {
			return nil, unavailable("autoscan event history")
		}
		if err != nil {
			return nil, serviceProblem(err)
		}
		if total < 0 || len(rows) > in.Limit {
			return nil, NewProblem(TypeInternalError, "Invalid event history page.")
		}
		items := make([]AdminAutoscanEvent, 0, len(rows))
		for _, r := range rows {
			e := r.Event
			if e.ID <= 0 {
				return nil, NewProblem(TypeInternalError, "Invalid event identity.")
			}
			row := AdminAutoscanEvent{ID: strconv.FormatInt(e.ID, 10), SourceID: e.SourceID, PluginID: e.PluginID, CapabilityID: e.CapabilityID, StartedAt: NewInstant(e.StartedAt), CompletedAt: NewInstant(e.CompletedAt), DurationMS: e.DurationMS, Status: string(e.Status), DeliveryMode: e.DeliveryMode, ProviderEvent: e.ProviderEventType, ChangesReturned: e.ChangesReturned, ChangesResolved: e.ChangesResolved, TargetsClaimed: e.TargetsClaimed, ScansCreated: e.ScansCreated, ScansReused: e.ScansReused, ScansSuppressed: e.ScansSuppressed, ErrorMessage: e.ErrorMessage, ScanRuns: make([]AdminAutoscanEventRun, 0, len(r.Runs)), Changes: adminAutoscanEventChanges(e.Changes), ChangesTruncated: e.ChangesTruncated}
			for _, run := range r.Runs {
				if run.ID == "" || run.MediaFolderID <= 0 {
					return nil, NewProblem(TypeInternalError, "Invalid event scan identity.")
				}
				row.ScanRuns = append(row.ScanRuns, AdminAutoscanEventRun{ID: run.ID, LibraryID: strconv.Itoa(run.MediaFolderID), Mode: run.Mode, Path: run.Path, Trigger: run.Trigger, Status: run.Status, RequestedAt: instantPtr(run.RequestedAt), StartedAt: instantPtr(run.StartedAt), CompletedAt: instantPtr(run.CompletedAt), ErrorMessage: run.ErrorMessage, Result: adminAutoscanScanResult(run.Result)})
			}
			items = append(items, row)
		}
		next := ""
		if len(rows) == in.Limit {
			pos.Offset += len(rows)
			next, err = cursors.Encode(scope, pos)
			if err != nil {
				return nil, serviceProblem(err)
			}
		}
		return &AdminAutoscanEventsOutput{Body: AdminAutoscanEventsPage{Items: items, Page: Paginated(items, next).Page, Total: total}}, nil
	})
}

func adminAutoscanEventChanges(records []autoscan.ChangeRecord) []AdminAutoscanEventChange {
	out := make([]AdminAutoscanEventChange, 0, len(records))
	for _, rec := range records {
		change := AdminAutoscanEventChange{SourcePath: rec.SourcePath, RewrittenPath: rec.RewrittenPath, Scope: string(rec.Scope), Outcome: string(rec.Outcome), Reason: rec.Reason, Detail: rec.Detail, TargetMode: rec.TargetMode, TargetPath: rec.TargetPath, ScanRunID: rec.ScanRunID}
		if rec.LibraryID > 0 {
			change.LibraryID = strconv.Itoa(rec.LibraryID)
		}
		out = append(out, change)
	}
	return out
}
