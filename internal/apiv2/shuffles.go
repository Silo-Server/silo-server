package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/shuffle"
)

// ShuffleAPI starts and advances shuffles (*shuffle.Service).
type ShuffleAPI interface {
	Create(ctx context.Context, owner shuffle.Owner, access catalogpkg.AccessFilter, scope shuffle.Scope) (*shuffle.Shuffle, error)
	Get(ctx context.Context, owner shuffle.Owner, access catalogpkg.AccessFilter, id string) (*shuffle.Shuffle, error)
	Advance(ctx context.Context, owner shuffle.Owner, access catalogpkg.AccessFilter, id, fromContentID string) (*shuffle.Shuffle, error)
	Skip(ctx context.Context, owner shuffle.Owner, access catalogpkg.AccessFilter, id, skipContentID string) (*shuffle.Shuffle, error)
	Delete(ctx context.Context, owner shuffle.Owner, id string) error
}

// ShuffleCapability reports that the server can shuffle. Clients show
// shuffle actions only when state is available.
type ShuffleCapability struct {
	Capability
	ScopeKinds []string `json:"scope_kinds" enum:"library,series,season,library_collection,user_collection" doc:"Scope kinds createShuffle accepts. Empty, never null"`
}

// ShuffleCapabilityOutput is the getShuffleCapability response.
type ShuffleCapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         ShuffleCapability
}

// ShuffleScope is what a shuffle draws from.
type ShuffleScope struct {
	Kind        string `json:"kind" enum:"library,series,season,library_collection,user_collection" doc:"library plays the movies and episodes of one library; series plays a series' episodes; season plays one season's episodes; library_collection and user_collection play a collection's movies and its series' episodes. Specials play like any other episode"`
	ID          string `json:"id" doc:"The library ID, series or season content ID, or collection ID" example:"series-tvdb-81189"`
	Title       string `json:"title" doc:"The scope's name when the shuffle started" example:"Breaking Bad"`
	ParentTitle string `json:"parent_title,omitempty" doc:"A season scope's series title; absent for other scopes" example:"Breaking Bad"`
}

// Shuffle is a running shuffle: the item playing now and the one picked to
// play after it.
type Shuffle struct {
	ID        ID           `json:"id" doc:"Shuffle id"`
	Scope     ShuffleScope `json:"scope"`
	Current   CatalogItem  `json:"current" doc:"The item to play now"`
	Next      CatalogItem  `json:"next" doc:"The item that plays after current. It equals current only when the scope has one playable item"`
	CreatedAt Instant      `json:"created_at"`
	UpdatedAt Instant      `json:"updated_at"`
}

// ShuffleScopeRequest names the scope a new shuffle draws from.
type ShuffleScopeRequest struct {
	Kind string `json:"kind" enum:"library,series,season,library_collection,user_collection"`
	ID   string `json:"id" minLength:"1" doc:"The library ID, series or season content ID, or collection ID"`
}

// CreateShuffleRequest starts a shuffle.
type CreateShuffleRequest struct {
	Scope ShuffleScopeRequest `json:"scope"`
}

// AdvanceShuffleRequest moves a shuffle to its next item.
type AdvanceShuffleRequest struct {
	FromContentID string `json:"from_content_id" minLength:"1" doc:"The content ID of the item that finished. The shuffle advances only while this item is still current, so a retry after a lost response changes nothing"`
}

// SkipShuffleItemRequest replaces a shuffle's next item.
type SkipShuffleItemRequest struct {
	NextContentID string `json:"next_content_id" minLength:"1" doc:"The content ID of the next item to skip. The shuffle picks another only while this item is still next, so a retry after a lost response changes nothing"`
}

// CreateShuffleInput is the createShuffle request.
type CreateShuffleInput struct {
	ImageSize string `query:"image_size" enum:"small,medium,large,original" doc:"Artwork variant to presign on the item cards"`
	Body      CreateShuffleRequest
}

// ShuffleInput names one shuffle.
type ShuffleInput struct {
	ShuffleID string `path:"shuffle_id" doc:"Shuffle id"`
	ImageSize string `query:"image_size" enum:"small,medium,large,original" doc:"Artwork variant to presign on the item cards"`
}

// DeleteShuffleInput names the shuffle to stop.
type DeleteShuffleInput struct {
	ShuffleID string `path:"shuffle_id" doc:"Shuffle id"`
}

// AdvanceShuffleInput is the advanceShuffle request.
type AdvanceShuffleInput struct {
	ShuffleInput
	Body AdvanceShuffleRequest
}

// SkipShuffleItemInput is the skipShuffleItem request.
type SkipShuffleItemInput struct {
	ShuffleInput
	Body SkipShuffleItemRequest
}

// ShuffleOutput carries a shuffle.
type ShuffleOutput struct {
	Body Shuffle
}

