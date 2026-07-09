package recommendations

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// JobName identifies a recommendation background job.
type JobName string

const (
	JobEmbeddings      JobName = "embeddings"
	JobTasteProfiles   JobName = "taste_profiles"
	JobCowatch         JobName = "cowatch"
	JobRecommendations JobName = "recommendations"
)

// Worker runs scheduled recommendation jobs.
type Worker struct {
	engine                *Engine
	cron                  *cron.Cron
	mu                    sync.Mutex
	running               map[JobName]bool
	profileRefreshCh      chan profileRefreshRequest
	profileRefreshPending map[string]struct{}
	cancelFunc            context.CancelFunc
	embeddingsJobTimeout  time.Duration
}

const tasteProfileRefreshSubjectsQuery = `
	SELECT DISTINCT user_id, profile_id FROM user_ratings
	UNION
	SELECT DISTINCT user_id, profile_id FROM user_taste_profiles
	UNION
	SELECT DISTINCT user_id, profile_id FROM user_watch_progress
	UNION
	SELECT DISTINCT user_id, profile_id FROM ebook_reader_progress
	UNION
	SELECT DISTINCT user_id, profile_id FROM user_favorites
	UNION
	SELECT DISTINCT user_id, profile_id FROM user_watchlist`

// NewWorker creates a new recommendation Worker.
func NewWorker(engine *Engine, embeddingsCron, tasteProfilesCron, cowatchCron, recommendationsCron string, embeddingsJobTimeout time.Duration) (*Worker, error) {
	if embeddingsJobTimeout <= 0 {
		embeddingsJobTimeout = 24 * time.Hour
	}
	w := &Worker{
		engine:                engine,
		cron:                  cron.New(),
		running:               make(map[JobName]bool),
		profileRefreshCh:      make(chan profileRefreshRequest, 256),
		profileRefreshPending: make(map[string]struct{}),
		embeddingsJobTimeout:  embeddingsJobTimeout,
	}

	if _, err := w.cron.AddFunc(embeddingsCron, w.runEmbeddings); err != nil {
		return nil, err
	}
	if _, err := w.cron.AddFunc(tasteProfilesCron, w.runTasteProfiles); err != nil {
		return nil, err
	}
	if _, err := w.cron.AddFunc(cowatchCron, w.runCowatch); err != nil {
		return nil, err
	}
	if _, err := w.cron.AddFunc(recommendationsCron, w.runRecommendations); err != nil {
		return nil, err
	}

	return w, nil
}

// Start begins the scheduled cron jobs and the staleness refresh goroutine.
func (w *Worker) Start() {
	w.cron.Start()

	ctx, cancel := context.WithCancel(context.Background())
	w.cancelFunc = cancel
	go w.profileRefreshLoop(ctx)
	go w.stalenessLoop(ctx)

	slog.Info("recommendation worker started")
}

// Stop halts the scheduled cron jobs and the staleness refresh goroutine.
func (w *Worker) Stop() {
	if w.cancelFunc != nil {
		w.cancelFunc()
	}
	w.cron.Stop()
	slog.Info("recommendation worker stopped")
}

// IsRunning reports whether the named job is currently executing.
func (w *Worker) IsRunning(name JobName) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running[name]
}

// setRunning marks a job as running or not.
func (w *Worker) setRunning(name JobName, v bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.running[name] = v
}

// tryStart attempts to mark a job as running. Returns false if already running.
func (w *Worker) tryStart(name JobName) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running[name] {
		return false
	}
	w.running[name] = true
	return true
}

// TriggerEmbeddings starts an embedding job if one is not already running.
func (w *Worker) TriggerEmbeddings() error {
	if !w.tryStart(JobEmbeddings) {
		return fmt.Errorf("embeddings job is already running")
	}
	go func() {
		defer w.setRunning(JobEmbeddings, false)
		ctx, cancel := context.WithTimeout(context.Background(), w.embeddingsJobTimeout)
		defer cancel()
		slog.Info("starting embedding job (manual trigger)", "timeout", w.embeddingsJobTimeout)
		count, err := w.engine.EmbedAll(ctx)
		if err != nil {
			slog.Error("embedding job failed", "error", err, "embedded", count)
			return
		}
		slog.Info("embedding job completed", "embedded", count)
	}()
	return nil
}

// TriggerTasteProfiles starts a taste profile refresh if one is not already running.
func (w *Worker) TriggerTasteProfiles() error {
	if !w.tryStart(JobTasteProfiles) {
		return fmt.Errorf("taste profiles job is already running")
	}
	go func() {
		defer w.setRunning(JobTasteProfiles, false)
		w.doTasteProfiles()
	}()
	return nil
}

