package apiv2

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// The history domain: what a profile has watched.

// HistoryWatch is the most recent watch record behind a history card.
type HistoryWatch struct {
	MediaItemID     ID      `json:"media_item_id" doc:"The item that was watched; an episode when the card is its series" example:"episode:heat-s01e01"`
	WatchedAt       Instant `json:"watched_at" doc:"When the watch was recorded" example:"2026-01-02T03:04:05.000Z"`
	DurationSeconds float64 `json:"duration_seconds" doc:"Known runtime at the time; 0 when unknown" example:"5400"`
	Completed       bool    `json:"completed" doc:"Whether the watch counted as finished" example:"true"`
	Source          string  `json:"source,omitempty" doc:"How the watch was recorded: playback, manual, import, legacy" example:"playback"`
}

// HistoryCard is a catalog card with the watch it stands for. An episode
// watch is shown as its series card, as v1 does.
type HistoryCard struct {
	CatalogItem
	Watch HistoryWatch `json:"watch" doc:"The most recent watch of the card's item"`
}

// HistoryListInput is the listHistory query.
type HistoryListInput struct {
	ImageSize string `query:"image_size" enum:"small,medium,large,original" doc:"Artwork variant to presign; absent picks each surface's default" example:"medium"`
	LimitParam
	Cursor string `query:"cursor" doc:"Opaque cursor from page.next_cursor" example:"eyJvIjo1MH0"`
}

// HistoryCollection is the named envelope the contract carries.
type HistoryCollection struct {
	Collection[HistoryCard]
}

// HistoryCollectionOutput is the listHistory response.
type HistoryCollectionOutput struct {
	Body HistoryCollection
}

// HistoryRemovalTarget is one thing to remove from history.
type HistoryRemovalTarget struct {
	ContentID string `json:"content_id" minLength:"1" doc:"A movie, ebook, series, season or episode" example:"episode:heat-s01e01"`
	Scope     string `json:"scope,omitempty" enum:"item,show" doc:"item (default) removes the target itself, a series or season expanding to its episodes; show widens a season or episode to its whole series" example:"item"`
}

// HistoryRemoveInput is the removeHistoryEntries command.
type HistoryRemoveInput struct {
	Body struct {
		Targets []HistoryRemovalTarget `json:"targets" minItems:"1" doc:"Targets to hide from history"`
	}
}

// opListHistory is the operation id; the cursor scope is bound to it.
const opListHistory = "listHistory"

func registerHistory(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/history", opListHistory, "history",
			"List the acting profile's watch history as catalog cards, most recent watch first."),
		Class:         ClassProfileScoped,
		ServiceBacked: true,
	}, func(ctx context.Context, in *HistoryListInput) (*HistoryCollectionOutput, error) {
		return reg.listHistory(ctx, cursors, in)
	})

	remove := humaOp(http.MethodPost, Prefix+"/history/remove", "removeHistoryEntries", "history",
		"Hide the targets' watches from the acting profile's history; hiding an already hidden item is a no-op.")
	remove.DefaultStatus = http.StatusNoContent
	Register(reg, Operation{Operation: remove, Class: ClassProfileScoped, ServiceBacked: true}, reg.removeHistoryEntries)
}

// listHistory pages the same rows v1 GET /history does; the cursor carries
// the offset the store pages by underneath.
func (reg *Registry) listHistory(ctx context.Context, cursors *Cursors, in *HistoryListInput) (*HistoryCollectionOutput, error) {
	if reg.deps.History == nil {
		return nil, unavailable("history")
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	scope := CursorScope{
		OperationID: opListHistory,
		// The cards are access-filtered, so the cursor dies with the viewer
		// policy it was minted under.
		Security:   strconv.Itoa(userID) + "/" + profileID + "/" + viewerScopeDigest(ctx),
		Filter:     in.ImageSize,
		Sort:       "-watched_at",
		Tiebreaker: "offset",
	}
	offset, p := decodeOffset(cursors, scope, in.Cursor)
	if p != nil {
		return nil, p
	}
	entries, err := reg.deps.History.HistoryEntries(ctx, userID, profileID, in.Limit+1, offset)
	if err != nil {
		return nil, serviceProblem(err)
	}
	entries, next, p := offsetPage(cursors, scope, len(entries), in.Limit, offset, entries)
	if p != nil {
		return nil, p
	}
	cards, err := reg.deps.History.HistoryCards(ctx, sectionViewer(ctx, in.ImageSize), entries)
	if err != nil {
		return nil, serviceProblem(err)
	}
	items := make([]HistoryCard, 0, len(cards))
	for _, c := range cards {
		card, p := historyCardOf(c)
		if p != nil {
			return nil, p
		}
		items = append(items, card)
	}
	return &HistoryCollectionOutput{Body: HistoryCollection{Collection: Paginated(items, next)}}, nil
}

func historyCardOf(v handlers.HistoryCardView) (HistoryCard, *Problem) {
	watched, p := storeInstant(v.Entry.WatchedAt)
	if p != nil {
		return HistoryCard{}, p
	}
	return HistoryCard{
		CatalogItem: catalogItemOfListing(v.Item),
		Watch: HistoryWatch{
			MediaItemID:     ID(v.Entry.MediaItemID),
			WatchedAt:       watched,
			DurationSeconds: v.Entry.DurationSeconds,
			Completed:       v.Entry.Completed,
			Source:          string(v.Entry.Source),
		},
	}, nil
}

// removeHistoryEntries runs the same command as v1 POST /history/remove.
func (reg *Registry) removeHistoryEntries(ctx context.Context, in *HistoryRemoveInput) (*struct{}, error) {
	if reg.deps.History == nil {
		return nil, unavailable("history")
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	targets := make([]handlers.HistoryRemovalTarget, 0, len(in.Body.Targets))
	for _, t := range in.Body.Targets {
		targets = append(targets, handlers.HistoryRemovalTarget{ContentID: t.ContentID, Scope: t.Scope})
	}
	if err := reg.deps.History.RemoveHistory(ctx, userID, profileID, handlers.AccessFilterFromContext(ctx, ""), targets); err != nil {
		return nil, fieldProblem(err)
	}
	return nil, nil
}

// fieldProblem maps a seam's decision onto problem types: a rejected member
// is a validation failure naming it; the rest follow the status.
func fieldProblem(err error) *Problem {
	apiErr, ok := err.(*handlers.APIError) //nolint:errorlint // the seams return the value directly
	if ok && apiErr.Field != "" {
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationBody + "." + apiErr.Field, Code: codeInvalid, Detail: apiErr.Message})
	}
	return serviceProblem(err)
}

// HistoryService is the slice of *handlers.PersonalDataHandler the history
// operations use.
type HistoryService interface {
	HistoryEntries(ctx context.Context, userID int, profileID string, limit, offset int) ([]userstore.WatchHistoryEntry, error)
	HistoryCards(ctx context.Context, viewer handlers.SectionViewer, entries []userstore.WatchHistoryEntry) ([]handlers.HistoryCardView, error)
	RemoveHistory(ctx context.Context, userID int, profileID string, filter catalogpkg.AccessFilter, targets []handlers.HistoryRemovalTarget) error
}
