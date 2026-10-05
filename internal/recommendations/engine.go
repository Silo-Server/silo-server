package recommendations

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/recommendations/embeddings"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// embedder is the minimal embedding-client seam the Engine depends on. The
// concrete *embeddings.Client satisfies it; tests substitute a fake so the
// backfill loop (EmbedAll) and query-vector path can run without a real
// embedding API.
type embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// ScopeResolver resolves a viewer's effective access scope: the account's
// and its access group's libraries, the profile's restrictions and hidden
// libraries, and its maturity limits. *access.Resolver and
// *policy.ViewerResolver implement it.
type ScopeResolver interface {
	Resolve(ctx context.Context, input access.ResolveInput) (access.Scope, error)
}

// Engine implements the Recommender interface.
type Engine struct {
	repo          *Repo
	ratingsRepo   *catalog.RatingsRepo
	itemRepo      *catalog.ItemRepository
	personRepo    *catalog.PersonRepository
	storeProvider userstore.UserStoreProvider
	signals       *SignalReader
	embClient     embedder
	cfg           config.RecommendationsConfig
	pool          *pgxpool.Pool
	unrated       access.UnratedContentPolicy
	scopes        ScopeResolver
	// refusedEmbeds are the inputs the embedding provider refused; see
	// refusedEmbedInputs.
	refusedEmbeds *refusedEmbedInputs
}

// WithScopeResolver installs the resolver the API resolves request scopes
// with, and returns the engine. Cached rows are then built under the scope a
// read filters them by. Without it the build falls back to the profile's own
// restrictions, missing access groups and hidden libraries.
func (e *Engine) WithScopeResolver(resolver ScopeResolver) *Engine {
	if e != nil {
		e.scopes = resolver
	}
	return e
}

// WithUnratedContentPolicy installs the reader for access.unrated_content and
// returns the engine. Without it every ceiling query hides titles whose rating
// carries no minimum age, which is the default but not necessarily the
// administrator's choice.
func (e *Engine) WithUnratedContentPolicy(policy access.UnratedContentPolicy) *Engine {
	if e != nil {
		e.unrated = policy
	}
	return e
}

// WithUserStoreOutsidePostgres records whether the user store keeps watch
// progress, favorites and watchlist outside Postgres (the SQLite backend), and
// returns the engine. The nightly taste job then finds those profiles through
// the store, and signal checks read the store instead of the Postgres tables.
func (e *Engine) WithUserStoreOutsidePostgres(outside bool) *Engine {
	if e != nil && e.signals != nil {
		e.signals.storeOutsidePostgres = outside
	}
	return e
}

// NewEngine creates a new recommendation Engine.
func NewEngine(
	pool *pgxpool.Pool,
	ratingsRepo *catalog.RatingsRepo,
	itemRepo *catalog.ItemRepository,
	personRepo *catalog.PersonRepository,
	storeProvider userstore.UserStoreProvider,
	cfg config.RecommendationsConfig,
) *Engine {
	repo := NewRepo(pool)

	embCfg := embeddings.ClientConfig{
		BaseURL: cfg.EmbeddingBaseURL,
		Model:   cfg.EmbeddingModel,
		APIKey:  cfg.EmbeddingAuthToken,
	}

	return &Engine{
		repo:          repo,
		ratingsRepo:   ratingsRepo,
		itemRepo:      itemRepo,
		personRepo:    personRepo,
		storeProvider: storeProvider,
		signals:       NewSignalReader(repo, storeProvider),
		embClient:     embeddings.NewClient(embCfg),
		cfg:           cfg,
		pool:          pool,
		refusedEmbeds: newRefusedEmbedInputs(),
	}
}

// ActiveEmbeddingModel returns the embedding model currently locked for this
// installation, or "" when no lock is established.
func (e *Engine) ActiveEmbeddingModel(ctx context.Context) (string, error) {
	lock, err := e.repo.GetEmbeddingLock(ctx)
	if err != nil {
		return "", err
	}
	if lock == nil {
		return "", nil
	}
	return lock.Model, nil
}