// TriggerCowatch starts a co-watch matrix computation if one is not already running.
func (w *Worker) TriggerCowatch() error {
	if !w.tryStart(JobCowatch) {
		return fmt.Errorf("cowatch job is already running")
	}
	go func() {
		defer w.setRunning(JobCowatch, false)
		w.doCowatch()
	}()
	return nil
}

// TriggerRecommendations starts a recommendation cache refresh if one is not already running.
func (w *Worker) TriggerRecommendations() error {
	if !w.tryStart(JobRecommendations) {
		return fmt.Errorf("recommendations job is already running")
	}
	go func() {
		defer w.setRunning(JobRecommendations, false)
		w.doRecommendations()
	}()
	return nil
}

type profileRefreshRequest struct {
	userID    int
	profileID string
}

// RequestProfileRefresh queues a profile-scoped recommendation refresh without blocking the caller.
func (w *Worker) RequestProfileRefresh(ctx context.Context, userID int, profileID string) {
	if w == nil || w.engine == nil || userID <= 0 || profileID == "" {
		return
	}

	req := profileRefreshRequest{userID: userID, profileID: profileID}
	key := profileRefreshKey(userID, profileID)

	w.mu.Lock()
	if _, exists := w.profileRefreshPending[key]; exists {
		w.mu.Unlock()
		return
	}
	w.profileRefreshPending[key] = struct{}{}
	ch := w.profileRefreshCh
	w.mu.Unlock()

	select {
	case ch <- req:
	case <-ctx.Done():
		w.clearProfileRefreshPending(key)
	case <-time.After(10 * time.Millisecond):
		w.clearProfileRefreshPending(key)
		slog.WarnContext(ctx, "profile refresh queue full; dropping request", "component", "recommendations", "user_id", userID, "profile_id", profileID)
	}
}

// StatusCounts returns counts used by the admin status endpoint.
func (w *Worker) StatusCounts(ctx context.Context) (embedded, totalItems, tasteProfiles, cacheEntries, cowatchPairs int, err error) {
	repo := NewRepo(w.engine.pool)

	embedded, err = repo.EmbeddingCount(ctx)
	if err != nil {
		return
	}
	totalItems, err = repo.TotalMediaItemCount(ctx)
	if err != nil {
		return
	}
	tasteProfiles, err = repo.TasteProfileCount(ctx)
	if err != nil {
		return
	}
	cacheEntries, err = repo.CacheEntryCount(ctx)
	if err != nil {
		return
	}
	cowatchPairs, err = repo.CowatchPairCount(ctx)
	return
}

func (w *Worker) runEmbeddings() {
	if !w.tryStart(JobEmbeddings) {
		slog.Warn("embedding job already running, skipping scheduled run")
		return
	}
	defer w.setRunning(JobEmbeddings, false)

	ctx, cancel := context.WithTimeout(context.Background(), w.embeddingsJobTimeout)
	defer cancel()
	slog.Info("starting embedding job", "timeout", w.embeddingsJobTimeout)
	count, err := w.engine.EmbedAll(ctx)
	if err != nil {
		slog.Error("embedding job failed", "error", err, "embedded", count)
		return
	}
	slog.Info("embedding job completed", "embedded", count)
}

func (w *Worker) runTasteProfiles() {
	if !w.tryStart(JobTasteProfiles) {
		slog.Warn("taste profile job already running, skipping scheduled run")
		return
	}
	defer w.setRunning(JobTasteProfiles, false)
	w.doTasteProfiles()
}

func (w *Worker) runCowatch() {
	if !w.tryStart(JobCowatch) {
		slog.Warn("cowatch job already running, skipping scheduled run")
		return
	}
	defer w.setRunning(JobCowatch, false)
	w.doCowatch()
}

func (w *Worker) runRecommendations() {
	if !w.tryStart(JobRecommendations) {
		slog.Warn("recommendations job already running, skipping scheduled run")
		return
	}
	defer w.setRunning(JobRecommendations, false)
	w.doRecommendations()
}

func (w *Worker) doTasteProfiles() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	slog.Info("starting taste profile refresh")

	rows, err := w.engine.pool.Query(ctx, tasteProfileRefreshSubjectsQuery)
	if err != nil {
		slog.Error("taste profile query failed", "error", err)
		return
	}
	defer rows.Close()

	var refreshed int
	for rows.Next() {
		var userID int
		var profileID string
		if err := rows.Scan(&userID, &profileID); err != nil {
			continue
		}
		if err := w.engine.RefreshTasteProfile(ctx, userID, profileID); err != nil {
			slog.Error("taste profile refresh failed", "user_id", userID, "profile_id", profileID, "error", err)
			continue
		}
		refreshed++
	}
	slog.Info("taste profile refresh completed", "refreshed", refreshed)
}

