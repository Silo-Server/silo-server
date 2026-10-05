package recommendations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/recommendations/embeddings"
	"github.com/Silo-Server/silo-server/internal/telemetry"
	"github.com/Silo-Server/silo-server/internal/workmetrics"

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

const (
	tasteProfilesJobTimeout = 20 * time.Minute
	cowatchJobTimeout       = 30 * time.Minute
	cacheJobTimeout         = 45 * time.Minute
	globalRowsJobTimeout    = 10 * time.Minute

	staleSweepInterval = 5 * time.Minute
	staleSweepBatch    = 50

	// embedMissingInterval spaces the catch-up passes that embed newly
	// matched items between nightly embedding runs.
	embedMissingInterval = 15 * time.Minute

	// profileRefreshTimeout bounds one profile's refresh.
	profileRefreshTimeout = 2 * time.Minute
	// staleMarkTimeout bounds marking a profile stale after its refresh
	// failed, which may happen after the refresh's own deadline passed.
	staleMarkTimeout = 10 * time.Second
	// readRefreshInterval spaces the refreshes that reads request for one
	// profile on one server. A read cannot tell a row that was never built
	// from one that came out empty, so without it every page load of such a
	// profile would queue another rebuild.
	readRefreshInterval = 15 * time.Minute
)

// profileStaleMarker marks a profile's taste profile stale. *Repo implements
// it.
type profileStaleMarker interface {
	MarkProfileStale(ctx context.Context, userID int, profileID string) error
}

// accountsStaleMarker marks stale every taste profile on some accounts.
// *Repo implements it.
type accountsStaleMarker interface {
	MarkAccountsStale(ctx context.Context, userIDs []int) (int64, error)
}

