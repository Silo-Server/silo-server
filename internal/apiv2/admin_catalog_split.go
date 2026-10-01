package apiv2

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type AdminCatalogSplitService interface {
	ListAdminItemFiles(context.Context, string, int, int) ([]handlers.AdminItemFileView, bool, error)
	SplitAdminItem(context.Context, string, handlers.AdminSplitRequest) (handlers.AdminSplitResult, error)
	MergeAdminItem(context.Context, string, string) (string, error)
}
type AdminItemFile struct {
	ID               ID     `json:"id"`
	LibraryID        ID     `json:"library_id"`
	FilePath         string `json:"file_path"`
	ObservedRootPath string `json:"observed_root_path"`
	SeasonNumber     int    `json:"season_number,omitzero"`
	EpisodeNumber    int    `json:"episode_number,omitzero"`
}
type AdminItemFilesInput struct {
	ID string `path:"id" minLength:"1" maxLength:"512"`
	LimitParam
	Cursor string `query:"cursor"`
}
type AdminItemFilesOutput struct{ Body Collection[AdminItemFile] }
type AdminSplitTarget struct {
	ProviderIDs map[string]string `json:"provider_ids,omitempty"`
	ContentID   string            `json:"content_id,omitempty" maxLength:"512"`
	Unmatched   bool              `json:"unmatched,omitempty"`
	Title       string            `json:"title,omitempty" maxLength:"2000"`
	Year        int               `json:"year,omitempty"`
}
type AdminSplitRequest struct {
	FileIDs         []ID             `json:"file_ids" minItems:"1" maxItems:"10000"`
	Target          AdminSplitTarget `json:"target"`
	HistoryMode     string           `json:"history_mode,omitempty" enum:",evidence,keep,move_all"`
	PersistOverride *bool            `json:"persist_override,omitempty" nullable:"true"`
	DryRun          bool             `json:"dry_run,omitempty"`
}
type AdminSplitInput struct {
	ID   string `path:"id" minLength:"1" maxLength:"512"`
	Body AdminSplitRequest
}
type AdminMergeInput struct {
	ID   string `path:"id" minLength:"1" maxLength:"512"`
	Body struct {
		Into string `json:"into" minLength:"1" maxLength:"512"`
	}
}
type AdminMergeResult struct {
	MergedInto string `json:"merged_into"`
}
type AdminMergeOutput struct{ Body AdminMergeResult }
type AdminSplitAmbiguousHistory struct {
	UserID    ID       `json:"user_id"`
	ProfileID string   `json:"profile_id"`
	WatchedAt *Instant `json:"watched_at,omitempty"`
}
type AdminSplitReattribution struct {
	PlaybackSessionLog int                          `json:"playback_session_log"`
	Downloads          int                          `json:"downloads"`
	ProgressMoved      int                          `json:"progress_moved"`
	ProgressConflicts  int                          `json:"progress_conflicts"`
	HistoryMoved       int                          `json:"history_moved"`
	HistoryStayed      int                          `json:"history_stayed"`
	HistoryAmbiguous   int                          `json:"history_ambiguous"`
	IntentMoved        int                          `json:"intent_moved"`
	EpisodePairsMoved  int                          `json:"episode_pairs_moved"`
	AmbiguousHistory   []AdminSplitAmbiguousHistory `json:"ambiguous_history,omitempty"`
}
type AdminSplitResult struct {
	DryRun          bool                    `json:"dry_run"`
	SourceContentID string                  `json:"source_content_id"`
	TargetContentID string                  `json:"target_content_id"`
	TargetCreated   bool                    `json:"target_created"`
	FilesMoved      int                     `json:"files_moved"`
	RootOverrides   []string                `json:"root_overrides"`
	FileOverrides   []string                `json:"file_overrides"`
	EpisodePairs    int                     `json:"episode_pairs"`
	Reattribution   AdminSplitReattribution `json:"reattribution"`
}
type AdminSplitOutput struct{ Body AdminSplitResult }