func (w *Worker) doCowatch() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	slog.Info("starting co-watch matrix computation")

	repo := NewRepo(w.engine.pool)
	watchers, err := repo.GetItemWatchers(ctx, 5, 500)
	if err != nil {
		slog.Error("co-watch: failed to get item watchers", "error", err)
		return
	}

	if len(watchers) == 0 {
		slog.Info("co-watch: no items with enough watchers, skipping")
		return
	}

	pairs := computeCowatchMatrix(watchers, 5, 3, 50)
	if len(pairs) == 0 {
		slog.Info("co-watch: no pairs met threshold")
		return
	}

	// Batch insert in chunks of 1000.
	const batchSize = 1000
	for i := 0; i < len(pairs); i += batchSize {
		end := i + batchSize
		if end > len(pairs) {
			end = len(pairs)
		}
		if err := repo.UpsertCowatchPairs(ctx, pairs[i:end]); err != nil {
			slog.Error("co-watch: failed to upsert pairs", "error", err, "batch_start", i)
			return
		}
	}
	slog.Info("co-watch matrix computation completed", "pairs", len(pairs))
}

func (w *Worker) doRecommendations() {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	slog.Info("starting recommendation cache refresh")

	repo := NewRepo(w.engine.pool)
	cleaned, _ := repo.CleanExpiredCache(ctx)
	if cleaned > 0 {
		slog.Info("cleaned expired cache entries", "count", cleaned)
	}

	expires := time.Now().Add(24 * time.Hour).Format(time.RFC3339)

	// Generate global (non-personalized) cache rows.
	w.cacheGlobalRows(ctx, repo, expires)

	// Generate per-user cached rows.
	profiles, err := repo.GetAllUsersWithTasteProfiles(ctx)
	if err != nil {
		slog.Error("recommendation cache query failed", "error", err)
		return
	}

	var cached int
	for _, p := range profiles {
		// Clean old V1 cache type.
		if err := repo.CleanOldCacheTypes(ctx, p.UserID, p.ProfileID); err != nil {
			slog.Warn("failed to cache recommendations", "error", err)
		}

		cached += w.cacheUserRows(ctx, repo, p.UserID, p.ProfileID, expires)
	}
	slog.Info("recommendation cache refresh completed", "cached_entries", cached)
}

// cacheGlobalRows generates and caches non-personalized rows.
func (w *Worker) cacheGlobalRows(ctx context.Context, repo *Repo, expires string) {
	popular, _ := repo.GetPopularItems(ctx, 30, CacheCandidateLimit)
	if len(popular) > 0 {
		if err := repo.UpsertRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, RecTypePopular, "", popular, expires); err != nil {
			slog.WarnContext(ctx, "failed to cache recommendations", "component", "recommendations", "error", err)
		}
	}

	recentlyAdded, _ := repo.GetRecentlyAddedItems(ctx, 14, CacheCandidateLimit)
	if len(recentlyAdded) > 0 {
		if err := repo.UpsertRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, RecTypeRecentlyAdded, "", recentlyAdded, expires); err != nil {
			slog.WarnContext(ctx, "failed to cache recommendations", "component", "recommendations", "error", err)
		}
	}

	topRated, _ := repo.GetTopRatedItems(ctx, 5, CacheCandidateLimit)
	if len(topRated) > 0 {
		if err := repo.UpsertRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, RecTypeTopRated, "", topRated, expires); err != nil {
			slog.WarnContext(ctx, "failed to cache recommendations", "component", "recommendations", "error", err)
		}
	}

	topGenres, _ := repo.GetTopGenres(ctx, 8)
	for _, genre := range topGenres {
		items, _ := repo.GetGenreSamplerItems(ctx, genre, CacheCandidateLimit)
		if len(items) > 0 {
			if err := repo.UpsertRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, RecTypeGenreSamplerPrefix+genre, "", items, expires); err != nil {
				slog.WarnContext(ctx, "failed to cache recommendations", "component", "recommendations", "error", err)
			}
		}
	}
}

