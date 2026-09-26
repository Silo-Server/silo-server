package apiv2

import (
	"context"
	"net/http"
	"reflect"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// The progress domain: a profile's watch progress.

// ProgressEntry is one item's watch position for the acting profile.
type ProgressEntry struct {
	MediaItemID     ID      `json:"media_item_id" doc:"The catalog item" example:"movie-8f2c1a"`
	PositionSeconds float64 `json:"position_seconds" doc:"Playback position" example:"1325.5"`
	DurationSeconds float64 `json:"duration_seconds" doc:"Known runtime; 0 when unknown" example:"5400"`
	Completed       bool    `json:"completed" doc:"Whether the item counts as watched" example:"false"`
	UpdatedAt       Instant `json:"updated_at" doc:"When the position last changed" example:"2026-01-02T03:04:05.000Z"`
}

// ProgressListInput is the listProgress query.
type ProgressListInput struct {
	Status    string `query:"status" enum:"in_progress,completed" doc:"Only entries in this state; absent lists every entry" example:"in_progress"`
	LibraryID ID     `query:"library_id" doc:"Only entries whose item is in this library" example:"1"`
	LimitParam
	Cursor string `query:"cursor" doc:"Opaque cursor from page.next_cursor" example:"eyJvZmZzZXQiOjUwfQ"`
}

// ProgressCollectionOutput is the listProgress response.
type ProgressCollectionOutput struct {
	Body ProgressCollection
}

// progressPosition is the cursor payload: the keyset (updated_at,
// media_item_id) of the last entry the previous page emitted, in the store's
// own string form. The next page resumes strictly after it in the effective
// sort, so a row whose updated_at moves ahead during playback is neither
// repeated nor lets an older row slip past, and equal timestamps are ordered
// by the unique media_item_id.
type progressPosition struct {
	UpdatedAt   string `json:"u"`
	MediaItemID string `json:"m"`
}

// ProgressCollection is the named envelope the contract carries.
type ProgressCollection struct {
	Collection[ProgressEntry]
}

// ProgressSyncItem is one progress write of a sync batch.
type ProgressSyncItem struct {
	ClientRef      *string  `json:"client_ref,omitempty" minLength:"1" maxLength:"128"`
	MediaItemID    ID       `json:"media_item_id" minLength:"1" doc:"The catalog item" example:"movie-8f2c1a"`
	PositionMs     int64    `json:"position_ms" minimum:"0" doc:"Playback position in milliseconds" example:"1325500"`
	DurationMs     int64    `json:"duration_ms" minimum:"0" doc:"Known runtime in milliseconds; 0 when unknown" example:"5400000"`
	ForceOverwrite bool     `json:"force_overwrite,omitempty" doc:"Write the position as given instead of merging it with the stored one" example:"false"`
	UpdatedAt      *Instant `json:"updated_at,omitempty" doc:"Client event time of an offline-queued write; the server clamps it to now and merges last-write-wins on it" example:"2026-01-02T03:04:05.000Z"`
}

// ProgressSyncInput is the syncProgress command.
type ProgressSyncInput struct {
	Body struct {
		Items []ProgressSyncItem `json:"items" minItems:"1" maxItems:"100" doc:"Writes to apply, in order"`
	}
}

// ProgressSyncSuccess includes accepted writes and threshold/LWW no-ops.
type ProgressSyncSuccess struct {
	BulkCorrelation
	MediaItemID ID     `json:"media_item_id"`
	Status      string `json:"status" enum:"success"`
}

type ProgressSyncFailure struct {
	BulkCorrelation
	MediaItemID ID              `json:"media_item_id"`
	Status      string          `json:"status" enum:"failure"`
	Failure     BulkItemFailure `json:"failure"`
}

// ProgressSyncResult is one named, discriminated success or failure variant.
type ProgressSyncResult struct {
	BulkCorrelation
	MediaItemID ID               `json:"media_item_id"`
	Status      string           `json:"status"`
	Failure     *BulkItemFailure `json:"failure,omitempty"`
}

func (ProgressSyncResult) Schema(r huma.Registry) *huma.Schema {
	return &huma.Schema{OneOf: []*huma.Schema{
		r.Schema(reflect.TypeFor[ProgressSyncSuccess](), true, ""),
		r.Schema(reflect.TypeFor[ProgressSyncFailure](), true, ""),
	}}
}

type ProgressSyncBatchResult struct {
	Items   []ProgressSyncResult `json:"items"`
	Summary BulkSummary          `json:"summary"`
}

// ProgressSyncOutput is always HTTP 200 after a per-item batch completes.
type ProgressSyncOutput struct{ Body ProgressSyncBatchResult }

// opListProgress is the operation id; the cursor scope is bound to it.
const opListProgress = "listProgress"

func registerProgress(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/progress", opListProgress, "progress",
			"List the acting profile's watch progress, newest change first."),
		Class:         ClassProfileScoped,
		ServiceBacked: true,
	}, func(ctx context.Context, in *ProgressListInput) (*ProgressCollectionOutput, error) {
		return reg.listProgress(ctx, cursors, in)
	})
	Register(reg, Operation{
		Operation: humaOp(http.MethodPost, Prefix+"/sync/progress", "syncProgress", "progress",
			"Apply a batch of progress writes for the acting profile and answer one result per item; supply updated_at on each item to preserve event ordering."),
		RetrySafety:   RetrySafetyNonRetryable,
		Class:         ClassProfileScoped,
		ServiceBacked: true,
	}, reg.syncProgress)
}

