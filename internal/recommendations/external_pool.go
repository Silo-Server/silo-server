package recommendations

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

// Pool rules and per-run budgets for outside titles. The budgets are
// constants: a run that spends one stops cleanly and the next run continues
// from stored state.
const (
	// externalProfileListCap is how many seed-list titles a profile's pool
	// keeps: those named by the most of its seeds.
	externalProfileListCap = 500
	externalSeedRefetch    = 30 * 24 * time.Hour
	externalSetRefetch     = 7 * 24 * time.Hour
	externalDetailRefetch  = 120 * 24 * time.Hour

	externalRetentionDetail       = 150 * 24 * time.Hour
	externalRetentionUnreferenced = 30 * 24 * time.Hour

	externalTMDBRequestBudget = 10000
	externalEmbedBudget       = 10000

	// externalFetchConcurrency bounds the TMDB calls a run has in flight; the
	// shared client paces them.
	externalFetchConcurrency = 4
	// externalMaxConsecutiveFetchFailures ends a run whose TMDB calls keep
	// failing: TMDB is down, not the titles.
	externalMaxConsecutiveFetchFailures = 20
	// externalDetailChunk is how many pool titles a run fetches before it
	// embeds them.
	externalDetailChunk = 50
	// externalOwnedLookupBatch bounds one owned-title lookup.
	externalOwnedLookupBatch = 500
)

// ExternalTMDB is the TMDB client outside titles are fetched with.
// *tmdb.Client satisfies it; its limiter paces every call.
type ExternalTMDB interface {
	GetMediaDetailInLanguage(ctx context.Context, mediaType string, id int, language string) (*tmdb.MediaDetail, error)
	DiscoverPage(ctx context.Context, mediaType string, params tmdb.DiscoverParams, page int) (*tmdb.MediaPage, error)
}

// externalOwnedLookup finds library titles by provider ID, the lookup
// Requests' CatalogPresence uses. *catalog.ItemRepository satisfies it.
type externalOwnedLookup interface {
	LookupExternalIDs(ctx context.Context, mediaType string, candidates []catalog.ExternalIDLookupCandidate) ([]catalog.ExternalIDMatchRow, error)
}

// ErrNoExternalTMDB ends an outside-title run on an engine built without a
// TMDB client.
var ErrNoExternalTMDB = errors.New("outside-title recommendations need a TMDB client, and none is configured")

// WithExternalTMDB installs the TMDB client outside titles are fetched with,
// and returns the engine. Pass a nil interface, not a nil *tmdb.Client, when
// there is none.
func (e *Engine) WithExternalTMDB(client ExternalTMDB) *Engine {
	if e != nil {
		e.externalTMDB = client
	}
	return e
}

func (e *Engine) externalOwnedLookup() externalOwnedLookup {
	if e.externalOwned != nil {
		return e.externalOwned
	}
	if e.itemRepo != nil {
		return e.itemRepo
	}
	return nil
}

// ExternalPoolOptions configures BuildExternalPool.
type ExternalPoolOptions struct {
	// MaxTitles caps the outside titles the server stores;
	// the configured default when <= 0.
	MaxTitles int
	// OnProgress reports phase progress while the run is executing.
	OnProgress func(phase string, done, total int)
	// held owns the PostgreSQL session used for pool writes.
	held clusterLock
	// partialProfiles prevents cap eviction if some profiles could not be read.
	partialProfiles bool
}

