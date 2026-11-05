package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Title allowances let the household's primary profile allow a limited
// profile specific movies and series above its maturity limits, one by one or
// through a personal collection. Storage is titleAllowanceStore;
// enforcement is access.MaturityLimits.AllowedTitleIDs.

// titleAllowanceStore keeps the accounts' title and collection allowances
// (*pgstore.TitleAllowances). It exists only where the user store is
// PostgreSQL; under another store no allowance can apply.
type titleAllowanceStore interface {
	// ListTitleAllowances lists the profile's own title allowances, newest
	// first, leaving out titles allowed only through a collection.
	ListTitleAllowances(ctx context.Context, userID int, profileID string) ([]userstore.TitleAllowance, error)
	// SetTitleAllowance adds or removes one title allowance and reports
	// whether a row changed.
	SetTitleAllowance(ctx context.Context, m userstore.AllowanceManager, profileID, contentID string, allowed bool) (bool, error)
	// ListCollectionAllowances lists the profile's collection allowances,
	// newest first.
	ListCollectionAllowances(ctx context.Context, userID int, profileID string) ([]userstore.CollectionAllowance, error)
	// SetCollectionAllowance adds or removes one collection allowance and
	// reports whether a row changed.
	SetCollectionAllowance(ctx context.Context, m userstore.AllowanceManager, profileID, collectionID string, allowed bool) (bool, error)
	// UnallowedCollectionTitles lists one page, in the collection's order and
	// after the given position, of the movies and series a personal
	// collection stores that the profile is not yet allowed.
	UnallowedCollectionTitles(ctx context.Context, userID int, profileID, collectionID string, after userstore.CollectionPosition, limit int) ([]userstore.CollectionPosition, error)
	// AllowTitles adds a title allowance for every movie and series among
	// contentIDs and returns how many were new.
	AllowTitles(ctx context.Context, m userstore.AllowanceManager, profileID string, contentIDs []string) (int, error)
}

// smartCollectionQuery evaluates a smart collection's query for a viewer
// (*catalog.QueryExecutor); smart collections store no items.
type smartCollectionQuery interface {
	Preview(ctx context.Context, def catalog.QueryDefinition, access catalog.AccessFilter, limit int) ([]*models.MediaItem, int, error)
}

// TitleAllowanceHandler serves the title-allowance operations.
type TitleAllowanceHandler struct {
	personal   *PersonalDataHandler
	scopes     apimw.ViewerResolver
	allowances titleAllowanceStore
	smart      smartCollectionQuery
}

// NewTitleAllowanceHandler builds the service from the personal-data handler,
// which owns item lookup and catalog cards, a scope resolver, and the
// allowance store; a nil store leaves the feature unsupported.
func NewTitleAllowanceHandler(personal *PersonalDataHandler, scopes apimw.ViewerResolver, allowances titleAllowanceStore) *TitleAllowanceHandler {
	return &TitleAllowanceHandler{personal: personal, scopes: scopes, allowances: allowances}
}

// WithSmartCollections lets AllowCollectionTitles copy smart collections by
// evaluating their query, and returns the handler.
func (h *TitleAllowanceHandler) WithSmartCollections(query *catalog.QueryExecutor) *TitleAllowanceHandler {
	if h != nil && query != nil {
		h.smart = query
	}
	return h
}

// AllowanceActor is the caller of a title-allowance operation: the account,
// the acting profile, the verifier for a PIN-locked primary profile, and the
// viewer whose scope renders catalog cards.
type AllowanceActor struct {
	UserID          int
	ActiveProfileID string
	VerifyProfile   func(*userstore.Profile) error
	Viewer          PersonalListViewer
}

// Title allowance states for one profile and one title.
const (
	AllowanceStateWithinLimits        = "within_limits"
	AllowanceStateAllowed             = "allowed"
	AllowanceStateAllowedByCollection = "allowed_by_collection"
	AllowanceStateAboveLimit          = "above_limit"
	AllowanceStateNoLibraryAccess     = "no_library_access"
)

