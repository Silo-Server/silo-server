package apiv2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/historyimport"
)

type AdminHistoryImportRun struct {
	HistoryImportRun
}
type AdminHistoryImportRunOutput struct {
	Status     int
	ETag       string `header:"ETag"`
	Location   string `header:"Location"`
	RetryAfter string `header:"Retry-After"`
	Body       AdminHistoryImportRun
}
type AdminHistoryImportRunGetInput struct {
	HistoryImportRunIDInput
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type AdminHistoryImportRunCollectionOutput struct {
	Body Collection[AdminHistoryImportRun]
}

const adminHistoryAccepted = "accepted"

type AdminHistoryImportBulkOutcome struct {
	MappingID ID                     `json:"mapping_id"`
	Status    string                 `json:"status" enum:"accepted,active,failed"`
	Run       *AdminHistoryImportRun `json:"run,omitempty"`
	Location  string                 `json:"location,omitempty"`
	Error     string                 `json:"error,omitempty"`
}
type AdminHistoryImportBulkOutput struct {
	Body struct {
		Outcomes []AdminHistoryImportBulkOutcome `json:"outcomes"`
		Accepted int                             `json:"accepted"`
		Active   int                             `json:"active"`
		Failed   int                             `json:"failed"`
	}
}
type AdminHistoryImportPlexLoginInput struct {
	Body struct {
		Username string `json:"username" minLength:"1"`
		Password string `json:"password" minLength:"1"`
	}
}
type AdminHistoryImportPlexLoginOutput struct {
	Body struct {
		Token string `json:"token"`
	}
}

func adminHistoryRunLocation(id string) string { return Prefix + "/admin/history-imports/runs/" + id }
func safeAdminHistoryRun(run *historyimport.Run) AdminHistoryImportRun {
	out := AdminHistoryImportRun{HistoryImportRun: historyImportRunOf(run)}
	out.Cancelable = !out.Terminal
	return out
}
func registerAdminHistoryImportRuns(reg *Registry, op func(string, string, string, bool) Operation) {
	cursors := NewCursors(reg.deps.CursorSecret)
	create := op(http.MethodPost, "/admin/history-imports/mappings/{id}/run", "createAdminHistoryImportRun", false)
	create.DefaultStatus = http.StatusAccepted
	Register(reg, create, func(ctx context.Context, in *AdminHistoryImportIDInput) (*AdminHistoryImportRunOutput, error) {
		s, p := reg.adminHistoryImports()
		if p != nil {
			return nil, p
		}
		id, p := adminHistoryID(in.ID)
		if p != nil {
			return nil, p
		}
		run, err := s.CreateAdminRun(ctx, id)
		if err != nil {
			return nil, adminHistoryProblem(err)
		}
		out := adminHistoryRunOutput(run)
		out.Status = http.StatusAccepted
		out.Location = adminHistoryRunLocation(run.ID)
		return out, nil
	})
	get := op(http.MethodGet, "/admin/history-imports/runs/{id}", "getAdminHistoryImportRun", false)
	get.Conditional = true
	Register(reg, get, func(ctx context.Context, in *AdminHistoryImportRunGetInput) (*AdminHistoryImportRunOutput, error) {
		s, p := reg.adminHistoryImports()
		if p != nil {
			return nil, p
		}
		run, err := s.GetAdminRun(ctx, in.ID)
		if err != nil {
			return nil, adminHistoryProblem(err)
		}
		out := adminHistoryRunOutput(run)
		tag, _ := ParseEntityTag(out.ETag)
		if matched, p := EvaluateReadPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
			return nil, p
		} else if matched {
			return NotModified(out, tag), nil
		}
		return out, nil
	})
	cancel := op(http.MethodPost, "/admin/history-imports/runs/{id}/cancel", "cancelAdminHistoryImportRun", false)
	cancel.DefaultStatus = http.StatusAccepted
	cancel.Responses = map[string]*huma.Response{"200": {Description: "The run is already canceled.", Content: map[string]*huma.MediaType{mediaTypeJSON: {Schema: &huma.Schema{Ref: "#/components/schemas/AdminHistoryImportRun"}}}}}
	Register(reg, cancel, func(ctx context.Context, in *HistoryImportRunIDInput) (*AdminHistoryImportRunOutput, error) {
		s, p := reg.adminHistoryImports()
		if p != nil {
			return nil, p
		}
		if err := s.CancelAdminRun(ctx, in.ID); err != nil {
			if errors.Is(err, historyimport.ErrRunNotCancelable) {
				return nil, NewProblem(TypeJobNotCancelable, "This import run cannot be canceled.")
			}
			return nil, adminHistoryProblem(err)
		}
		run, err := s.GetAdminRun(ctx, in.ID)
		if err != nil {
			return nil, adminHistoryProblem(err)
		}
		out := adminHistoryRunOutput(run)
		out.Location = adminHistoryRunLocation(run.ID)
		out.Status = http.StatusAccepted
		if out.Body.Terminal {
			out.Status = http.StatusOK
		}
		return out, nil
	})
	Register(reg, op(http.MethodPost, "/admin/history-imports/sources/{id}/bulk-run", "bulkCreateAdminHistoryImportRuns", false), func(ctx context.Context, in *AdminHistoryImportIDInput) (*AdminHistoryImportBulkOutput, error) {
		s, p := reg.adminHistoryImports()
		if p != nil {
			return nil, p
		}
		id, p := adminHistoryID(in.ID)
		if p != nil {
			return nil, p
		}
		result, err := s.BulkCreateAdminRuns(ctx, id)
		if err != nil {
			if errors.Is(err, historyimport.ErrBulkRunTooLarge) {
				return nil, NewProblem(TypeValidationFailed, "This source has more than 200 mappings; start individual mapping runs.")
			}
			return nil, adminHistoryProblem(err)
		}
		out := new(AdminHistoryImportBulkOutput)
		out.Body.Outcomes = make([]AdminHistoryImportBulkOutcome, 0, len(result.Outcomes))
		for _, r := range result.Outcomes {
			item := AdminHistoryImportBulkOutcome{MappingID: ID(strconv.Itoa(r.MappingID)), Status: r.Status}
			if r.Run != nil {
				item.Run = new(safeAdminHistoryRun(r.Run))
				item.Location = adminHistoryRunLocation(r.Run.ID)
			}
			switch r.Status {
			case adminHistoryAccepted:
				out.Body.Accepted++
			case "active":
				out.Body.Active++
			default:
				out.Body.Failed++
				item.Error = "This mapping could not start an import; review its source, target, and active runs."
			}
			out.Body.Outcomes = append(out.Body.Outcomes, item)
		}
		return out, nil
	})
	Register(reg, op(http.MethodGet, "/admin/history-imports/runs", "listAdminHistoryImportRuns", false), func(ctx context.Context, in *AdminHistoryImportListInput) (*AdminHistoryImportRunCollectionOutput, error) {
		s, p := reg.adminHistoryImports()
		if p != nil {
			return nil, p
		}
		var source *int
		if in.SourceID != "" {
			id, p := adminHistoryID(in.SourceID)
			if p != nil {
				return nil, p
			}
			source = &id
		}
		scope := adminHistoryCursorScope(ctx, "listAdminHistoryImportRuns", string(in.SourceID))
		scope.Sort = "-created_at,-id"
		var after *historyimport.RunKey
		if in.Cursor != "" {
			var position runPosition
			if p := cursors.Decode(scope, in.Cursor, &position); p != nil {
				return nil, p
			}
			if position.ID == "" || position.CreatedAt.IsZero() {
				return nil, NewProblem(TypeInvalidCursor, "Invalid run cursor.")
			}
			after = &historyimport.RunKey{CreatedAt: position.CreatedAt, ID: position.ID}
		}
		rows, more, err := s.ListAdminRunsPage(ctx, source, after, in.Limit)
		if err != nil {
			return nil, adminHistoryProblem(err)
		}
		next := ""
		if more && len(rows) > 0 {
			last := rows[len(rows)-1]
			next, err = cursors.Encode(scope, runPosition{CreatedAt: last.CreatedAt, ID: last.ID})
			if err != nil {
				return nil, NewProblem(TypeInternalError, "Unable to encode cursor.")
			}
		}
		items := make([]AdminHistoryImportRun, 0, len(rows))
		for i := range rows {
			items = append(items, safeAdminHistoryRun(&rows[i]))
		}
		return &AdminHistoryImportRunCollectionOutput{Body: Paginated(items, next)}, nil
	})
	Register(reg, op(http.MethodPost, "/admin/history-imports/plex/login", "loginAdminHistoryImportPlex", false), func(ctx context.Context, in *AdminHistoryImportPlexLoginInput) (*AdminHistoryImportPlexLoginOutput, error) {
		s, p := reg.adminHistoryImports()
		if p != nil {
			return nil, p
		}
		token, err := s.AuthenticatePlex(ctx, in.Body.Username, in.Body.Password)
		if err != nil {
			return nil, adminHistoryPlexLoginProblem(err)
		}
		out := new(AdminHistoryImportPlexLoginOutput)
		out.Body.Token = token
		return out, nil
	})
}

func adminHistoryRunOutput(run *historyimport.Run) *AdminHistoryImportRunOutput {
	out := &AdminHistoryImportRunOutput{Body: safeAdminHistoryRun(run)}
	data, _ := json.Marshal(out.Body)
	sum := sha256.Sum256(data)
	out.ETag = EntityTag{Opaque: hex.EncodeToString(sum[:])}.String()
	if !out.Body.Terminal {
		out.RetryAfter = "2"
	}
	return out
}