// syncProgress runs the same batch write as v1 POST /sync/progress; a write
// that fails is an error result, not a failed batch.
func (reg *Registry) syncProgress(ctx context.Context, in *ProgressSyncInput) (*ProgressSyncOutput, error) {
	if reg.deps.Progress == nil {
		return nil, unavailable("progress")
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	if p := validateBulkIdentity(in.Body.Items, func(item ProgressSyncItem) (string, *string) { return string(item.MediaItemID), item.ClientRef }, "media_item_id"); p != nil {
		return nil, p
	}
	updates := make([]handlers.ProgressSyncUpdate, 0, len(in.Body.Items))
	for _, item := range in.Body.Items {
		update := handlers.ProgressSyncUpdate{
			MediaItemID:    string(item.MediaItemID),
			CheckAccess:    true,
			Position:       float64(item.PositionMs) / 1000,
			Duration:       float64(item.DurationMs) / 1000,
			ForceOverwrite: item.ForceOverwrite,
		}
		if item.UpdatedAt != nil {
			t := item.UpdatedAt.Time
			update.UpdatedAt = &t
		}
		updates = append(updates, update)
	}
	results, err := reg.deps.Progress.SyncProgress(ctx, userID, profileID, updates)
	if err != nil {
		return nil, serviceProblem(err)
	}
	if len(results) != len(in.Body.Items) {
		return nil, NewProblem(TypeInternalError, "Progress results were incomplete.")
	}
	out := &ProgressSyncOutput{Body: ProgressSyncBatchResult{Items: make([]ProgressSyncResult, 0, len(results)), Summary: BulkSummary{Total: len(results)}}}
	for i, r := range results {
		item := ProgressSyncResult{BulkCorrelation: BulkCorrelation{Index: i, ClientRef: in.Body.Items[i].ClientRef}, MediaItemID: in.Body.Items[i].MediaItemID, Status: "success"}
		if r.Status == "ok" {
			out.Body.Summary.Succeeded++
		} else {
			item.Status = "failure"
			kind := TypeInternalError
			detail := "Progress could not be updated."
			if r.FailureStatus == http.StatusNotFound {
				kind = TypeNotFound
				detail = "Catalog item not found."
			}
			item.Failure = bulkFailure(kind, detail)
			out.Body.Summary.Failed++
		}
		out.Body.Items = append(out.Body.Items, item)
	}
	return out, nil
}

// listProgress answers from the same listing v1 GET /progress (without
// ?since=) uses, including its viewer-access and library filters.
func (reg *Registry) listProgress(ctx context.Context, cursors *Cursors, in *ProgressListInput) (*ProgressCollectionOutput, error) {
	if reg.deps.Progress == nil {
		return nil, unavailable("progress")
	}
	claims := claimsFrom(ctx)
	profileID := profileFrom(ctx)
	if claims == nil || profileID == "" {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	libraryID := 0
	if in.LibraryID != "" {
		n, err := intOfID(in.LibraryID)
		if err != nil || n <= 0 {
			return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: locationQueryLibraryID, Code: codeInvalid, Detail: detailLibraryIDInvalid})
		}
		libraryID = n
	}
	scope := CursorScope{
		OperationID: opListProgress,
		// The listing is access-filtered, so the cursor also dies with the
		// viewer policy it was minted under.
		Security:   strconv.Itoa(claims.UserID) + "/" + profileID + "/" + viewerScopeDigest(ctx),
		Filter:     in.Status + "|" + string(in.LibraryID),
		Sort:       "-updated_at,-media_item_id",
		Tiebreaker: "media_item_id",
	}
	var after *userstore.ProgressKey
	if in.Cursor != "" {
		var pos progressPosition
		if p := cursors.Decode(scope, in.Cursor, &pos); p != nil {
			return nil, p
		}
		after = &userstore.ProgressKey{UpdatedAt: pos.UpdatedAt, MediaItemID: pos.MediaItemID}
	}
	// The seam applies the access and library filters before deciding
	// has_more, so a filtered-out row never hides the rows behind it.
	entries, hasMore, err := reg.deps.Progress.ListProgressPage(ctx, claims.UserID, profileID, in.Status, libraryID, after, in.Limit)
	if err != nil {
		return nil, serviceProblem(err)
	}
	next := ""
	if hasMore && len(entries) > 0 {
		last := entries[len(entries)-1]
		next, err = cursors.Encode(scope, progressPosition{UpdatedAt: last.UpdatedAt, MediaItemID: last.MediaItemID})
		if err != nil {
			return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
		}
	}
	items := make([]ProgressEntry, 0, len(entries))
	for _, e := range entries {
		entry, err := progressEntryOf(e)
		if err != nil {
			return nil, err
		}
		items = append(items, entry)
	}
	return &ProgressCollectionOutput{Body: ProgressCollection{Collection: Paginated(items, next)}}, nil
}

func progressEntryOf(e userstore.WatchProgress) (ProgressEntry, *Problem) {
	updated, err := storeInstant(e.UpdatedAt)
	if err != nil {
		return ProgressEntry{}, err
	}
	return ProgressEntry{
		MediaItemID:     ID(e.MediaItemID),
		PositionSeconds: e.PositionSeconds,
		DurationSeconds: e.DurationSeconds,
		Completed:       e.Completed,
		UpdatedAt:       updated,
	}, nil
}

// storeInstant parses the RFC 3339 strings the user store keeps its
// timestamps in. A value that does not parse is a server defect, never a
// client one.
func storeInstant(raw string) (Instant, *Problem) {
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || t.IsZero() {
		return Instant{}, NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	return NewInstant(t), nil
}
