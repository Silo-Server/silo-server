package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/autoscan"
	"github.com/Silo-Server/silo-server/internal/scanqueue"
	"github.com/danielgtaylor/huma/v2"
)

type AdminAutoscanScansService interface {
	ReadAdminAutoscanScans(context.Context, autoscan.ScanListFilter) ([]autoscan.ScanWithEvent, int, error)
}
type AdminAutoscanScanStatus string

func (AdminAutoscanScanStatus) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Enum: []any{"", scanqueue.StatusAccepted, scanqueue.StatusRunning, scanqueue.StatusCompleted, scanqueue.StatusFailed, scanqueue.StatusCancelled}}
}

const autoscanScanIDDescending = "id_desc"

type AdminAutoscanScansInput struct {
	Limit  int                     `query:"limit" default:"50" minimum:"1" maximum:"200"`
	Cursor string                  `query:"cursor" maxLength:"8192"`
	Status AdminAutoscanScanStatus `query:"status"`
	Search string                  `query:"q" maxLength:"1024"`
}
type AdminAutoscanScansOutput struct{ Body AdminAutoscanScansPage }
type AdminAutoscanScansPage struct {
	Items []AdminAutoscanScan `json:"items"`
	Page  *PageInfo           `json:"page"`
	Total int                 `json:"total" minimum:"0" doc:"Separate live count; not a snapshot of the page."`
}
type AdminAutoscanScan struct {
	ID               string   `json:"id"`
	LibraryID        string   `json:"library_id"`
	Mode             string   `json:"mode"`
	Path             string   `json:"path,omitempty"`
	Trigger          string   `json:"trigger"`
	Status           string   `json:"status"`
	ErrorMessage     string   `json:"error_message,omitempty"`
	RequestedAt      *Instant `json:"requested_at,omitempty"`
	StartedAt        *Instant `json:"started_at,omitempty"`
	CompletedAt      *Instant `json:"completed_at,omitempty"`
	AutoscanEventID  *string  `json:"autoscan_event_id,omitempty"`
	SourceID         *string  `json:"source_id,omitempty"`
	PluginID         string   `json:"plugin_id,omitempty"`
	CapabilityID     string   `json:"capability_id,omitempty"`
	EventStatus      string   `json:"event_status,omitempty"`
	EventCompletedAt *Instant `json:"event_completed_at,omitempty"`
}

func registerAdminAutoscanScans(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := Operation{Operation: humaOp("GET", Prefix+"/admin/autoscan/scans", "listAdminAutoscanScans", "admin-autoscan", "Read bounded SQL offset pages with a separate live count. Mutable lifecycle timestamp descending/id descending order; signed continuation is not a snapshot or keyset guarantee."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, in *AdminAutoscanScansInput) (*AdminAutoscanScansOutput, error) {
		if reg.deps.AdminAutoscanScans == nil {
			return nil, unavailable("autoscan scan history")
		}
		filter := autoscan.ScanListFilter{Limit: in.Limit, Status: string(in.Status), Search: strings.TrimSpace(in.Search)}
		encoded, _ := json.Marshal([]any{filter.Status, filter.Search, filter.Limit})
		scope := CursorScope{OperationID: "listAdminAutoscanScans", Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx) + "/" + viewerScopeDigest(ctx), Filter: string(encoded), Sort: "lifecycle_desc", Tiebreaker: autoscanScanIDDescending}
		var pos struct{ Offset int }
		if in.Cursor != "" {
			if problem := cursors.Decode(scope, in.Cursor, &pos); problem != nil {
				return nil, problem
			}
		}
		if pos.Offset < 0 || pos.Offset > 1000000000 {
			return nil, NewProblem(TypeInvalidCursor, "Invalid scan history position.")
		}
		filter.Offset = pos.Offset
		rows, total, err := reg.deps.AdminAutoscanScans.ReadAdminAutoscanScans(ctx, filter)
		if errors.Is(err, handlers.ErrAdminAutoscanScansUnavailable) {
			return nil, unavailable("autoscan scan history")
		}
		if err != nil {
			return nil, serviceProblem(err)
		}
		if total < 0 || len(rows) > in.Limit {
			return nil, NewProblem(TypeInternalError, "Invalid scan history page.")
		}
		items := make([]AdminAutoscanScan, 0, len(rows))
		for _, r := range rows {
			if r.ID == "" || r.MediaFolderID <= 0 {
				return nil, NewProblem(TypeInternalError, "Invalid scan history identity.")
			}
			row := AdminAutoscanScan{ID: r.ID, LibraryID: strconv.Itoa(r.MediaFolderID), Mode: r.Mode, Path: r.Path, Trigger: r.Trigger, Status: r.Status, ErrorMessage: r.ErrorMessage, RequestedAt: instantPtr(r.RequestedAt), StartedAt: instantPtr(r.StartedAt), CompletedAt: instantPtr(r.CompletedAt), SourceID: r.SourceID, PluginID: r.PluginID, CapabilityID: r.CapabilityID, EventStatus: string(r.EventStatus), EventCompletedAt: instantPtr(r.EventCompletedAt)}
			if r.AutoscanEventID != nil {
				row.AutoscanEventID = new(strconv.FormatInt(*r.AutoscanEventID, 10))
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
		return &AdminAutoscanScansOutput{Body: AdminAutoscanScansPage{Items: items, Page: Paginated(items, next).Page, Total: total}}, nil
	})
}