// Worker runs scheduled recommendation jobs.
type Worker struct {
	engine                *Engine
	cron                  *cron.Cron
	locker                jobLocker
	history               JobHistory
	staleMarker           profileStaleMarker
	accountsMarker        accountsStaleMarker
	mu                    sync.Mutex
	running               map[JobName]bool
	profileRefreshCh      chan profileRefreshRequest
	profileRefreshPending map[string]struct{}
	readRefreshes         refreshThrottle
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

// postgresStoreTasteProfileSubjectsQuery is tasteProfileRefreshSubjectsQuery
// on the Postgres user store, where user_profiles lists every profile. Like
// the purge migration, it leaves out a profile missing from an account that
// lists others, so rows its deletion left behind (ebook reading progress, a
// taste row written while it was being deleted) are not rebuilt every night.
// An account that lists no profile at all is kept.
const postgresStoreTasteProfileSubjectsQuery = `
	SELECT s.user_id, s.profile_id
	FROM   (` + tasteProfileRefreshSubjectsQuery + `) s
	WHERE  EXISTS (SELECT 1 FROM user_profiles p WHERE p.user_id = s.user_id AND p.id = s.profile_id)
	   OR  NOT EXISTS (SELECT 1 FROM user_profiles p WHERE p.user_id = s.user_id)`

// NewWorker creates a new recommendation Worker.
func NewWorker(engine *Engine, embeddingsCron, tasteProfilesCron, cowatchCron, recommendationsCron string, embeddingsJobTimeout time.Duration) (*Worker, error) {
	if embeddingsJobTimeout <= 0 {
		embeddingsJobTimeout = 24 * time.Hour
	}
	w := &Worker{
		engine:                engine,
		cron:                  cron.New(),
		locker:                pgJobLocker{pool: engine.pool},
		staleMarker:           engine.repo,
		accountsMarker:        engine.repo,
		running:               make(map[JobName]bool),
		profileRefreshCh:      make(chan profileRefreshRequest, 256),
		profileRefreshPending: make(map[string]struct{}),
		embeddingsJobTimeout:  embeddingsJobTimeout,
	}

	for _, schedule := range []struct {
		spec string
		name JobName
	}{
		{embeddingsCron, JobEmbeddings},
		{tasteProfilesCron, JobTasteProfiles},
		{cowatchCron, JobCowatch},
		{recommendationsCron, JobRecommendations},
	} {
		if _, err := w.cron.AddFunc(schedule.spec, func() { w.runScheduled(schedule.name) }); err != nil {
			return nil, err
		}
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
	go w.embedMissingLoop(ctx)
	go w.ensureGlobalRows(ctx, w.engine.repo)

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

// jobFor returns the scheduled body of the named job.
func (w *Worker) jobFor(name JobName) job {
	switch name {
	case JobEmbeddings:
		return job{name: name, timeout: w.embeddingsJobTimeout, run: func(ctx context.Context) (jobResult, error) { return w.doEmbeddings(ctx) }}
	case JobTasteProfiles:
		return job{name: name, timeout: tasteProfilesJobTimeout, run: func(ctx context.Context) (jobResult, error) { return w.doTasteProfiles(ctx) }}
	case JobCowatch:
		return job{name: name, timeout: cowatchJobTimeout, run: func(ctx context.Context) (jobResult, error) { return w.doCowatch(ctx) }}
	default:
		return job{name: JobRecommendations, timeout: cacheJobTimeout, run: func(ctx context.Context) (jobResult, error) { return w.doRecommendations(ctx) }}
	}
}

// TriggerEmbeddings starts an embedding job unless one is already running on
// this server or another.
func (w *Worker) TriggerEmbeddings() error {
	return w.runJob(w.jobFor(JobEmbeddings), true)
}

// TriggerTasteProfiles starts a taste profile refresh unless one is already
// running on this server or another.
func (w *Worker) TriggerTasteProfiles() error {
	return w.runJob(w.jobFor(JobTasteProfiles), true)
}

// TriggerCowatch starts a co-watch matrix computation unless one is already
// running on this server or another.
func (w *Worker) TriggerCowatch() error {
	return w.runJob(w.jobFor(JobCowatch), true)
}

// TriggerRecommendations starts a recommendation cache refresh unless one is
// already running on this server or another.
func (w *Worker) TriggerRecommendations() error {
	return w.runJob(w.jobFor(JobRecommendations), true)
}

// embedMissingJob is the catch-up pass: Pass 1 of the embeddings job only. It
// takes the embeddings job's claim, so it never overlaps an embedding run on
// any server, and a run that found nothing to do is not recorded.
func (w *Worker) embedMissingJob() job {
	return job{
		name:          JobEmbeddings,
		timeout:       w.embeddingsJobTimeout,
		quietWhenIdle: true,
		run:           func(ctx context.Context) (jobResult, error) { return w.doEmbedMissing(ctx) },
	}
}

// EmbedMissingNow starts the catch-up pass in the background unless an
// embedding run holds the job here or on another server. Startup calls it
// when items need embeddings, so an interrupted backfill resumes at once.
func (w *Worker) EmbedMissingNow() {
	if err := w.runJob(w.embedMissingJob(), true); err != nil {
		slog.Info("embedding catch-up not started", "component", "recommendations", "reason", err)
	}
}

// embedMissingLoop runs the catch-up pass every embedMissingInterval.
func (w *Worker) embedMissingLoop(ctx context.Context) {
	ticker := time.NewTicker(embedMissingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runEmbedMissing()
		}
	}
}

// runEmbedMissing runs the catch-up pass in the caller's goroutine, skipping
// it while an embedding run holds the job here or on another server.
func (w *Worker) runEmbedMissing() {
	err := w.runJob(w.embedMissingJob(), false)
	switch {
	case err == nil:
	case errors.Is(err, ErrJobRunning), errors.Is(err, ErrJobRunningElsewhere):
		slog.Debug("embedding job is running; skipping the catch-up pass", "component", "recommendations", "reason", err)
	default:
		slog.Warn("embedding catch-up pass could not start", "component", "recommendations", "error", err)
	}
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
	if !w.claimProfileRefresh(key) {
		return
	}

	select {
	case w.profileRefreshCh <- req:
	case <-ctx.Done():
		w.clearProfileRefreshPending(key)
	case <-time.After(10 * time.Millisecond):
		w.clearProfileRefreshPending(key)
		slog.WarnContext(ctx, "profile refresh queue full; dropping request", "component", "recommendations", "user_id", userID, "profile_id", profileID)
	}
}

// NotifySignalsChanged records that a profile's recommendation signals
// changed: a rating, favorite, watchlist entry, watch progress or history. It
// marks the taste profile stale, which the stale sweep on any server picks up,
// then queues a refresh on this server. Call it once per change event or
// import run, not once per record. It does nothing on a nil Worker, which is
// what callers hold when recommendations are disabled.
func (w *Worker) NotifySignalsChanged(ctx context.Context, userID int, profileID string) {
	if w == nil || userID <= 0 || profileID == "" {
		return
	}
	// The change is already committed, so the request that made it ending
	// must not cancel recording it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), staleMarkTimeout)
	defer cancel()
	w.markProfileStale(ctx, userID, profileID)
	w.RequestProfileRefresh(ctx, userID, profileID)
}

// NotifyAccountsScopeChanged records that the access scope of every profile
// on the accounts changed: an account's libraries or access group, or its
// access group's libraries. Their cached rows were built under the old scope,
// so their taste profiles are marked stale in one statement and the stale
// sweep rebuilds them. Nothing is refreshed here: one access group can hold
// many accounts.
func (w *Worker) NotifyAccountsScopeChanged(ctx context.Context, userIDs []int) {
	if w == nil || w.accountsMarker == nil || len(userIDs) == 0 {
		return
	}
	// The change is already committed; see NotifySignalsChanged.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), staleMarkTimeout)
	defer cancel()
	marked, err := w.accountsMarker.MarkAccountsStale(ctx, userIDs)
	if err != nil {
		slog.WarnContext(ctx, "marking accounts' taste profiles stale after an access change failed", "component", "recommendations", "accounts", len(userIDs), "error", err)
		return
	}
	slog.InfoContext(ctx, "access change: taste profiles marked stale", "component", "recommendations", "accounts", len(userIDs), "profiles", marked)
}

// RequestReadRefresh queues a refresh for a profile whose cached rows a read
// found missing, at most once per readRefreshInterval per profile on this
// server. Signal changes go through NotifySignalsChanged, which is not
// throttled.
// ReadRefreshDue reports whether RequestReadRefresh would queue a refresh for
// the profile now, so a read can skip checking whether one is worth asking
// for.
func (w *Worker) ReadRefreshDue(userID int, profileID string) bool {
	if w == nil || w.engine == nil || userID <= 0 || profileID == "" {
		return false
	}
	return w.readRefreshes.due(profileRefreshKey(userID, profileID), readRefreshInterval)
}

func (w *Worker) RequestReadRefresh(ctx context.Context, userID int, profileID string) {
	if w == nil || w.engine == nil || userID <= 0 || profileID == "" {
		return
	}
	if !w.readRefreshes.allow(profileRefreshKey(userID, profileID), readRefreshInterval) {
		return
	}
	w.RequestProfileRefresh(ctx, userID, profileID)
}

func (w *Worker) markProfileStale(ctx context.Context, userID int, profileID string) {
	if w.staleMarker == nil {
		return
	}
	if err := w.staleMarker.MarkProfileStale(ctx, userID, profileID); err != nil {
		slog.WarnContext(ctx, "marking taste profile stale failed", "component", "recommendations", "user_id", userID, "profile_id", profileID, "error", err)
	}
}

// StatusCounts returns counts used by the admin status endpoint.
func (w *Worker) StatusCounts(ctx context.Context) (embedded, totalItems, tasteProfiles, cacheEntries, cowatchPairs int, err error) {
	repo := w.engine.repo

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

type embeddingsResult struct {
	// MissingOnly marks a catch-up pass, which skips the text-staleness
	// pass.
	MissingOnly bool `json:"missing_only,omitempty"`
	EmbedCounts
	// QuotaLimited marks a run a provider limit stopped; RetryDeferred
	// marks one stopped because the provider asked to wait longer than a
	// run waits.
	QuotaLimited  bool `json:"quota_limited"`
	RetryDeferred bool `json:"retry_deferred"`
}

func (r embeddingsResult) failures() int { return r.Failed + r.Skipped }

func (r embeddingsResult) idle() bool { return r.Embedded == 0 && r.failures() == 0 }

func newEmbeddingsResult(counts EmbedCounts, err error, missingOnly bool) embeddingsResult {
	res := embeddingsResult{MissingOnly: missingOnly, EmbedCounts: counts}
	if err != nil && isQuotaError(err) {
		res.QuotaLimited = true
		var limitErr *embeddings.RateLimitError
		res.RetryDeferred = errors.As(err, &limitErr) && limitErr.RetryDeferred
	}
	return res
}

func (w *Worker) doEmbeddings(ctx context.Context) (embeddingsResult, error) {
	counts, err := w.engine.EmbedAll(ctx)
	return newEmbeddingsResult(counts, err, false), err
}

func (w *Worker) doEmbedMissing(ctx context.Context) (embeddingsResult, error) {
	counts, err := w.engine.EmbedMissing(ctx)
	return newEmbeddingsResult(counts, err, true), err
}

type tasteProfilesResult struct {
	Profiles  int `json:"profiles"`
	Refreshed int `json:"refreshed"`
	// NoOp counts profiles whose refresh stored no taste vector: no positive
	// signal, none of their positively signaled titles has an embedding yet,
	// or the profile was deleted after the job listed it. Their previous
	// vector, clusters and personal rows are cleared.
	NoOp      int `json:"no_op"`
	Failed    int `json:"failed"`
	Remaining int `json:"remaining"`
}

func (r tasteProfilesResult) failures() int { return r.Failed }

func (w *Worker) doTasteProfiles(ctx context.Context) (tasteProfilesResult, error) {
	var res tasteProfilesResult
	subjects, failed, err := w.tasteProfileSubjects(ctx)
	res.Failed += failed
	if err != nil {
		return res, err
	}

	res.Profiles = len(subjects)
	for i, s := range subjects {
		if err := ctx.Err(); err != nil {
			res.Remaining = len(subjects) - i
			slog.WarnContext(ctx, "taste profile job ran out of time", "component", "recommendations", "processed", i, "remaining", res.Remaining)
			return res, fmt.Errorf("taste profile refresh stopped after %d of %d profiles: %w", i, len(subjects), err)
		}
		written, err := w.engine.refreshTasteProfile(ctx, s.UserID, s.ProfileID)
		switch {
		case err != nil:
			res.Failed++
			slog.WarnContext(ctx, "taste profile refresh failed", "component", "recommendations", "user_id", s.UserID, "profile_id", s.ProfileID, "error", err)
		case written:
			res.Refreshed++
		default:
			res.NoOp++
		}
	}
	if res.Refreshed == 0 && res.NoOp > 0 {
		slog.WarnContext(ctx, "taste profile job stored no taste vectors; their titles may have no embeddings yet", "component", "recommendations", "profiles", res.Profiles, "no_op", res.NoOp)
	}
	return res, nil
}

// tasteProfileSubjects lists the profiles the taste job refreshes: those with
// a taste profile or any signal. failed counts subjects that could not be
// read.
func (w *Worker) tasteProfileSubjects(ctx context.Context) (subjects []StaleProfile, failed int, err error) {
	signals := w.engine.signalReader()
	query := postgresStoreTasteProfileSubjectsQuery
	if signals.storeIsSeparate() {
		query = tasteProfileRefreshSubjectsQuery
	}
	// Read every subject before refreshing, so the query's connection is not
	// held for the whole job.
	rows, err := w.engine.pool.Query(ctx, query)
	if err != nil {
		return nil, 0, fmt.Errorf("list taste profile subjects: %w", err)
	}
	seen := make(map[StaleProfile]struct{})
	for rows.Next() {
		var s StaleProfile
		if err := rows.Scan(&s.UserID, &s.ProfileID); err != nil {
			failed++
			slog.WarnContext(ctx, "reading a taste profile subject failed", "component", "recommendations", "error", err)
			continue
		}
		seen[s] = struct{}{}
		subjects = append(subjects, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, failed, fmt.Errorf("list taste profile subjects: %w", err)
	}
	if !signals.storeIsSeparate() {
		return subjects, failed, nil
	}

	// The query above sees ratings and taste profiles, but a user store
	// outside Postgres holds the progress, favorites and watchlist. Ask it
	// for every account's profiles that have signals.
	userIDs, err := w.engine.repo.ListUserIDs(ctx)
	if err != nil {
		return nil, failed, fmt.Errorf("list taste profile subjects: %w", err)
	}
	for _, userID := range userIDs {
		if err := ctx.Err(); err != nil {
			return nil, failed, fmt.Errorf("list taste profile subjects: %w", err)
		}
		profiles, err := w.engine.storeProfiles(ctx, userID)
		if err != nil {
			failed++
			slog.WarnContext(ctx, "listing an account's profiles for the taste job failed", "component", "recommendations", "user_id", userID, "error", err)
			continue
		}
		for _, profileID := range profiles {
			s := StaleProfile{UserID: userID, ProfileID: profileID}
			if _, ok := seen[s]; ok {
				continue
			}
			has, err := signals.HasSignals(ctx, userID, profileID)
			if err != nil {
				failed++
				slog.WarnContext(ctx, "checking a profile's signals for the taste job failed", "component", "recommendations", "user_id", userID, "profile_id", profileID, "error", err)
				continue
			}
			if has {
				seen[s] = struct{}{}
				subjects = append(subjects, s)
			}
		}
	}
	return subjects, failed, nil
}

type cowatchResult struct {
	Items  int   `json:"items"`
	Pairs  int   `json:"pairs"`
	Pruned int64 `json:"pruned"`
}

func (cowatchResult) failures() int { return 0 }

// doCowatch rebuilds item_cowatch from current watch history. Once every
// batch is stored it deletes the pairs this run did not write: pairs of items
// deleted since the last run, pairs that fell below the shared-watcher floor,
// and pairs pushed out of an item's top neighbors. The table then holds only
// this run's pairs.
func (w *Worker) doCowatch(ctx context.Context) (cowatchResult, error) {
	var res cowatchResult
	repo := w.engine.repo
	// The database clock stamps updated_at, so the cutoff comes from it too.
	runStart, err := repo.Now(ctx)
	if err != nil {
		return res, fmt.Errorf("read run start: %w", err)
	}
	watchers, err := repo.GetItemWatchers(ctx, DefaultMinWatchers, DefaultMaxWatchesPerUser)
	if err != nil {
		return res, fmt.Errorf("get item watchers: %w", err)
	}
	res.Items = len(watchers)

	pairs, err := computeCowatchMatrix(ctx, watchers, DefaultMinWatchers, DefaultMinShared, DefaultTopN)
	if err != nil {
		return res, fmt.Errorf("compute co-watch matrix: %w", err)
	}

	// Batch insert in chunks of 1000.
	const batchSize = 1000
	for i := 0; i < len(pairs); i += batchSize {
		end := min(i+batchSize, len(pairs))
		if err := repo.UpsertCowatchPairs(ctx, pairs[i:end]); err != nil {
			return res, fmt.Errorf("upsert co-watch pairs from %d: %w", i, err)
		}
		res.Pairs = end
	}

	res.Pruned, err = repo.DeleteCowatchPairsBefore(ctx, runStart)
	if err != nil {
		return res, fmt.Errorf("delete stale co-watch pairs: %w", err)
	}
	return res, nil
}

type cacheResult struct {
	// GlobalOnly marks the startup run that builds only the global rows.
	GlobalOnly     bool  `json:"global_only,omitempty"`
	Cleaned        int64 `json:"cleaned"`
	CleanFailed    bool  `json:"clean_failed,omitempty"`
	GlobalRows     int   `json:"global_rows"`
	GlobalFailures int   `json:"global_failures"`
	Profiles       int   `json:"profiles"`
	Processed      int   `json:"processed"`
	Remaining      int   `json:"remaining"`
	CachedRows     int   `json:"cached_rows"`
	FailedProfiles int   `json:"failed_profiles"`
	BuildErrors    int   `json:"build_errors"`
}

func (r cacheResult) failures() int {
	n := r.GlobalFailures + r.BuildErrors
	if r.CleanFailed {
		n++
	}
	return n
}

func (w *Worker) doRecommendations(ctx context.Context) (cacheResult, error) {
	var res cacheResult
	expires := cacheExpiry(time.Now())
	repo := w.engine.repo

	cleaned, err := repo.CleanExpiredCache(ctx)
	if err != nil {
		res.CleanFailed = true
		slog.WarnContext(ctx, "cleaning expired recommendation cache failed", "component", "recommendations", "error", err)
	}
	res.Cleaned = cleaned

	res.GlobalRows, res.GlobalFailures = w.cacheGlobalRows(ctx, repo, expires)

	// Least recently cached first, so a run that runs out of time leaves the
	// profiles refreshed most recently for last.
	profiles, err := repo.ListCacheRefreshCandidates(ctx)
	if err != nil {
		return res, fmt.Errorf("list profiles to cache: %w", err)
	}
	res.Profiles = len(profiles)

	for i, p := range profiles {
		if err := ctx.Err(); err != nil {
			res.Remaining = len(profiles) - i
			slog.WarnContext(ctx, "recommendation cache job ran out of time", "component", "recommendations", "processed", i, "remaining", res.Remaining)
			return res, fmt.Errorf("cache refresh stopped after %d of %d profiles: %w", i, len(profiles), err)
		}
		built := w.cacheUserRows(ctx, repo, p.UserID, p.ProfileID, expires)
		res.Processed++
		res.CachedRows += built.cached
		res.BuildErrors += built.failed
		if built.failed > 0 {
			res.FailedProfiles++
		}
	}
	return res, nil
}

// globalRowStore is the part of Repo that builds and caches the global rows.
type globalRowStore interface {
	GetPopularItems(ctx context.Context, days, limit int) ([]ScoredItem, error)
	GetRecentlyAddedItems(ctx context.Context, days, limit int) ([]ScoredItem, error)
	GetTopRatedItems(ctx context.Context, minRatings, limit int) ([]ScoredItem, error)
	GetTopGenres(ctx context.Context, limit int) ([]string, error)
	GetGenreSamplerItems(ctx context.Context, genre string, limit int) ([]ScoredItem, error)
	UpsertRecommendationCache(ctx context.Context, userID int, profileID, recType, sourceItemID string, items []ScoredItem, expiresAt string) error
	ExtendGlobalRecommendationCache(ctx context.Context, recType string, prefix bool, expiresAt string) (int64, error)
	DeleteGlobalRecommendationCache(ctx context.Context, recType string, prefix bool, keep []string) (int64, error)
}

// cacheGlobalRows generates and caches non-personalized rows. A row whose
// query or write fails keeps its previously cached version until the new
// rows' expiry, so one failed run does not empty it. A row whose query finds
// nothing, and a genre row whose genre left the menu, is deleted.
func (w *Worker) cacheGlobalRows(ctx context.Context, store globalRowStore, expires string) (written, failed int) {
	// keep extends the cached rows of recType (every type starting with it
	// when prefix is set) after their rebuild failed with cause.
	keep := func(recType string, prefix bool, cause error) {
		failed++
		slog.WarnContext(ctx, "building global recommendation row failed; keeping the cached row", "component", "recommendations", "rec_type", recType, "prefix", prefix, "error", cause)
		if _, err := store.ExtendGlobalRecommendationCache(ctx, recType, prefix, expires); err != nil {
			slog.WarnContext(ctx, "extending the cached global recommendation row failed", "component", "recommendations", "rec_type", recType, "prefix", prefix, "error", err)
		}
	}
	// drop deletes the cached rows of recType (every type starting with it
	// when prefix is set, except keep) when the rebuild found nothing to
	// show, so an empty row does not keep serving its old items.
	drop := func(recType string, prefix bool, keep []string) {
		if _, err := store.DeleteGlobalRecommendationCache(ctx, recType, prefix, keep); err != nil {
			failed++
			slog.WarnContext(ctx, "deleting an empty global recommendation row failed", "component", "recommendations", "rec_type", recType, "prefix", prefix, "error", err)
		}
	}
	put := func(recType string, items []ScoredItem, buildErr error) {
		switch {
		case buildErr != nil:
			keep(recType, false, buildErr)
		case len(items) == 0:
			drop(recType, false, nil)
		default:
			if err := store.UpsertRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, recType, "", items, expires); err != nil {
				keep(recType, false, err)
				return
			}
			written++
		}
	}

	popular, err := store.GetPopularItems(ctx, 30, CacheCandidateLimit)
	put(RecTypePopular, popular, err)
	recentlyAdded, err := store.GetRecentlyAddedItems(ctx, 14, CacheCandidateLimit)
	put(RecTypeRecentlyAdded, recentlyAdded, err)
	topRated, err := store.GetTopRatedItems(ctx, 5, CacheCandidateLimit)
	put(RecTypeTopRated, topRated, err)

	topGenres, err := store.GetTopGenres(ctx, 8)
	if err != nil {
		keep(RecTypeGenreSamplerPrefix, true, err)
		return written, failed
	}
	menu := make([]string, 0, len(topGenres))
	for _, genre := range topGenres {
		items, err := store.GetGenreSamplerItems(ctx, genre, CacheCandidateLimit)
		put(RecTypeGenreSamplerPrefix+genre, items, err)
		menu = append(menu, RecTypeGenreSamplerPrefix+genre)
	}
	// A genre that left the menu would otherwise keep serving its row.
	drop(RecTypeGenreSamplerPrefix, true, menu)
	return written, failed
}

// globalRowCache is a globalRowStore that can tell whether any global row is
// cached.
type globalRowCache interface {
	globalRowStore
	HasGlobalRecommendationCache(ctx context.Context) (bool, error)
}

// ensureGlobalRows builds the global rows when none is cached, so cold-start
// rows exist before the first nightly cache job. It runs under the cache job's
// claim, so it never overlaps that job on any server.
func (w *Worker) ensureGlobalRows(ctx context.Context, store globalRowCache) {
	cached, err := store.HasGlobalRecommendationCache(ctx)
	if err != nil {
		slog.WarnContext(ctx, "checking for global recommendation rows failed", "component", "recommendations", "error", err)
		return
	}
	if cached {
		return
	}
	slog.InfoContext(ctx, "no global recommendation rows cached; building them now", "component", "recommendations")
	err = w.runJob(job{name: JobRecommendations, timeout: globalRowsJobTimeout, run: func(ctx context.Context) (jobResult, error) {
		res := cacheResult{GlobalOnly: true}
		res.GlobalRows, res.GlobalFailures = w.cacheGlobalRows(ctx, store, cacheExpiry(time.Now()))
		return res, nil
	}}, false)
	if err != nil {
		slog.InfoContext(ctx, "global recommendation rows not built at startup", "component", "recommendations", "reason", err)
	}
}

// userRowsResult counts one profile's cache build.
type userRowsResult struct {
	cached int
	// failed counts the build steps and cache writes that failed.
	failed int
}

// cacheUserRows generates and caches personalized rows for a single user. A
// main, cluster or Because You Watched row whose rebuild finds nothing is
// deleted; a row whose build or write fails keeps its cached version.
func (w *Worker) cacheUserRows(ctx context.Context, repo *Repo, userID int, profileID, expires string) userRowsResult {
	var res userRowsResult
	fail := func(step string, err error, attrs ...any) {
		res.failed++
		slog.WarnContext(ctx, "recommendation cache build step failed", append([]any{
			"component", "recommendations", "step", step, "user_id", userID, "profile_id", profileID, "error", err,
		}, attrs...)...)
	}
	put := func(recType, sourceItemID string, items []ScoredItem) {
		if err := repo.UpsertRecommendationCache(ctx, userID, profileID, recType, sourceItemID, items, expires); err != nil {
			fail("write_cache", err, "rec_type", recType, "source_item_id", sourceItemID)
			return
		}
		res.cached++
	}
	// putOrDrop caches a rebuilt row, or deletes the cached one when the
	// rebuild found nothing, so it does not keep serving its old items until
	// it expires. A row whose build failed never gets here.
	putOrDrop := func(recType, sourceItemID string, items []ScoredItem) {
		if len(items) > 0 {
			put(recType, sourceItemID, items)
			return
		}
		if err := repo.DeleteProfileRecommendationCache(ctx, userID, profileID, recType, sourceItemID); err != nil {
			fail("delete_cache", err, "rec_type", recType, "source_item_id", sourceItemID)
		}
	}

	// Rows are built under the scope the profile's reads are filtered by. A
	// read only removes titles, so rows built under a wider scope would come
	// back thin; a profile whose scope cannot be resolved keeps its cached
	// rows, and one that no longer exists gets none.
	accessFilter, err := w.engine.profileAccessFilter(ctx, userID, profileID)
	if errors.Is(err, access.ErrProfileNotFound) {
		slog.InfoContext(ctx, "profile no longer exists; recommendation rows not built", "component", "recommendations", "user_id", userID, "profile_id", profileID)
		return res
	}
	if err != nil {
		fail("access_scope", err)
		return res
	}

	// Watched and favorited titles (taste-seed picks are favorites) never
	// enter any row's candidates. Rows built without the set would carry
	// them, so a profile whose set cannot be read keeps its cached rows.
	excluded, err := w.engine.recommendationExclusionSet(ctx, userID, profileID)
	if err != nil {
		fail("excluded_items", err)
		return res
	}
	excludeIDs := scoredItemIDsFromSet(excluded)

	aggregatedRow, err := w.engine.buildAggregatedRow(ctx, userID, profileID, CacheCandidateLimit, excludeIDs, accessFilter)
	if err != nil {
		fail("main_row", err)
	} else {
		var items []ScoredItem
		if aggregatedRow != nil {
			items = aggregatedRow.Items
		}
		putOrDrop(RecTypeForYouMain, "", items)
	}

	// Cache per-cluster ForYou rows. buildClusterRows logs each cluster whose
	// candidate query failed and returns no row for it, so its cached row
	// stays.
	clusterRows, failedClusters, err := w.engine.buildClusterRows(ctx, userID, profileID, CacheCandidateLimit, excludeIDs, accessFilter)
	res.failed += failedClusters
	if err != nil {
		fail("cluster_rows", err)
	}
	for _, row := range clusterRows {
		putOrDrop(fmt.Sprintf("%s%d", RecTypeForYouClusterPrefix, row.ClusterIndex), "", row.Items)
	}

	// An empty Similar Users row is cached too: it records that the row was
	// built and came out empty (below the account floors), so reads do not
	// keep asking for a rebuild that cannot fill it.
	items, err := w.engine.similarUsersLiked(ctx, userID, profileID, CacheCandidateLimit, excluded, accessFilter)
	if err != nil {
		fail("similar_users", err)
	} else {
		if items == nil {
			items = []ScoredItem{}
		}
		put(RecTypeSimilarUsersLiked, "", items)
	}

	recentCompleted, err := w.engine.signalReader().RecentCompletedItemIDs(ctx, userID, profileID, 3)
	if err != nil {
		fail("recent_completed", err)
		return res
	}
	for _, sourceItemID := range recentCompleted {
		items, err := w.engine.becauseYouWatched(ctx, sourceItemID, CacheCandidateLimit, excluded, accessFilter)
		if err != nil {
			fail("because_you_watched", err, "source_item_id", sourceItemID)
			continue
		}
		putOrDrop(RecTypeBecauseWatched, sourceItemID, items)
	}

	return res
}

// stalenessLoop refreshes stale taste profiles every few minutes.
func (w *Worker) stalenessLoop(ctx context.Context) {
	ticker := time.NewTicker(staleSweepInterval)
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

// refreshStaleProfiles refreshes the profiles marked stale. One server sweeps
// at a time: the sweep holds its cluster lock until its refreshes finish, so a
// server whose tick lands meanwhile skips instead of rebuilding the same
// profiles.
func (w *Worker) refreshStaleProfiles(ctx context.Context) {
	lockCtx, cancel := context.WithTimeout(ctx, jobLockTimeout)
	unlock, acquired, err := w.locker.TryLock(lockCtx, staleSweepLock)
	cancel()
	if err != nil {
		slog.WarnContext(ctx, "stale profile sweep could not take its lock", "component", "recommendations", "error", err)
		return
	}
	if !acquired {
		return
	}
	defer unlock()

	stale, err := w.engine.repo.GetStaleProfiles(ctx, staleSweepBatch)
	if err != nil {
		slog.ErrorContext(ctx, "staleness check failed", "component", "recommendations", "error", err)
		return
	}
	if len(stale) == 0 {
		return
	}

	slog.InfoContext(ctx, "refreshing stale taste profiles", "component", "recommendations", "count", len(stale))
	for _, p := range stale {
		if ctx.Err() != nil {
			return
		}
		key := profileRefreshKey(p.UserID, p.ProfileID)
		if !w.claimProfileRefresh(key) {
			continue // already queued or refreshing on this server
		}
		w.runProfileRefresh(ctx, p.UserID, p.ProfileID)
	}
}

func (w *Worker) profileRefreshLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-w.profileRefreshCh:
			w.runProfileRefresh(ctx, req.userID, req.profileID)
		}
	}
}

// runProfileRefresh refreshes a profile whose pending key the caller claimed,
// then releases the key.
func (w *Worker) runProfileRefresh(ctx context.Context, userID int, profileID string) {
	defer w.clearProfileRefreshPending(profileRefreshKey(userID, profileID))
	if err := w.refreshProfile(ctx, userID, profileID); err != nil {
		slog.ErrorContext(ctx, "profile recommendation refresh failed", "component", "recommendations", "user_id", userID, "profile_id", profileID, "error", err)
	}
}

// refreshProfile rebuilds one profile's taste profile and cached rows. When
// every step succeeds it clears the stale marks set before the refresh
// started; a mark set while it ran stays for the stale sweep. When a step
// fails it marks the profile stale again, so the sweep retries it.
func (w *Worker) refreshProfile(ctx context.Context, userID int, profileID string) (runErr error) {
	ctx, observation := workmetrics.Start(ctx, "recommendations", time.Time{})
	defer workmetrics.Profile(ctx)()
	defer func() { observation.Finish(telemetry.Outcome(runErr)) }()
	defer func() {
		if runErr == nil {
			return
		}
		// The rebuild may have stored the taste profile, consuming the mark
		// that queued this refresh, before a later step failed.
		markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), staleMarkTimeout)
		defer cancel()
		w.markProfileStale(markCtx, userID, profileID)
	}()
	refreshCtx, cancel := context.WithTimeout(ctx, profileRefreshTimeout)
	defer cancel()

	repo := w.engine.repo
	started, err := repo.Now(refreshCtx)
	if err != nil {
		return err
	}
	if _, err := w.engine.rebuildTasteProfile(refreshCtx, userID, profileID, started); err != nil {
		return fmt.Errorf("refresh taste profile: %w", err)
	}
	if built := w.cacheUserRows(refreshCtx, repo, userID, profileID, cacheExpiry(started)); built.failed > 0 {
		return fmt.Errorf("rebuild recommendation cache: %d steps failed", built.failed)
	}
	if err := repo.ClearStaleAt(refreshCtx, userID, profileID, started); err != nil {
		slog.WarnContext(ctx, "failed to clear stale profile marker", "component", "recommendations", "user_id", userID, "profile_id", profileID, "error", err)
	}
	return nil
}