func registerAdminCatalogSplit(reg *Registry) {
	op := func(method, path, id string) Operation {
		o := Operation{Operation: humaOp(method, Prefix+"/admin/items/{id}/"+path, id, "admin-catalog", "Repair catalog item file grouping."), Class: ClassActingAdmin, ServiceBacked: true}
		if method != http.MethodGet {
			o.RetrySafety = RetrySafetyNonRetryable
		}
		return o
	}
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, op(http.MethodGet, "files", "listAdminItemFiles"), func(ctx context.Context, in *AdminItemFilesInput) (*AdminItemFilesOutput, error) {
		if reg.deps.AdminCatalogSplit == nil {
			return nil, unavailable("item splitting")
		}
		scope := adminPolicyListScope(ctx, "listAdminItemFiles", in.ID+"/"+strconv.Itoa(in.Limit), "id", "id")
		after := 0
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &after); p != nil {
				return nil, p
			}
			if after <= 0 {
				return nil, NewProblem(TypeInvalidCursor, "Invalid file cursor.")
			}
		}
		rows, more, err := reg.deps.AdminCatalogSplit.ListAdminItemFiles(ctx, in.ID, in.Limit, after)
		if err != nil {
			return nil, collectionProblem(err)
		}
		items := make([]AdminItemFile, 0, len(rows))
		for _, f := range rows {
			items = append(items, AdminItemFile{ID: IDFromInt(int64(f.ID)), LibraryID: IDFromInt(int64(f.LibraryID)), FilePath: f.FilePath, ObservedRootPath: f.ObservedRootPath, SeasonNumber: f.SeasonNumber, EpisodeNumber: f.EpisodeNumber})
		}
		next := ""
		if more && len(rows) > 0 {
			next, err = cursors.Encode(scope, rows[len(rows)-1].ID)
			if err != nil {
				return nil, serviceProblem(err)
			}
		}
		return &AdminItemFilesOutput{Body: Paginated(items, next)}, nil
	})
	Register(reg, op(http.MethodPost, "merge", "mergeAdminItem"), func(ctx context.Context, in *AdminMergeInput) (*AdminMergeOutput, error) {
		if reg.deps.AdminCatalogSplit == nil {
			return nil, unavailable("item splitting")
		}
		into, err := reg.deps.AdminCatalogSplit.MergeAdminItem(ctx, in.ID, in.Body.Into)
		if err != nil {
			return nil, collectionProblem(err)
		}
		return &AdminMergeOutput{Body: AdminMergeResult{MergedInto: into}}, nil
	})
	Register(reg, op(http.MethodPost, "split", "splitAdminItem"), func(ctx context.Context, in *AdminSplitInput) (*AdminSplitOutput, error) {
		if reg.deps.AdminCatalogSplit == nil {
			return nil, unavailable("item splitting")
		}
		b := in.Body
		ids := make([]int, 0, len(b.FileIDs))
		for i, v := range b.FileIDs {
			id, p := v.positive("body.file_ids[" + strconv.Itoa(i) + "]")
			if p != nil {
				return nil, p
			}
			ids = append(ids, id)
		}
		result, err := reg.deps.AdminCatalogSplit.SplitAdminItem(ctx, in.ID, handlers.AdminSplitRequest{FileIDs: ids, Target: handlers.AdminSplitTarget{ProviderIDs: b.Target.ProviderIDs, ContentID: b.Target.ContentID, Unmatched: b.Target.Unmatched, Title: b.Target.Title, Year: b.Target.Year}, HistoryMode: b.HistoryMode, PersistOverride: b.PersistOverride, DryRun: b.DryRun})
		if err != nil {
			return nil, collectionProblem(err)
		}
		out := AdminSplitResult{DryRun: result.DryRun, SourceContentID: result.SourceContentID, TargetContentID: result.TargetContentID, TargetCreated: result.TargetCreated, FilesMoved: result.FilesMoved, RootOverrides: result.RootOverrides, FileOverrides: result.FileOverrides, EpisodePairs: result.EpisodePairs}
		if out.RootOverrides == nil {
			out.RootOverrides = []string{}
		}
		if out.FileOverrides == nil {
			out.FileOverrides = []string{}
		}
		if r := result.Reattribution; r != nil {
			out.Reattribution = AdminSplitReattribution{PlaybackSessionLog: r.PlaybackSessionLog, Downloads: r.Downloads, ProgressMoved: r.ProgressMoved, ProgressConflicts: r.ProgressConflicts, HistoryMoved: r.HistoryMoved, HistoryStayed: r.HistoryStayed, HistoryAmbiguous: r.HistoryAmbiguous, IntentMoved: r.IntentMoved, EpisodePairsMoved: r.EpisodePairsMoved}
			for _, v := range r.AmbiguousHistory {
				watchedAt, p := storedInstant(v.WatchedAt)
				if p != nil {
					return nil, p
				}
				out.Reattribution.AmbiguousHistory = append(out.Reattribution.AmbiguousHistory, AdminSplitAmbiguousHistory{UserID: IDFromInt(int64(v.UserID)), ProfileID: v.ProfileID, WatchedAt: watchedAt})
			}
		}
		return &AdminSplitOutput{Body: out}, nil
	})
}