// ExternalPoolCounts reports one BuildExternalPool run.
type ExternalPoolCounts struct {
	Profiles int `json:"profiles"`
	// Seeds counts distinct seed titles across profiles.
	Seeds int `json:"seeds"`
	// SourcesFetched counts seed lists and sets fetched; SourcesFailed those
	// whose fetch failed, retried next run; SourcesLeft those due that the
	// budget left for the next run.
	SourcesFetched int `json:"sources_fetched"`
	SourcesFailed  int `json:"sources_failed"`
	SourcesLeft    int `json:"sources_left"`
	// PoolTitles counts the outside titles the server keeps.
	PoolTitles int `json:"pool_titles"`
	// TitlesDeleted counts stored titles deleted to stay under the cap.
	TitlesDeleted int64 `json:"titles_deleted"`
	// DetailsFetched, DetailsNotFound and DetailsFailed count title detail
	// calls; DetailsLeft counts titles still needing a detail or vector.
	DetailsFetched  int `json:"details_fetched"`
	DetailsNotFound int `json:"details_not_found"`
	DetailsFailed   int `json:"details_failed"`
	DetailsLeft     int `json:"details_left"`
	// DetailsOwned counts fetched titles the library turned out to have
	// under their IMDb or TVDB ID; they are not stored.
	DetailsOwned int `json:"details_owned"`
	// TextUnchanged counts refetched titles whose stored vector was kept
	// because their text had not changed.
	TextUnchanged int `json:"text_unchanged"`
	// RefusedSkipped counts titles not embedded because the provider refused
	// the same text before (see Engine.externalRefusedEmbeds): with a fresh
	// stored detail they are not fetched either.
	RefusedSkipped int         `json:"refused_skipped"`
	Embeddings     EmbedCounts `json:"embeddings"`
	TMDBRequests   int         `json:"tmdb_requests"`
	// BudgetExhausted is set when a per-run budget stopped the run.
	BudgetExhausted bool `json:"budget_exhausted"`
	// VectorsSkipped says why the run fetched no details and stored no
	// vectors, or is empty; VectorsSkippedReason holds its machine-readable
	// code (ExternalSkip*) for localized UI guidance.
	VectorsSkipped       string `json:"vectors_skipped,omitempty"`
	VectorsSkippedReason string `json:"vectors_skipped_reason,omitempty"`
}

// externalLimits are a run's budgets.
type externalLimits struct {
	tmdbRequests int
	embeds       int
}

// BuildExternalPool builds the server's pool of outside titles: it fetches
// the TMDB lists of every profile's seeds and the shared sets that are due,
// chooses the pool within opts.MaxTitles, and fetches and embeds the pool
// titles that need it, within this run's budgets. The job runs it under a
// cluster-wide lock. Vectors use the embedding lock library vectors made and
// never create it.
func (e *Engine) BuildExternalPool(ctx context.Context, opts ExternalPoolOptions) (ExternalPoolCounts, error) {
	if e.externalTMDB == nil {
		return ExternalPoolCounts{}, ErrNoExternalTMDB
	}
	subjects, err := e.repo.profilesWithTasteVector(ctx)
	if err != nil {
		return ExternalPoolCounts{}, err
	}
	profiles := make([]ExternalProfile, 0, len(subjects))
	var firstErr error
	failed := 0
	for _, s := range subjects {
		if err := ctx.Err(); err != nil {
			return ExternalPoolCounts{}, err
		}
		p, ok, err := e.externalProfile(ctx, s.UserID, s.ProfileID, nil)
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if ok {
			profiles = append(profiles, p)
		}
	}
	opts.partialProfiles = failed > 0
	counts, err := e.buildExternalPool(ctx, profiles, opts, externalLimits{tmdbRequests: externalTMDBRequestBudget, embeds: externalEmbedBudget})
	if failed > 0 {
		err = errors.Join(err, fmt.Errorf("read outside-title seeds for %d of %d profiles: %w", failed, len(subjects), firstErr))
	}
	return counts, err
}

// externalRun is one BuildExternalPool run past the seed reads.
type externalRun struct {
	e      *Engine
	tmdb   ExternalTMDB
	now    time.Time
	held   clusterLock
	writer externalPoolWriter
	counts ExternalPoolCounts
	// tmdbLeft is the TMDB requests the run may still make.
	tmdbLeft        int
	partialProfiles bool
	onProgress      func(phase string, done, total int)
	// failures counts TMDB calls in a row that failed.
	failures atomic.Int32
	mu       sync.Mutex
}

func (e *Engine) buildExternalPool(ctx context.Context, profiles []ExternalProfile, opts ExternalPoolOptions, limits externalLimits) (ExternalPoolCounts, error) {
	if e.externalTMDB == nil {
		return ExternalPoolCounts{}, ErrNoExternalTMDB
	}
	maxTitles := opts.MaxTitles
	if maxTitles <= 0 {
		maxTitles = config.DefaultExternalRecommendationsMaxTitles
	}
	now, err := e.repo.Now(ctx)
	if err != nil {
		return ExternalPoolCounts{}, err
	}
	r := &externalRun{e: e, tmdb: e.externalTMDB, now: now, held: opts.held, partialProfiles: opts.partialProfiles, onProgress: opts.OnProgress}
	r.writer = e.repo.externalPoolWriter
	if opts.held != nil {
		r.writer = opts.held.poolWriter(r.writer)
	}
	r.tmdbLeft = limits.tmdbRequests
	r.counts.Profiles = len(profiles)
	err = r.run(ctx, profiles, maxTitles, limits.embeds)
	r.counts.TMDBRequests = limits.tmdbRequests - r.tmdbLeft
	return r.counts, err
}