// claimProfileRefresh marks a profile's refresh pending on this server. It
// returns false when one is already queued or running.
func (w *Worker) claimProfileRefresh(key string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, exists := w.profileRefreshPending[key]; exists {
		return false
	}
	w.profileRefreshPending[key] = struct{}{}
	return true
}

func (w *Worker) clearProfileRefreshPending(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.profileRefreshPending, key)
}

func profileRefreshKey(userID int, profileID string) string {
	return fmt.Sprintf("%d:%s", userID, profileID)
}

// refreshThrottle lets each key through at most once per interval. The zero
// value is ready to use.
type refreshThrottle struct {
	mu        sync.Mutex
	now       func() time.Time // nil means time.Now
	last      map[string]time.Time
	lastPrune time.Time
}

// allow reports whether key may go ahead now, and records it when it may.
func (t *refreshThrottle) allow(key string, interval time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	if t.now != nil {
		now = t.now()
	}
	if t.last == nil {
		t.last = make(map[string]time.Time)
	}
	// Forget expired keys once per interval, so the map holds only the
	// profiles let through recently.
	if now.Sub(t.lastPrune) >= interval {
		for k, at := range t.last {
			if now.Sub(at) >= interval {
				delete(t.last, k)
			}
		}
		t.lastPrune = now
	}
	if at, ok := t.last[key]; ok && now.Sub(at) < interval {
		return false
	}
	t.last[key] = now
	return true
}

// due reports whether allow would let key go ahead now, without recording
// it.
func (t *refreshThrottle) due(key string, interval time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	if t.now != nil {
		now = t.now()
	}
	at, ok := t.last[key]
	return !ok || now.Sub(at) >= interval
}