// ProfileAllowanceState is one household profile's standing for a title.
type ProfileAllowanceState struct {
	Profile userstore.Profile
	State   string
}

// Supported reports whether allowances are kept for the account. They are
// only where the user store is PostgreSQL.
func (h *TitleAllowanceHandler) Supported(context.Context, int) bool {
	return h != nil && h.personal != nil && h.allowances != nil
}

// manage opens the account's user store after checking that allowances are
// kept and the actor manages the household, and returns the manager a write
// rechecks in its transaction.
func (h *TitleAllowanceHandler) manage(ctx context.Context, actor AllowanceActor) (userstore.UserStore, userstore.AllowanceManager, error) {
	manager := userstore.AllowanceManager{UserID: actor.UserID}
	if !h.Supported(ctx, actor.UserID) {
		return nil, manager, apiError(http.StatusNotImplemented, "unsupported", "Title allowances need the PostgreSQL user store")
	}
	store, err := h.personal.storeProvider.ForUser(ctx, actor.UserID)
	if err != nil || store == nil {
		return nil, manager, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	allowed, acting, err := householdManagerAs(ctx, store, actor.ActiveProfileID, actor.VerifyProfile)
	if err != nil {
		return nil, manager, profileManagementError(err)
	}
	if !allowed {
		return nil, manager, apiError(http.StatusForbidden, "forbidden", "Only the primary profile can manage allowed titles")
	}
	if acting != nil {
		manager.ProfileID, manager.PINRevision = acting.ID, acting.PINRevision
	}
	return store, manager, nil
}

// allowanceWriteError answers a failed allowance write: a manager whose PIN
// changed after authorization is refused as unverified, as other household
// writes are; anything else is a server failure with the given message.
func allowanceWriteError(err error, message string) error {
	if errors.Is(err, userstore.ErrAllowanceManagerChanged) {
		return profileManagementError(access.ErrProfileUnverified)
	}
	return apiError(http.StatusInternalServerError, "internal_error", message)
}

// targetProfile loads a profile on the account, 404 when there is none.
func targetProfile(ctx context.Context, store userstore.UserStore, profileID string) (*userstore.Profile, error) {
	if !auth.ValidTextKey(profileID) {
		return nil, apiError(http.StatusNotFound, "not_found", "Profile not found")
	}
	profile, err := store.GetProfile(ctx, profileID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load profile")
	}
	if profile == nil {
		return nil, apiError(http.StatusNotFound, "not_found", "Profile not found")
	}
	return profile, nil
}

// requireMaturityLimits refuses a profile with no limit to make an exception
// to.
func requireMaturityLimits(p *userstore.Profile) error {
	limits := access.MaturityLimits{MaxContentRating: p.MaxContentRating, MaxAdvisoryAge: p.MaxAdvisoryAge}
	if limits.Active() {
		return nil
	}
	return apiError(http.StatusUnprocessableEntity, "no_maturity_limits", "The profile has no rating or advisory-age limit to make an exception to")
}

// ListAllowedTitles lists a profile's own title allowances, newest first, and
// the cards of the allowed titles the actor may see, in the same order.
func (h *TitleAllowanceHandler) ListAllowedTitles(ctx context.Context, actor AllowanceActor, profileID string) ([]userstore.TitleAllowance, []CollectionItemView, error) {
	store, _, err := h.manage(ctx, actor)
	if err != nil {
		return nil, nil, err
	}
	if _, err := targetProfile(ctx, store, profileID); err != nil {
		return nil, nil, err
	}
	entries, err := h.allowances.ListTitleAllowances(ctx, actor.UserID, profileID)
	if err != nil {
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list allowed titles")
	}
	cards, err := resolveItems(h.personal, ctx, actor.Viewer, entries, func(e userstore.TitleAllowance) string { return e.ContentID })
	if err != nil {
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve allowed titles")
	}
	return entries, cards, nil
}

// SetTitleAllowance allows or stops allowing one movie or series for a
// profile. Allowing needs a profile with maturity limits, a title the actor
// may see, and a title in a library the profile can reach. Removing an absent
// allowance succeeds and changes nothing.
func (h *TitleAllowanceHandler) SetTitleAllowance(ctx context.Context, actor AllowanceActor, profileID, itemID string, allowed bool) error {
	store, manager, err := h.manage(ctx, actor)
	if err != nil {
		return err
	}
	profile, err := targetProfile(ctx, store, profileID)
	if err != nil {
		return err
	}
	if allowed {
		if err := h.checkAllowanceFor(ctx, actor, profile, itemID); err != nil {
			return err
		}
	}
	if _, err := h.allowances.SetTitleAllowance(ctx, manager, profileID, itemID, allowed); err != nil {
		return allowanceWriteError(err, "Failed to change the allowed title")
	}
	return nil
}

// checkAllowanceFor requires what allowing a title needs: a profile with
// maturity limits, a movie or series the actor may see, and a title in a
// library the profile can reach.
func (h *TitleAllowanceHandler) checkAllowanceFor(ctx context.Context, actor AllowanceActor, profile *userstore.Profile, itemID string) error {
	if err := requireMaturityLimits(profile); err != nil {
		return err
	}
	if err := h.checkAllowableTitle(ctx, actor, itemID); err != nil {
		return err
	}
	scope, err := h.contentScope(ctx, actor.UserID, profile.ID)
	if err != nil {
		return err
	}
	err = h.personal.itemRepo.EnsureAccessible(ctx, itemID, libraryOnly(scope))
	if errors.Is(err, catalog.ErrItemNotFound) {
		return apiError(http.StatusUnprocessableEntity, "no_library_access", "The profile cannot see the library this title is in")
	}
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to check the profile's library access")
	}
	return nil
}

