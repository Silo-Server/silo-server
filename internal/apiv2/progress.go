package apiv2

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// The progress domain: a profile's watch progress.

// ProgressStatus is the listProgress status filter.
const (
	ProgressStatusInProgress = "in_progress"
	ProgressStatusCompleted  = "completed"
)

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
		n, err := strconv.Atoi(string(in.LibraryID))
		if err != nil || n <= 0 {
			return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: "query.library_id", Code: codeInvalid, Detail: "expected a library identifier"})
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