func (r *externalRun) run(ctx context.Context, profiles []ExternalProfile, maxTitles, embedBudget int) error {
	// Verify the embedding provider and lock before spending TMDB calls.
	lock, code, msg, err := r.e.externalEmbeddingLock(ctx)
	if err != nil {
		return err
	}
	if lock == nil {
		r.counts.VectorsSkipped, r.counts.VectorsSkippedReason = msg, code
	} else if len(profiles) == 0 && !r.partialProfiles {
		r.counts.VectorsSkipped, r.counts.VectorsSkippedReason = errNoExternalTasteProfiles.Error(), ExternalSkipNoTasteProfiles
	}
	var setKeys []string
	for _, p := range profiles {
		setKeys = append(setKeys, p.SetSources...)
	}
	setKeys = uniqueSorted(setKeys)
	if r.counts.VectorsSkippedReason != "" {
		// Still choose from what is stored, which calls no TMDB: it keeps the
		// pool referenced, so retention does not delete it while an admin
		// fixes the provider, and it applies the cap and drops titles the
		// library has gained.
		_, err := r.choosePool(ctx, profiles, setKeys, maxTitles)
		return err
	}
	// Sets first: the server cap admits them first, and they are few requests.
	if err := r.fetchSets(ctx, setKeys); err != nil {
		return err
	}
	seedKeys := externalSeedFetchOrder(profiles)
	r.counts.Seeds = len(seedKeys)
	if err := r.fetchSeedLists(ctx, seedKeys); err != nil {
		return err
	}

	chosen, err := r.choosePool(ctx, profiles, setKeys, maxTitles)
	if err != nil {
		return err
	}
	return r.refreshTitles(ctx, lock, chosen, embedBudget)
}

// externalSeedFetchOrder lists the profiles' distinct seed titles in the
// order their lists are fetched: every profile's heaviest seed, then every
// profile's second, and so on, so a budget that runs out leaves each profile
// its heaviest seeds.
func externalSeedFetchOrder(profiles []ExternalProfile) []ExternalTitleKey {
	var out []ExternalTitleKey
	seen := map[ExternalTitleKey]struct{}{}
	for rank := 0; ; rank++ {
		more := false
		for _, p := range profiles {
			if rank >= len(p.Seeds) {
				continue
			}
			more = true
			k := p.Seeds[rank].Key
			if _, dup := seen[k]; !dup {
				seen[k] = struct{}{}
				out = append(out, k)
			}
		}
		if !more {
			return out
		}
	}
}

func uniqueSorted(keys []string) []string {
	slices.Sort(keys)
	return slices.Compact(keys)
}

// takeRequest spends one TMDB request of the budget, or reports that none is
// left.
func (r *externalRun) takeRequest(ctx context.Context) (bool, error) {
	// The lock session is also used to store source results. Serialize its
	// probes and writes while TMDB fetches run concurrently.
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkHeld(ctx); err != nil {
		return false, err
	}
	if r.tmdbLeft > 0 {
		r.tmdbLeft--
		return true, nil
	}
	r.counts.BudgetExhausted = true
	return false, nil
}

// Parallel fetches hold mu while using the job session.
func (r *externalRun) checkHeld(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.held != nil {
		return r.held.Check(ctx)
	}
	return nil
}

var errExternalTMDBUnavailable = errors.New("TMDB calls keep failing")

// fetchOutcome records one TMDB call's result for the failure streak, and
// returns the error that ends the run once too many in a row failed.
func (r *externalRun) fetchOutcome(err error) error {
	if err == nil || errors.Is(err, tmdb.ErrNotFound) {
		r.failures.Store(0)
		return nil
	}
	if r.failures.Add(1) >= externalMaxConsecutiveFetchFailures {
		return fmt.Errorf("outside titles: %w: %w", errExternalTMDBUnavailable, err)
	}
	return nil
}