// checkAllowableTitle requires a movie or series the actor may see.
func (h *TitleAllowanceHandler) checkAllowableTitle(ctx context.Context, actor AllowanceActor, itemID string) error {
	if !auth.ValidTextKey(itemID) {
		return apiError(http.StatusNotFound, "not_found", "Item not found")
	}
	item, err := h.personal.itemRepo.GetByID(ctx, itemID)
	if err != nil || item == nil {
		if h.isEpisodeOrSeason(ctx, itemID) {
			return notATitle()
		}
		return apiError(http.StatusNotFound, "not_found", "Item not found")
	}
	if !userstore.AllowableTitleType(item.Type) {
		return notATitle()
	}
	if err := h.personal.itemRepo.EnsureAccessible(ctx, itemID, actor.Viewer.Access); err != nil {
		if errors.Is(err, catalog.ErrItemNotFound) {
			return apiError(http.StatusNotFound, "not_found", "Item not found")
		}
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to check item access")
	}
	return nil
}

func notATitle() error {
	return apiError(http.StatusUnprocessableEntity, "not_a_title", "Only movies and series can be allowed; allowing a series covers its seasons and episodes")
}

// isEpisodeOrSeason reports whether id names an episode or season, which live
// outside media_items.
func (h *TitleAllowanceHandler) isEpisodeOrSeason(ctx context.Context, id string) bool {
	if repo := h.personal.episodeRepo; repo != nil {
		if ep, err := repo.GetByID(ctx, id); err == nil && ep != nil {
			return true
		}
	}
	if repo := h.personal.seasonRepo; repo != nil {
		if season, err := repo.GetByID(ctx, id); err == nil && season != nil {
			return true
		}
	}
	return false
}