// ShuffleCreatedOutput carries a new shuffle and where it lives.
type ShuffleCreatedOutput struct {
	Location string `header:"Location"`
	Body     Shuffle
}

var shuffleScopeKinds = []string{
	string(shuffle.ScopeLibrary), string(shuffle.ScopeSeries), string(shuffle.ScopeSeason),
	string(shuffle.ScopeLibraryCollection), string(shuffle.ScopeUserCollection),
}

func registerShuffles(reg *Registry) {
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/shuffles/capabilities", "getShuffleCapability", "playback",
		"Whether the server can shuffle, and the scopes a shuffle can draw from.")), reg.getShuffleCapability)

	create := viewerOperation(humaOp(http.MethodPost, Prefix+"/shuffles", "createShuffle", "playback",
		"Start a shuffle: random movies and episodes from a library, series, season, or collection, with no repeats until all of them have played."))
	create.DefaultStatus = http.StatusCreated
	// A retry after a lost response starts a second shuffle. The first is
	// never read again and is deleted with other abandoned shuffles.
	create.RetrySafety = RetrySafetyNonRetryable
	create.Errors = []int{http.StatusNotFound, http.StatusConflict}
	Register(reg, create, reg.createShuffle)

	get := viewerOperation(humaOp(http.MethodGet, Prefix+"/shuffles/{shuffle_id}", "getShuffle", "playback",
		"Read a shuffle: what plays now and what plays next."))
	get.Errors = []int{http.StatusNotFound, http.StatusConflict}
	Register(reg, get, reg.getShuffle)

	advance := viewerOperation(humaOp(http.MethodPost, Prefix+"/shuffles/{shuffle_id}/advance", "advanceShuffle", "playback",
		"Move a shuffle on after its current item: the next item becomes current and another is picked to follow it."))
	advance.RetrySafety = RetrySafetyNaturalIdempotent
	advance.Errors = []int{http.StatusNotFound, http.StatusConflict}
	Register(reg, advance, reg.advanceShuffle)

	skip := viewerOperation(humaOp(http.MethodPost, Prefix+"/shuffles/{shuffle_id}/skip", "skipShuffleItem", "playback",
		"Replace a shuffle's next item with another pick. The skipped item never played, so it can still come up later in the cycle."))
	skip.RetrySafety = RetrySafetyNaturalIdempotent
	skip.Errors = []int{http.StatusNotFound, http.StatusConflict}
	Register(reg, skip, reg.skipShuffleItem)

	remove := viewerOperation(humaOp(http.MethodDelete, Prefix+"/shuffles/{shuffle_id}", "deleteShuffle", "playback",
		"Stop a shuffle. Stopping one that is already gone succeeds."))
	remove.DefaultStatus = http.StatusNoContent
	remove.RetrySafety = RetrySafetyNaturalIdempotent
	Register(reg, remove, reg.deleteShuffle)
}

func (reg *Registry) getShuffleCapability(ctx context.Context, _ *CapabilityInput) (*ShuffleCapabilityOutput, error) {
	doc := ShuffleCapability{Capability: Capability{State: StateAvailable}, ScopeKinds: shuffleScopeKinds}
	if reg.deps.Shuffles == nil || reg.deps.CatalogItems == nil || reg.deps.CatalogAccess == nil {
		doc = ShuffleCapability{Capability: Capability{State: StateNotConfigured}, ScopeKinds: []string{}}
	}
	return &ShuffleCapabilityOutput{CacheControl: cacheControlPrivateNoCache, Body: doc}, nil
}

// shuffleViewer resolves what every shuffle operation needs: the service,
// the owner, and the viewer the item cards render for.
func (reg *Registry) shuffleViewer(ctx context.Context, imageSize string) (ShuffleAPI, shuffle.Owner, handlers.ItemViewer, *Problem) {
	if reg.deps.Shuffles == nil {
		return nil, shuffle.Owner{}, handlers.ItemViewer{}, unavailable("shuffle")
	}
	if _, p := reg.catalogItems(); p != nil {
		return nil, shuffle.Owner{}, handlers.ItemViewer{}, p
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, shuffle.Owner{}, handlers.ItemViewer{}, p
	}
	viewer, p := reg.itemViewer(ctx, imageSize, "", "")
	if p != nil {
		return nil, shuffle.Owner{}, handlers.ItemViewer{}, p
	}
	return reg.deps.Shuffles, shuffle.Owner{UserID: userID, ProfileID: profileID}, viewer, nil
}

func (reg *Registry) createShuffle(ctx context.Context, in *CreateShuffleInput) (*ShuffleCreatedOutput, error) {
	svc, owner, viewer, p := reg.shuffleViewer(ctx, in.ImageSize)
	if p != nil {
		return nil, p
	}
	scope := shuffle.Scope{Kind: shuffle.ScopeKind(in.Body.Scope.Kind), ID: in.Body.Scope.ID}
	created, err := svc.Create(ctx, owner, viewer.Access, scope)
	if err != nil {
		return nil, shuffleProblem(err)
	}
	body, p := reg.shuffleOf(ctx, viewer, created)
	if p != nil {
		return nil, p
	}
	return &ShuffleCreatedOutput{Location: Prefix + "/shuffles/" + created.ID, Body: body}, nil
}