// recommendationExclusionSet is SignalReader.RecommendationExclusionSet.
func (e *Engine) recommendationExclusionSet(ctx context.Context, userID int, profileID string) (map[string]struct{}, error) {
	return e.signalReader().RecommendationExclusionSet(ctx, userID, profileID)
}

func (e *Engine) signalReader() *SignalReader {
	if e.signals != nil {
		return e.signals
	}
	return NewSignalReader(e.repo, e.storeProvider)
}

// defaultMMRLambda is the relevance/diversity trade-off used when the
// configured DiversityLambda is outside [0, 1].
const defaultMMRLambda = 0.7

// mmrLambda returns the configured MMR lambda, or defaultMMRLambda when it is
// not a valid weight.
func (e *Engine) mmrLambda() float64 {
	if e == nil {
		return defaultMMRLambda
	}
	if e.cfg.DiversityLambda >= 0 && e.cfg.DiversityLambda <= 1 {
		return e.cfg.DiversityLambda
	}
	return defaultMMRLambda
}

// profileAccessFilter returns the access filter a profile's cached rows are
// built under. With a ScopeResolver it is the filter the API derives from the
// profile's resolved scope (handlers.accessFilterFromScope), PIN verification
// skipped. An error means the scope is unknown and nothing may be cached for
// the profile; access.ErrProfileNotFound means the profile no longer exists.
func (e *Engine) profileAccessFilter(ctx context.Context, userID int, profileID string) (catalog.AccessFilter, error) {
	filter := catalog.AccessFilter{UserID: userID, ProfileID: profileID}
	if e == nil || profileID == "" {
		return filter, nil
	}
	if e.scopes != nil {
		scope, err := e.scopes.Resolve(ctx, access.ResolveInput{
			UserID:              userID,
			ProfileID:           profileID,
			SkipPINVerification: true,
		})
		if err != nil {
			return catalog.AccessFilter{}, fmt.Errorf("resolve access scope for user %d profile %s: %w", userID, profileID, err)
		}
		filter.AllowedLibraryIDs = scope.AllowedLibraryIDs
		filter.DisabledLibraryIDs = scope.DisabledLibraryIDs
		filter.MaturityLimits = scope.MaturityLimits
		return filter, nil
	}
	if e.storeProvider == nil {
		return filter, nil
	}

	store, err := e.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return catalog.AccessFilter{}, fmt.Errorf("open user store for user %d: %w", userID, err)
	}
	if store == nil {
		return catalog.AccessFilter{}, fmt.Errorf("open user store for user %d: no store", userID)
	}
	profile, err := store.GetProfile(ctx, profileID)
	if err != nil {
		return catalog.AccessFilter{}, fmt.Errorf("load profile %s: %w", profileID, err)
	}
	if profile == nil {
		return catalog.AccessFilter{}, access.ErrProfileNotFound
	}

	filter.MaxContentRating = profile.MaxContentRating
	filter.MaxAdvisoryAge = profile.MaxAdvisoryAge
	filter.RequireAdvisoryAge = profile.RequireAdvisoryAge && profile.MaxAdvisoryAge > 0
	if e.unrated != nil {
		// The ceiling is the pair: without this, every recommendations query
		// emits the hide-unrated predicate while the catalog rails beside it
		// show those titles.
		filter.AllowUnratedContent = e.unrated.AllowUnratedContent(ctx)
	}
	if profile.LibraryRestrictionsEnabled {
		filter.AllowedLibraryIDs = append([]int{}, profile.AllowedLibraryIDs...)
	}
	return filter, nil
}

// storeProfiles lists the IDs of an account's profiles from the user store.
func (e *Engine) storeProfiles(ctx context.Context, userID int) ([]string, error) {
	if e.storeProvider == nil {
		return nil, nil
	}
	store, err := e.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("open user store for user %d: %w", userID, err)
	}
	if store == nil {
		return nil, nil
	}
	profiles, err := store.ListProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("list profiles for user %d: %w", userID, err)
	}
	ids := make([]string, 0, len(profiles))
	for _, p := range profiles {
		ids = append(ids, p.ID)
	}
	return ids, nil
}

func scoredItemIDsFromSet(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}

	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	return ids
}