// contentScope resolves what a profile may access, ignoring the libraries it
// hides from its own browsing and its PIN: the household manager is asking on
// its behalf.
func (h *TitleAllowanceHandler) contentScope(ctx context.Context, userID int, profileID string) (access.Scope, error) {
	if h.scopes == nil {
		return access.Scope{}, apiError(http.StatusServiceUnavailable, "unavailable", "Profile access cannot be resolved")
	}
	scope, err := h.scopes.Resolve(ctx, access.ResolveInput{
		UserID: userID, ProfileID: profileID, SkipPINVerification: true, ContentAccessOnly: true,
	})
	if err != nil {
		return access.Scope{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve the profile's access")
	}
	return scope, nil
}

func libraryOnly(scope access.Scope) catalog.AccessFilter {
	return catalog.AccessFilter{AllowedLibraryIDs: scope.AllowedLibraryIDs, DisabledLibraryIDs: scope.DisabledLibraryIDs}
}

// TitleAllowanceStates reports, for a movie or series the actor may see, the
// standing of every other profile on the account: whether it already sees the
// title, is allowed it, or could be.
func (h *TitleAllowanceHandler) TitleAllowanceStates(ctx context.Context, actor AllowanceActor, itemID string) ([]ProfileAllowanceState, error) {
	store, _, err := h.manage(ctx, actor)
	if err != nil {
		return nil, err
	}
	if err := h.checkAllowableTitle(ctx, actor, itemID); err != nil {
		return nil, err
	}
	profiles, err := store.ListProfiles(ctx)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list profiles")
	}
	out := make([]ProfileAllowanceState, 0, len(profiles))
	for _, profile := range profiles {
		if profile.ID == actor.ActiveProfileID {
			continue
		}
		state, err := h.stateFor(ctx, actor.UserID, profile.ID, itemID)
		if err != nil {
			return nil, err
		}
		out = append(out, ProfileAllowanceState{Profile: profile, State: state})
	}
	return out, nil
}

// stateFor is one profile's standing for a title the actor may see.
func (h *TitleAllowanceHandler) stateFor(ctx context.Context, userID int, profileID, itemID string) (string, error) {
	scope, err := h.contentScope(ctx, userID, profileID)
	if err != nil {
		return "", err
	}
	visible := func(filter catalog.AccessFilter) (bool, error) {
		err := h.personal.itemRepo.EnsureAccessible(ctx, itemID, filter)
		if errors.Is(err, catalog.ErrItemNotFound) {
			return false, nil
		}
		if err != nil {
			return false, apiError(http.StatusInternalServerError, "internal_error", "Failed to check the profile's access")
		}
		return true, nil
	}
	filter := libraryOnly(scope)
	if ok, err := visible(filter); err != nil || !ok {
		return AllowanceStateNoLibraryAccess, err
	}
	limits := scope.MaturityLimits
	limits.AllowedTitleIDs = nil
	filter.MaturityLimits = limits
	if ok, err := visible(filter); err != nil || ok {
		return AllowanceStateWithinLimits, err
	}
	own, err := h.allowances.ListTitleAllowances(ctx, userID, profileID)
	if err != nil {
		return "", apiError(http.StatusInternalServerError, "internal_error", "Failed to list allowed titles")
	}
	if slices.ContainsFunc(own, func(a userstore.TitleAllowance) bool { return a.ContentID == itemID }) {
		return AllowanceStateAllowed, nil
	}
	if slices.Contains(scope.AllowedTitleIDs, itemID) {
		return AllowanceStateAllowedByCollection, nil
	}
	return AllowanceStateAboveLimit, nil
}

// ListAllowedCollections lists a profile's collection allowances, newest
// first.
func (h *TitleAllowanceHandler) ListAllowedCollections(ctx context.Context, actor AllowanceActor, profileID string) ([]userstore.CollectionAllowance, error) {
	store, _, err := h.manage(ctx, actor)
	if err != nil {
		return nil, err
	}
	if _, err := targetProfile(ctx, store, profileID); err != nil {
		return nil, err
	}
	out, err := h.allowances.ListCollectionAllowances(ctx, actor.UserID, profileID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list allowed collections")
	}
	return out, nil
}