// cacheUserRows generates and caches personalized rows for a single user.
func (w *Worker) cacheUserRows(ctx context.Context, repo *Repo, userID int, profileID, expires string) int {
	var cached int
	watchedSet, err := w.engine.watchedItemIDSet(ctx, userID, profileID)
	if err != nil {
		slog.WarnContext(ctx, "failed to load watched items for recommendation cache", "component", "recommendations", "user_id", userID, "profile_id", profileID, "error", err)
		watchedSet = nil
	}
	watchedIDs := scoredItemIDsFromSet(watchedSet)
	accessFilter := w.engine.profileAccessFilter(ctx, userID, profileID)

	if aggregatedRow, err := w.engine.buildAggregatedRow(ctx, userID, profileID, CacheCandidateLimit, watchedIDs, accessFilter); err == nil && aggregatedRow != nil && len(aggregatedRow.Items) > 0 {
		if err := repo.UpsertRecommendationCache(ctx, userID, profileID, RecTypeForYouMain, "", aggregatedRow.Items, expires); err != nil {
			slog.WarnContext(ctx, "failed to cache recommendations", "component", "recommendations", "error", err)
		} else {
			cached++
		}
	}

	// Cache per-cluster ForYou rows.
	clusterRows, err := w.engine.buildClusterRows(ctx, userID, profileID, CacheCandidateLimit, watchedIDs, accessFilter)
	if err != nil {
		slog.WarnContext(ctx, "failed to build cluster recommendations for cache", "component", "recommendations", "user_id", userID, "profile_id", profileID, "error", err)
	}
	for _, row := range clusterRows {
		if len(row.Items) == 0 {
			continue
		}

		recType := fmt.Sprintf("%s%d", RecTypeForYouClusterPrefix, row.ClusterIndex)
		if err := repo.UpsertRecommendationCache(ctx, userID, profileID, recType, "", row.Items, expires); err != nil {
			slog.WarnContext(ctx, "failed to cache recommendations", "component", "recommendations", "error", err)
		}
		cached++
	}

	// Cache similar users liked.
	items, err := w.engine.SimilarUsersLiked(ctx, userID, profileID, CacheCandidateLimit)
	if err == nil && len(items) > 0 {
		if err := repo.UpsertRecommendationCache(ctx, userID, profileID, RecTypeSimilarUsersLiked, "", items, expires); err != nil {
			slog.WarnContext(ctx, "failed to cache recommendations", "component", "recommendations", "error", err)
		} else {
			cached++
		}
	}

	recentCompleted, err := w.engine.signalReader().RecentCompletedItemIDs(ctx, userID, profileID, 3)
	if err != nil {
		slog.WarnContext(ctx, "failed to load recent completed items for recommendation cache", "component", "recommendations", "user_id", userID, "profile_id", profileID, "error", err)
		return cached
	}
	for _, sourceItemID := range recentCompleted {
		items, err := w.engine.BecauseYouWatched(ctx, userID, profileID, sourceItemID, CacheCandidateLimit)
		if err != nil || len(items) == 0 {
			continue
		}
		if err := repo.UpsertRecommendationCache(ctx, userID, profileID, RecTypeBecauseWatched, sourceItemID, items, expires); err != nil {
			slog.WarnContext(ctx, "failed to cache recommendations", "component", "recommendations", "error", err, "rec_type", RecTypeBecauseWatched, "source_item_id", sourceItemID)
			continue
		}
		cached++
	}

	return cached
}

// stalenessLoop checks for stale taste profiles every 5 minutes and refreshes them.
func (w *Worker) stalenessLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.refreshStaleProfiles(ctx)
		}
	}
}

// refreshStaleProfiles finds profiles marked stale and refreshes them.
func (w *Worker) refreshStaleProfiles(ctx context.Context) {
	repo := NewRepo(w.engine.pool)
	stale, err := repo.GetStaleProfiles(ctx, 50)
	if err != nil {
		slog.ErrorContext(ctx, "staleness check failed", "component", "recommendations", "error", err)
		return
	}
	if len(stale) == 0 {
		return
	}

	slog.InfoContext(ctx, "refreshing stale taste profiles", "component", "recommendations", "count", len(stale))
	for _, p := range stale {
		w.RequestProfileRefresh(ctx, p.UserID, p.ProfileID)
	}
}

// RunEmbeddingsNow triggers an immediate embedding run (for first-run setup).
func (w *Worker) RunEmbeddingsNow() {
	_ = w.TriggerEmbeddings()
}

func (w *Worker) profileRefreshLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-w.profileRefreshCh:
			if err := w.refreshProfile(ctx, req.userID, req.profileID); err != nil {
				slog.ErrorContext(ctx, "profile recommendation refresh failed", "component", "recommendations", "user_id", req.userID, "profile_id", req.profileID, "error", err)
			}
			w.clearProfileRefreshPending(profileRefreshKey(req.userID, req.profileID))
		}
	}
}

func (w *Worker) refreshProfile(ctx context.Context, userID int, profileID string) error {
	refreshCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	if err := w.engine.RefreshTasteProfile(refreshCtx, userID, profileID); err != nil {
		return fmt.Errorf("refresh taste profile: %w", err)
	}

	repo := NewRepo(w.engine.pool)
	expires := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	w.cacheUserRows(refreshCtx, repo, userID, profileID, expires)
	if err := repo.ClearStaleAt(refreshCtx, userID, profileID); err != nil {
		slog.WarnContext(ctx, "failed to clear stale profile marker", "component", "recommendations", "user_id", userID, "profile_id", profileID, "error", err)
	}
	return nil
}

func (w *Worker) clearProfileRefreshPending(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.profileRefreshPending, key)
}

func profileRefreshKey(userID int, profileID string) string {
	return fmt.Sprintf("%d:%s", userID, profileID)
}