// forEachBounded runs fn for each of n indexes, externalFetchConcurrency at a
// time, until fn returns stop or an error, or ctx ends.
func forEachBounded(ctx context.Context, n int, fn func(ctx context.Context, i int) (stop bool, err error)) error {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(externalFetchConcurrency)
	var stopped atomic.Bool
	for i := 0; i < n; i++ {
		if stopped.Load() || gctx.Err() != nil {
			break
		}
		g.Go(func() error {
			if stopped.Load() {
				return nil
			}
			stop, err := fn(gctx, i)
			if stop {
				stopped.Store(true)
			}
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	return ctx.Err()
}

// recordSource stores a fetch result, counting it.
func (r *externalRun) recordSource(ctx context.Context, res ExternalSourceResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkHeld(ctx); err != nil {
		return err
	}
	if err := r.writer.RecordExternalSource(ctx, res); err != nil {
		return err
	}
	if res.Status == ExternalSourceFailed {
		r.counts.SourcesFailed++
	} else {
		r.counts.SourcesFetched++
	}
	return nil
}

func (r *externalRun) leaveSources(n int) {
	r.mu.Lock()
	r.counts.SourcesLeft += n
	r.mu.Unlock()
}

// fetchSeedLists fetches, for each seed whose lists are due, the TMDB detail
// that carries its recommendations and similar lists.
func (r *externalRun) fetchSeedLists(ctx context.Context, seeds []ExternalTitleKey) error {
	keys := make([]string, len(seeds))
	bySource := make(map[string]ExternalTitleKey, len(seeds))
	for i, s := range seeds {
		keys[i] = externalSeedSourceKey(s)
		bySource[keys[i]] = s
	}
	due, err := r.e.repo.ExternalSourcesToFetch(ctx, keys, r.now.Add(-externalSeedRefetch))
	if err != nil {
		return err
	}
	// Seed lists use at most half of the TMDB requests left, so a server
	// with more due seeds than the budget still fetches details in the same
	// run; the rest of the seeds wait for the next one.
	fetch := due[:min(len(due), r.tmdbLeft/2)]
	if len(fetch) < len(due) {
		r.mu.Lock()
		r.counts.BudgetExhausted = true
		r.mu.Unlock()
	}
	if len(fetch) > 0 {
		r.reportProgress(ExternalPhaseSources, 0, len(fetch))
	}
	var done atomic.Int64
	err = forEachBounded(ctx, len(fetch), func(ctx context.Context, i int) (bool, error) {
		if ok, err := r.takeRequest(ctx); !ok {
			return true, err
		}
		key := fetch[i]
		seed := bySource[key]
		// Lists hold IDs only, so the default-language detail serves, and
		// shares its cache entry with the Requests title page.
		detail, err := r.tmdb.GetMediaDetailInLanguage(ctx, seed.MediaType, seed.TMDBID, "")
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		if stop := r.fetchOutcome(err); stop != nil {
			return true, stop
		}
		res := ExternalSourceResult{SourceKey: key, Status: ExternalSourceOK}
		switch {
		case errors.Is(err, tmdb.ErrNotFound):
			// Recorded as fetched with nothing, so it is not asked again
			// until the seed's lists are due.
			res.Status = ExternalSourceEmpty
		case err != nil:
			res.Status, res.Error = ExternalSourceFailed, err.Error()
		default:
			res.Links = externalSeedLinks(seed.MediaType, detail)
		}
		n := int(done.Add(1))
		r.reportProgress(ExternalPhaseSources, n, len(fetch))
		return false, r.recordSource(ctx, res)
	})
	r.leaveSources(len(due) - int(done.Load()))
	return err
}

// externalSeedLinks are a seed detail's recommendations and similar lists.
// A list holds titles of the seed's own media type.
func externalSeedLinks(mediaType string, detail *tmdb.MediaDetail) []ExternalLink {
	if detail == nil {
		return nil
	}
	var links []ExternalLink
	for _, list := range []struct {
		name    string
		results []tmdb.MediaResult
	}{{ExternalListRecommendations, detail.Recommendations}, {ExternalListSimilar, detail.Similar}} {
		rank := 0
		for _, res := range list.results {
			if res.ID <= 0 {
				continue
			}
			rank++
			links = append(links, ExternalLink{List: list.name, Rank: rank, Title: ExternalTitleKey{MediaType: mediaType, TMDBID: res.ID}})
		}
	}
	return links
}

// externalSet is one shared set of outside titles, fetched through TMDB's
// discover endpoint, most-voted first.
type externalSet struct {
	key       string
	mediaType string
	// recent limits the set to titles released this year or last.
	recent   bool
	language string
	minVotes int
	size     int
}

// externalSetSpecs returns the recent sets and, for each language, its recent
// and all-time sets, in source-key order within each group.
func externalSetSpecs(languages []string) []externalSet {
	sets := []externalSet{
		{key: "set:recent:movie", mediaType: ExternalMediaMovie, recent: true, minVotes: 100, size: 600},
		{key: "set:recent:series", mediaType: ExternalMediaSeries, recent: true, minVotes: 100, size: 250},
	}
	for _, l := range languages {
		for _, mt := range []string{ExternalMediaMovie, ExternalMediaSeries} {
			sets = append(sets,
				externalSet{key: "set:lang:" + l + ":recent:" + mt, mediaType: mt, recent: true, language: l, minVotes: 10, size: 300},
				externalSet{key: "set:lang:" + l + ":top:" + mt, mediaType: mt, language: l, minVotes: 10, size: 200},
			)
		}
	}
	return sets
}

// externalSetByKey parses a set source key built by externalSetSpecs.
func externalSetByKey(key string) (externalSet, bool) {
	var languages []string
	if rest, ok := strings.CutPrefix(key, "set:lang:"); ok {
		language, _, _ := strings.Cut(rest, ":")
		languages = []string{language}
	}
	for _, s := range externalSetSpecs(languages) {
		if s.key == key {
			return s, true
		}
	}
	return externalSet{}, false
}

func (s externalSet) discoverParams(now time.Time) tmdb.DiscoverParams {
	p := tmdb.DiscoverParams{SortBy: "vote_count.desc", VoteCountGte: s.minVotes, OriginalLanguage: s.language}
	if s.recent {
		p.ReleaseDateGte = strconv.Itoa(now.UTC().Year()-1) + "-01-01"
		p.ReleaseDateLte = now.UTC().Format(time.DateOnly)
	}
	return p
}

// fetchSets fetches the due sets among keys, paging each until it holds its
// size or TMDB runs out. A set the budget cuts short is not recorded, so the
// next run fetches it whole.
func (r *externalRun) fetchSets(ctx context.Context, keys []string) error {
	due, err := r.e.repo.ExternalSourcesToFetch(ctx, keys, r.now.Add(-externalSetRefetch))
	if err != nil {
		return err
	}
	var done atomic.Int64
	err = forEachBounded(ctx, len(due), func(ctx context.Context, i int) (bool, error) {
		set, ok := externalSetByKey(due[i])
		if !ok {
			return false, fmt.Errorf("outside titles: unknown set %q", due[i])
		}
		res, complete, err := r.fetchSet(ctx, set)
		if err != nil || !complete {
			return true, err
		}
		done.Add(1)
		return false, r.recordSource(ctx, res)
	})
	r.leaveSources(len(due) - int(done.Load()))
	return err
}

// fetchSet pages through one set. complete is false when the budget ran out
// first.
func (r *externalRun) fetchSet(ctx context.Context, set externalSet) (res ExternalSourceResult, complete bool, err error) {
	res = ExternalSourceResult{SourceKey: set.key, Status: ExternalSourceOK}
	discoverType := "movie"
	if set.mediaType == ExternalMediaSeries {
		discoverType = "tv"
	}
	params := set.discoverParams(r.now)
	seen := map[int]struct{}{}
	for page := 1; len(res.Links) < set.size; page++ {
		if ok, err := r.takeRequest(ctx); !ok {
			return res, false, err
		}
		got, fetchErr := r.tmdb.DiscoverPage(ctx, discoverType, params, page)
		if ctx.Err() != nil {
			return res, false, ctx.Err()
		}
		if stop := r.fetchOutcome(fetchErr); stop != nil {
			return res, false, stop
		}
		if fetchErr != nil {
			// The set keeps its previous links until a fetch succeeds.
			res = ExternalSourceResult{SourceKey: set.key, Status: ExternalSourceFailed, Error: fetchErr.Error()}
			break
		}
		for _, m := range got.Results {
			if _, dup := seen[m.ID]; dup || m.ID <= 0 || len(res.Links) == set.size {
				continue
			}
			seen[m.ID] = struct{}{}
			res.Links = append(res.Links, ExternalLink{List: ExternalListSet, Rank: len(res.Links) + 1, Title: ExternalTitleKey{MediaType: set.mediaType, TMDBID: m.ID}})
		}
		if len(got.Results) == 0 || page >= got.TotalPages {
			break
		}
	}
	return res, true, nil
}

// choosePool decides the outside titles the server keeps (see
// chooseExternalPool), deletes stored titles past maxTitles, and marks the
// sources and titles the pool uses as referenced.
func (r *externalRun) choosePool(ctx context.Context, profiles []ExternalProfile, setKeys []string, maxTitles int) ([]ExternalTitleKey, error) {
	// Every profile's pool is held until the choice, so only what the choice
	// reads is kept: the seed lists alone would take about half a megabyte
	// a profile.
	pools := make([][]externalPoolCount, len(profiles))
	var sources []string
	for i, p := range profiles {
		seedSources := p.SeedSources()
		pool, err := r.e.repo.ExternalPool(ctx, ExternalPoolQuery{SeedSources: seedSources, SetSources: p.SetSources, IncludeUnstored: true})
		if err != nil {
			return nil, err
		}
		pools[i] = countPool(pool)
		sources = append(sources, seedSources...)
	}
	sets, err := r.e.repo.externalSetTitles(ctx, setKeys)
	if err != nil {
		return nil, err
	}
	// Stored titles carry the IMDb and TVDB IDs their detail gave, so a
	// library item matched by one of those counts as owning them.
	stored, err := r.e.repo.externalStoredTitleIDs(ctx)
	if err != nil {
		return nil, err
	}
	ids := make(map[ExternalTitleKey]externalTitleIDs, len(stored))
	for _, s := range stored {
		ids[s.key] = s
	}
	addKey := func(k ExternalTitleKey) {
		if _, ok := ids[k]; !ok {
			ids[k] = externalTitleIDs{key: k}
		}
	}
	for _, pool := range pools {
		for _, t := range pool {
			addKey(t.Key)
		}
	}
	for _, s := range sets {
		for _, k := range s.titles {
			addKey(k)
		}
	}
	owned, err := r.ownedTitles(ctx, maps.Values(ids))
	if err != nil {
		return nil, err
	}
	chosen, named := chooseExternalPool(sets, pools, owned, externalProfileListCap, maxTitles)

	// Stored titles the library now has go with the links that name them,
	// so a title owned under its IMDb or TVDB ID is not chosen and fetched
	// again; the least-named unused titles past the cap go and keep their
	// links.
	var unowned, nowOwned []ExternalTitleKey
	for _, s := range stored {
		if owned[s.key] {
			nowOwned = append(nowOwned, s.key)
		} else {
			unowned = append(unowned, s.key)
		}
	}
	if err := r.writer.forgetExternalTitles(ctx, nowOwned); err != nil {
		return nil, err
	}
	r.counts.TitlesDeleted = int64(len(nowOwned))
	if r.partialProfiles {
		chosen = preserveExternalPool(chosen, unowned, maxTitles)
	} else if excess := externalExcessTitles(unowned, chosen, named, maxTitles); len(excess) > 0 {
		deleted, err := r.writer.DeleteExternalTitles(ctx, excess)
		if err != nil {
			return nil, err
		}
		r.counts.TitlesDeleted += deleted
	}
	r.counts.PoolTitles = len(chosen)
	if err := r.writer.TouchExternalReferences(ctx, uniqueSorted(append(sources, setKeys...)), chosen); err != nil {
		return nil, err
	}
	return chosen, nil
}

// preserveExternalPool keeps already stored titles when a partial scan cannot
// tell which ones a failed profile still needs. New titles use only free slots.
func preserveExternalPool(chosen, stored []ExternalTitleKey, maxTitles int) []ExternalTitleKey {
	kept := make(map[ExternalTitleKey]bool, len(stored))
	for _, key := range stored {
		kept[key] = true
	}
	room := maxTitles - len(stored)
	return slices.DeleteFunc(chosen, func(key ExternalTitleKey) bool {
		if kept[key] {
			return false
		}
		if room <= 0 {
			return true
		}
		room--
		return false
	})
}

// externalPoolCount is a pool title as chooseExternalPool reads it: the
// title and the number of the profile's seeds that name it.
type externalPoolCount struct {
	Key   ExternalTitleKey
	seeds int
}

// countPool reduces a profile's pool to externalPoolCounts, in its order.
func countPool(pool []ExternalPoolTitle) []externalPoolCount {
	out := make([]externalPoolCount, len(pool))
	for i, t := range pool {
		out[i] = externalPoolCount{Key: t.Key, seeds: len(t.Seeds)}
	}
	return out
}

// externalSetTitles is one shared set's titles, best rank first.
type externalSetTitles struct {
	key    string
	titles []ExternalTitleKey
}

// isRecentSet reports whether a set source key names one of the two recent
// sets, as opposed to a language set.
func isRecentSet(key string) bool { return strings.HasPrefix(key, "set:recent:") }

// chooseExternalPool picks the server's pool. Each profile keeps the listCap
// titles of its pool (most-named first, as ExternalPool returns them) that
// its seeds name, plus its set titles, leaving out owned titles. The server
// keeps at most maxTitles, and sets fill at most half of it:
//
//   - the recent sets first, best rank first across them;
//   - then the language sets share what is left of that half. When their
//     titles do not all fit, each keeps its best-ranked titles, and the room
//     is divided across them in proportion to their sizes (largest remainder),
//     so no language is dropped whole while another is kept whole;
//   - then list titles named by the most profiles (ties by the seeds naming
//     them, then key) fill the rest, including whatever the sets left.
//
// named counts, for every title in an uncapped profile pool, the profiles
// that have it, so the stored titles the pool no longer uses can be dropped
// least-named first.
func chooseExternalPool(sets []externalSetTitles, pools [][]externalPoolCount, owned map[ExternalTitleKey]bool, listCap, maxTitles int) (chosen []ExternalTitleKey, named map[ExternalTitleKey]int) {
	named = map[ExternalTitleKey]int{}
	profiles := map[ExternalTitleKey]int{}
	seeds := map[ExternalTitleKey]int{}
	for _, pool := range pools {
		listed := 0
		for _, t := range pool {
			if owned[t.Key] {
				continue
			}
			named[t.Key]++
			if t.seeds == 0 || listed == listCap {
				continue
			}
			listed++
			profiles[t.Key]++
			seeds[t.Key] += t.seeds
		}
	}
	inChosen := map[ExternalTitleKey]bool{}
	add := func(k ExternalTitleKey, limit int) bool {
		if len(chosen) < limit && !owned[k] && !inChosen[k] {
			inChosen[k] = true
			chosen = append(chosen, k)
			return true
		}
		return false
	}
	setCap := maxTitles / 2
	var recent, languages []externalSetTitles
	for _, s := range sets {
		if isRecentSet(s.key) {
			recent = append(recent, s)
		} else {
			languages = append(languages, s)
		}
	}
	// The recent sets, rank by rank across them.
	for rank := 0; ; rank++ {
		more := false
		for _, s := range recent {
			if rank < len(s.titles) {
				more = true
				add(s.titles[rank], setCap)
			}
		}
		if !more {
			break
		}
	}
	// The language sets, each held to its share of the room left.
	candidates := make([][]ExternalTitleKey, len(languages))
	for i, s := range languages {
		for _, k := range s.titles {
			if !owned[k] && !inChosen[k] {
				candidates[i] = append(candidates[i], k)
			}
		}
	}
	for i, quota := range proportionalQuotas(candidates, setCap-len(chosen)) {
		for _, k := range candidates[i] {
			if quota == 0 {
				break
			}
			if add(k, setCap) {
				quota--
			}
		}
	}
	// A quota counts a set's titles, including ones an earlier language set
	// has already added, and a set is charged only for titles it adds. So a
	// set whose titles were mostly added by another runs out before its
	// quota does. The room that leaves goes to the language sets' remaining
	// titles, rank by rank across them.
	for rank := 0; len(chosen) < setCap; rank++ {
		more := false
		for _, c := range candidates {
			if rank < len(c) {
				more = true
				add(c[rank], setCap)
			}
		}
		if !more {
			break
		}
	}
	lists := make([]ExternalTitleKey, 0, len(profiles))
	for k := range profiles {
		lists = append(lists, k)
	}
	slices.SortFunc(lists, func(a, b ExternalTitleKey) int {
		return cmp.Or(cmp.Compare(profiles[b], profiles[a]), cmp.Compare(seeds[b], seeds[a]), compareExternalKeys(a, b))
	})
	for _, k := range lists {
		add(k, maxTitles)
	}
	return chosen, named
}

// proportionalQuotas divides room across lists in proportion to their
// lengths, by largest remainder (ties to the earlier list), giving each list
// at most its length.
func proportionalQuotas(lists [][]ExternalTitleKey, room int) []int {
	quotas := make([]int, len(lists))
	total := 0
	for i, l := range lists {
		quotas[i] = len(l)
		total += len(l)
	}
	room = max(room, 0)
	if total <= room {
		return quotas
	}
	remainders := make([]int, len(lists))
	given := 0
	for i, l := range lists {
		quotas[i] = room * len(l) / total
		remainders[i] = room * len(l) % total
		given += quotas[i]
	}
	order := make([]int, len(lists))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(remainders[b], remainders[a]) })
	for _, i := range order[:room-given] {
		quotas[i]++
	}
	return quotas
}

// externalExcessTitles returns the stored titles to delete so the stored
// titles and chosen together fit maxTitles: stored titles outside chosen,
// least-named first (ties by key). Under the cap it returns none, leaving
// unused titles to retention.
func externalExcessTitles(stored, chosen []ExternalTitleKey, named map[ExternalTitleKey]int, maxTitles int) []ExternalTitleKey {
	inChosen := make(map[ExternalTitleKey]bool, len(chosen))
	for _, k := range chosen {
		inChosen[k] = true
	}
	var unused []ExternalTitleKey
	for _, k := range stored {
		if !inChosen[k] {
			unused = append(unused, k)
		}
	}
	excess := len(chosen) + len(unused) - maxTitles
	if excess <= 0 {
		return nil
	}
	slices.SortFunc(unused, func(a, b ExternalTitleKey) int {
		return cmp.Or(cmp.Compare(named[a], named[b]), compareExternalKeys(a, b))
	})
	return unused[:min(excess, len(unused))]
}

// externalTitleIDs is an outside title's key with the other IDs its TMDB
// detail gave, "" and 0 when unknown.
type externalTitleIDs struct {
	key    ExternalTitleKey
	imdbID string
	tvdbID int
}

// ownedTitles reports which titles the catalog has, by the lookup Requests
// uses to show a title as available: TMDB ID, then TVDB, then IMDb.
func (r *externalRun) ownedTitles(ctx context.Context, titles iter.Seq[externalTitleIDs]) (map[ExternalTitleKey]bool, error) {
	lookup := r.e.externalOwnedLookup()
	if lookup == nil {
		return nil, errors.New("outside titles: no catalog lookup for owned titles")
	}
	owned := map[ExternalTitleKey]bool{}
	byType := map[string][]externalTitleIDs{}
	for t := range titles {
		byType[t.key.MediaType] = append(byType[t.key.MediaType], t)
	}
	for _, mediaType := range []string{ExternalMediaMovie, ExternalMediaSeries} {
		group := byType[mediaType]
		for start := 0; start < len(group); start += externalOwnedLookupBatch {
			batch := group[start:min(start+externalOwnedLookupBatch, len(group))]
			candidates := make([]catalog.ExternalIDLookupCandidate, len(batch))
			for i, t := range batch {
				candidates[i] = catalog.ExternalIDLookupCandidate{TMDBID: strconv.Itoa(t.key.TMDBID), IMDbID: t.imdbID}
				if t.tvdbID > 0 {
					candidates[i].TVDBID = strconv.Itoa(t.tvdbID)
				}
			}
			rows, err := lookup.LookupExternalIDs(ctx, mediaType, candidates)
			if err != nil {
				return nil, fmt.Errorf("outside titles: look up owned titles: %w", err)
			}
			for _, row := range rows {
				if id, err := strconv.Atoi(row.QueryTMDBID); err == nil {
					owned[ExternalTitleKey{MediaType: mediaType, TMDBID: id}] = true
				}
			}
		}
	}
	return owned, nil
}
