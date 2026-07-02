package recommendations

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

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

func (e *Engine) watchedItemIDSet(ctx context.Context, userID int, profileID string) (map[string]struct{}, error) {
	return e.signalReader().WatchedItemIDSet(ctx, userID, profileID)
}

func (e *Engine) signalReader() *SignalReader {
	if e.signals != nil {
		return e.signals
	}
	return NewSignalReader(e.repo, e.storeProvider)
}

func (e *Engine) mmrLambda(defaultLambda float64) float64 {
	if e == nil {
		return defaultLambda
	}
	if e.cfg.DiversityLambda >= 0 && e.cfg.DiversityLambda <= 1 {
		return e.cfg.DiversityLambda
	}
	return defaultLambda
}

func (e *Engine) profileAccessFilter(ctx context.Context, userID int, profileID string) catalog.AccessFilter {
	filter := catalog.AccessFilter{UserID: userID, ProfileID: profileID}
	if e == nil || e.storeProvider == nil || profileID == "" {
		return filter
	}

	store, err := e.storeProvider.ForUser(ctx, userID)
	if err != nil || store == nil {
		return filter
	}
	profile, err := store.GetProfile(ctx, profileID)
	if err != nil || profile == nil {
		return filter
	}

	filter.MaxContentRating = profile.MaxContentRating
	if profile.LibraryRestrictionsEnabled {
		filter.AllowedLibraryIDs = append([]int(nil), profile.AllowedLibraryIDs...)
	}
	return filter
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