// SetCollectionAllowance allows or stops allowing a personal collection for a
// profile. Allowing needs a profile with maturity limits and a manual
// collection the acting primary profile created: only then does the
// household manager alone decide what the collection holds. Other collections
// are copied with AllowCollectionTitles instead.
func (h *TitleAllowanceHandler) SetCollectionAllowance(ctx context.Context, actor AllowanceActor, profileID, collectionID string, allowed bool) error {
	store, manager, err := h.manage(ctx, actor)
	if err != nil {
		return err
	}
	profile, err := targetProfile(ctx, store, profileID)
	if err != nil {
		return err
	}
	if allowed {
		if err := checkLiveCollection(ctx, store, actor, profile, collectionID); err != nil {
			return err
		}
	}
	if _, err := h.allowances.SetCollectionAllowance(ctx, manager, profileID, collectionID, allowed); err != nil {
		return allowanceWriteError(err, "Failed to change the allowed collection")
	}
	return nil
}

// checkLiveCollection requires what allowing a collection live needs: a
// profile with maturity limits, a manual collection the acting primary
// profile created.
func checkLiveCollection(ctx context.Context, store userstore.UserStore, actor AllowanceActor, profile *userstore.Profile, collectionID string) error {
	if err := requireMaturityLimits(profile); err != nil {
		return err
	}
	collection, err := visibleCollection(ctx, store, actor, collectionID)
	if err != nil {
		return err
	}
	if collection.CollectionType != userstore.CollectionTypeManual || collection.CreatorProfileID != actor.ActiveProfileID {
		return apiError(http.StatusUnprocessableEntity, "collection_not_live", "Only a manual collection the primary profile created can be allowed as it changes; copy its titles instead")
	}
	return nil
}

// AllowCollectionTitles allows a profile every movie and series a personal
// collection the actor can see holds now, and returns how many were new. Only
// titles the actor may see are copied: a smart collection holds what its query
// matches for the actor, and another collection's stored items are filtered
// by the actor's access.
func (h *TitleAllowanceHandler) AllowCollectionTitles(ctx context.Context, actor AllowanceActor, profileID, collectionID string) (int, error) {
	store, manager, err := h.manage(ctx, actor)
	if err != nil {
		return 0, err
	}
	profile, err := targetProfile(ctx, store, profileID)
	if err != nil {
		return 0, err
	}
	if err := requireMaturityLimits(profile); err != nil {
		return 0, err
	}
	collection, err := visibleCollection(ctx, store, actor, collectionID)
	if err != nil {
		return 0, err
	}
	target, err := h.contentScope(ctx, actor.UserID, profileID)
	if err != nil {
		return 0, err
	}
	filter := copyFilter(actor.Viewer.Access, target)
	var ids []string
	if catalog.IsLiveQueryType(collection.CollectionType) {
		ids, err = h.smartCollectionTitles(ctx, filter, collection)
	} else {
		ids, err = h.storedCollectionTitles(ctx, actor.UserID, filter, profileID, collectionID)
	}
	if err != nil {
		return 0, err
	}
	added, err := h.allowances.AllowTitles(ctx, manager, profileID, ids)
	if err != nil {
		return 0, allowanceWriteError(err, "Failed to allow the collection's titles")
	}
	return added, nil
}

// maxCopiedTitles bounds how many titles one collection copy allows: the most
// a synced list holds (collectionutil.MDBListMaxEntries). A smart collection
// with no item limit would otherwise read the whole matching catalog.
const maxCopiedTitles = 10_000

// Stored collection copies read candidates in pages, and scan at most
// maxScannedCopyEntries of them in one copy.
const (
	copyPageSize          = 1_000
	maxScannedCopyEntries = 100_000
)