func (reg *Registry) getShuffle(ctx context.Context, in *ShuffleInput) (*ShuffleOutput, error) {
	svc, owner, viewer, p := reg.shuffleViewer(ctx, in.ImageSize)
	if p != nil {
		return nil, p
	}
	current, err := svc.Get(ctx, owner, viewer.Access, in.ShuffleID)
	if err != nil {
		return nil, shuffleProblem(err)
	}
	return reg.shuffleOutput(ctx, viewer, current)
}

func (reg *Registry) advanceShuffle(ctx context.Context, in *AdvanceShuffleInput) (*ShuffleOutput, error) {
	svc, owner, viewer, p := reg.shuffleViewer(ctx, in.ImageSize)
	if p != nil {
		return nil, p
	}
	advanced, err := svc.Advance(ctx, owner, viewer.Access, in.ShuffleID, in.Body.FromContentID)
	if err != nil {
		return nil, shuffleProblem(err)
	}
	return reg.shuffleOutput(ctx, viewer, advanced)
}

func (reg *Registry) skipShuffleItem(ctx context.Context, in *SkipShuffleItemInput) (*ShuffleOutput, error) {
	svc, owner, viewer, p := reg.shuffleViewer(ctx, in.ImageSize)
	if p != nil {
		return nil, p
	}
	skipped, err := svc.Skip(ctx, owner, viewer.Access, in.ShuffleID, in.Body.NextContentID)
	if err != nil {
		return nil, shuffleProblem(err)
	}
	return reg.shuffleOutput(ctx, viewer, skipped)
}

func (reg *Registry) deleteShuffle(ctx context.Context, in *DeleteShuffleInput) (*struct{}, error) {
	if reg.deps.Shuffles == nil {
		return nil, unavailable("shuffle")
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	if err := reg.deps.Shuffles.Delete(ctx, shuffle.Owner{UserID: userID, ProfileID: profileID}, in.ShuffleID); err != nil {
		return nil, shuffleProblem(err)
	}
	return nil, nil
}

func (reg *Registry) shuffleOutput(ctx context.Context, viewer handlers.ItemViewer, s *shuffle.Shuffle) (*ShuffleOutput, error) {
	body, p := reg.shuffleOf(ctx, viewer, s)
	if p != nil {
		return nil, p
	}
	return &ShuffleOutput{Body: body}, nil
}

// shuffleOf renders a shuffle with cards for its current and next items, as
// the catalog renders them for this viewer.
func (reg *Registry) shuffleOf(ctx context.Context, viewer handlers.ItemViewer, s *shuffle.Shuffle) (Shuffle, *Problem) {
	current, p := reg.shuffleCard(ctx, viewer, s.CurrentContentID)
	if p != nil {
		return Shuffle{}, p
	}
	next := current
	if s.NextContentID != s.CurrentContentID {
		if next, p = reg.shuffleCard(ctx, viewer, s.NextContentID); p != nil {
			return Shuffle{}, p
		}
	}
	return Shuffle{
		ID:        ID(s.ID),
		Scope:     ShuffleScope{Kind: string(s.Scope.Kind), ID: s.Scope.ID, Title: s.Title, ParentTitle: s.ParentTitle},
		Current:   current,
		Next:      next,
		CreatedAt: NewInstant(s.CreatedAt),
		UpdatedAt: NewInstant(s.UpdatedAt),
	}, nil
}

func (reg *Registry) shuffleCard(ctx context.Context, viewer handlers.ItemViewer, contentID string) (CatalogItem, *Problem) {
	detail, err := reg.deps.CatalogItems.ItemDetail(ctx, viewer, contentID)
	if err != nil {
		return CatalogItem{}, serviceProblem(err)
	}
	return withShownRatings(catalogItemCardOf(detail), reg.ratingSelection(ctx)), nil
}

func shuffleProblem(err error) *Problem {
	switch {
	case errors.Is(err, shuffle.ErrNotFound):
		return NewProblem(TypeNotFound, "The shuffle does not exist.")
	case errors.Is(err, shuffle.ErrScopeNotFound):
		return NewProblem(TypeNotFound, "The library, series, season, or collection does not exist.")
	case errors.Is(err, shuffle.ErrUnsupportedScope):
		return NewProblem(TypeConflict, "This library holds no movies or episodes to shuffle.")
	case errors.Is(err, shuffle.ErrEmpty):
		return NewProblem(TypeConflict, "Nothing here can be played.")
	}
	return serviceProblem(err)
}