// copyFilter is what a collection copy may take: titles the acting profile
// may see (actor) in a library the target profile can reach (target), the
// rule a single allowance applies (no_library_access), so a copy never stores
// an allowance a later library change would turn on without a new grant.
// Applying both before counting keeps titles that fail either from filling a
// copy's budget.
func copyFilter(actor catalog.AccessFilter, target access.Scope) catalog.AccessFilter {
	filter := actor
	switch {
	case actor.AllowedLibraryIDs == nil:
		filter.AllowedLibraryIDs = slices.Clone(target.AllowedLibraryIDs)
	case target.AllowedLibraryIDs != nil:
		filter.AllowedLibraryIDs = slices.DeleteFunc(slices.Clone(actor.AllowedLibraryIDs), func(id int) bool {
			return !slices.Contains(target.AllowedLibraryIDs, id)
		})
	}
	filter.DisabledLibraryIDs = slices.Concat(actor.DisabledLibraryIDs, target.DisabledLibraryIDs)
	return filter
}

// storedCollectionTitles lists, in the collection's order, up to
// maxCopiedTitles of the stored movies and series of a collection that the
// profile is not yet allowed and filter admits. Titles filter turns away are
// never allowed, so they stay candidates on every copy; reading page by page
// past them is what lets a repeated copy reach the rest.
func (h *TitleAllowanceHandler) storedCollectionTitles(ctx context.Context, userID int, filter catalog.AccessFilter, profileID, collectionID string) ([]string, error) {
	var out []string
	var after userstore.CollectionPosition
	for scanned := 0; scanned < maxScannedCopyEntries && len(out) < maxCopiedTitles; {
		page, err := h.allowances.UnallowedCollectionTitles(ctx, userID, profileID, collectionID, after, copyPageSize)
		if err != nil {
			return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list the collection's titles")
		}
		ids := make([]string, len(page))
		for i, p := range page {
			ids[i] = p.MediaItemID
		}
		visible, err := h.personal.itemRepo.EnsureAccessibleIDs(ctx, ids, filter)
		if err != nil {
			return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to check the collection's titles")
		}
		for _, id := range ids {
			if visible[id] && len(out) < maxCopiedTitles {
				out = append(out, id)
			}
		}
		if len(page) < copyPageSize {
			break
		}
		scanned += len(page)
		after = page[len(page)-1]
	}
	return out, nil
}

// smartCollectionTitles lists what a smart collection's query matches under
// filter, up to the collection's item limit and maxCopiedTitles: one copy
// takes the first matches in the query's order.
func (h *TitleAllowanceHandler) smartCollectionTitles(ctx context.Context, filter catalog.AccessFilter, collection *userstore.Collection) ([]string, error) {
	if h.smart == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Catalog queries are unavailable")
	}
	var def catalog.QueryDefinition
	if err := json.Unmarshal([]byte(collection.QueryDefinition), &def); err != nil {
		return nil, apiError(http.StatusUnprocessableEntity, "invalid_query", "The smart collection's query cannot be read")
	}
	def = catalog.ApplySmartCollectionItemLimit(def.Normalize())
	if err := def.ValidateWithOptions(true, true); err != nil {
		return nil, apiError(http.StatusUnprocessableEntity, "invalid_query", err.Error())
	}
	limit := min(*def.Limit, maxCopiedTitles)
	def.Limit = &limit
	items, _, err := h.smart.Preview(ctx, def, filter, limit)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to evaluate the smart collection")
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ContentID)
	}
	return ids, nil
}

// visibleCollection loads a personal collection the acting profile can see.
func visibleCollection(ctx context.Context, store userstore.UserStore, actor AllowanceActor, collectionID string) (*userstore.Collection, error) {
	if !auth.ValidTextKey(collectionID) {
		return nil, apiError(http.StatusNotFound, "not_found", "Collection not found")
	}
	collection, err := store.GetCollection(ctx, collectionID)
	if err != nil || collection == nil {
		return nil, apiError(http.StatusNotFound, "not_found", "Collection not found")
	}
	if actor.ActiveProfileID != "" && !collection.VisibleTo(actor.ActiveProfileID) {
		return nil, apiError(http.StatusNotFound, "not_found", "Collection not found")
	}
	return collection, nil
}
