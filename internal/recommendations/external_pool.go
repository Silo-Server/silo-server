package recommendations

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

// Pool rules and per-run budgets for outside titles. The budgets are
// constants: a run that spends one stops cleanly and the next run continues
// from stored state.
const (
	// externalProfileListCap is how many seed-list titles a profile's pool
	// keeps: those named by the most of its seeds.
	externalProfileListCap = 500
	// DefaultExternalMaxTitles is the server cap when the caller passes none.
	DefaultExternalMaxTitles = 5000

	externalSeedRefetch   = 30 * 24 * time.Hour
	externalSetRefetch    = 7 * 24 * time.Hour
	externalDetailRefetch = 120 * 24 * time.Hour

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
	// DefaultExternalMaxTitles when <= 0.
	MaxTitles int
	// held is the job's cluster lock. Vectors are not stored once its
	// session has closed.
	held clusterLock
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
	// vectors, or is empty.
	VectorsSkipped string `json:"vectors_skipped,omitempty"`
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
	for _, s := range subjects {
		if err := ctx.Err(); err != nil {
			return ExternalPoolCounts{}, err
		}
		p, ok, err := e.externalProfile(ctx, s.UserID, s.ProfileID)
		if err != nil {
			return ExternalPoolCounts{}, err
		}
		if ok {
			profiles = append(profiles, p)
		}
	}
	return e.buildExternalPool(ctx, profiles, opts, externalLimits{tmdbRequests: externalTMDBRequestBudget, embeds: externalEmbedBudget})
}

// externalRun is one BuildExternalPool run past the seed reads.
type externalRun struct {
	e      *Engine
	tmdb   ExternalTMDB
	now    time.Time
	held   clusterLock
	counts ExternalPoolCounts
	// tmdbLeft is the TMDB requests the run may still make.
	tmdbLeft atomic.Int64
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
		maxTitles = DefaultExternalMaxTitles
	}
	now, err := e.repo.Now(ctx)
	if err != nil {
		return ExternalPoolCounts{}, err
	}
	r := &externalRun{e: e, tmdb: e.externalTMDB, now: now, held: opts.held}
	r.tmdbLeft.Store(int64(limits.tmdbRequests))
	r.counts.Profiles = len(profiles)
	err = r.run(ctx, profiles, maxTitles, limits.embeds)
	r.counts.TMDBRequests = limits.tmdbRequests - int(max(r.tmdbLeft.Load(), 0))
	return r.counts, err
}

func (r *externalRun) run(ctx context.Context, profiles []ExternalProfile, maxTitles, embedBudget int) error {
	// Sets first: the server cap admits them first, and they are few requests.
	var setKeys []string
	for _, p := range profiles {
		setKeys = append(setKeys, p.SetSources...)
	}
	setKeys = uniqueSorted(setKeys)
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
	return r.refreshTitles(ctx, chosen, embedBudget)
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
func (r *externalRun) takeRequest() bool {
	if r.tmdbLeft.Add(-1) >= 0 {
		return true
	}
	r.mu.Lock()
	r.counts.BudgetExhausted = true
	r.mu.Unlock()
	return false
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
	if err := r.e.repo.RecordExternalSource(ctx, res); err != nil {
		return err
	}
	r.mu.Lock()
	if res.Status == ExternalSourceFailed {
		r.counts.SourcesFailed++
	} else {
		r.counts.SourcesFetched++
	}
	r.mu.Unlock()
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
	var done atomic.Int64
	err = forEachBounded(ctx, len(due), func(ctx context.Context, i int) (bool, error) {
		if !r.takeRequest() {
			return true, nil
		}
		key := due[i]
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
		done.Add(1)
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
		if !r.takeRequest() {
			return res, false, nil
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
	pools := make([][]ExternalPoolTitle, len(profiles))
	var sources []string
	for i, p := range profiles {
		seedSources := p.SeedSources()
		pool, err := r.e.repo.ExternalPool(ctx, ExternalPoolQuery{SeedSources: seedSources, SetSources: p.SetSources, IncludeUnstored: true})
		if err != nil {
			return nil, err
		}
		pools[i] = pool
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
	var all []externalTitleIDs
	addKey := func(k ExternalTitleKey) {
		if t, ok := ids[k]; ok {
			all = append(all, t)
		} else {
			all = append(all, externalTitleIDs{key: k})
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
	all = append(all, stored...)
	owned, err := r.ownedTitles(ctx, all)
	if err != nil {
		return nil, err
	}
	chosen, named := chooseExternalPool(sets, pools, owned, externalProfileListCap, maxTitles)
	r.counts.PoolTitles = len(chosen)

	// Stored titles the library now has go; so do the least-named unused
	// ones past the cap.
	var unowned, remove []ExternalTitleKey
	for _, s := range stored {
		if owned[s.key] {
			remove = append(remove, s.key)
		} else {
			unowned = append(unowned, s.key)
		}
	}
	remove = append(remove, externalExcessTitles(unowned, chosen, named, maxTitles)...)
	if len(remove) > 0 {
		deleted, err := r.e.repo.DeleteExternalTitles(ctx, remove)
		if err != nil {
			return nil, err
		}
		r.counts.TitlesDeleted = deleted
	}
	if err := r.e.repo.TouchExternalReferences(ctx, uniqueSorted(append(sources, setKeys...)), chosen); err != nil {
		return nil, err
	}
	return chosen, nil
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
func chooseExternalPool(sets []externalSetTitles, pools [][]ExternalPoolTitle, owned map[ExternalTitleKey]bool, listCap, maxTitles int) (chosen []ExternalTitleKey, named map[ExternalTitleKey]int) {
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
			if len(t.Seeds) == 0 || listed == listCap {
				continue
			}
			listed++
			profiles[t.Key]++
			seeds[t.Key] += len(t.Seeds)
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
	// A title two language sets share counts toward both quotas and is added
	// once, so a set can fall short of its share. The room that leaves goes
	// to the language sets' remaining titles, rank by rank across them.
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
func (r *externalRun) ownedTitles(ctx context.Context, titles []externalTitleIDs) (map[ExternalTitleKey]bool, error) {
	lookup := r.e.externalOwnedLookup()
	if lookup == nil {
		return nil, errors.New("outside titles: no catalog lookup for owned titles")
	}
	owned := map[ExternalTitleKey]bool{}
	byType := map[string][]externalTitleIDs{}
	seen := map[ExternalTitleKey]bool{}
	for _, t := range titles {
		if !seen[t.key] {
			seen[t.key] = true
			byType[t.key.MediaType] = append(byType[t.key.MediaType], t)
		}
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

// externalSetTitles returns the titles each of the given sets names, best
// rank first, in source-key order. A set with no stored links is left out.
func (r *Repo) externalSetTitles(ctx context.Context, setKeys []string) ([]externalSetTitles, error) {
	if len(setKeys) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT source_key, media_type, tmdb_id
		FROM   external_title_links
		WHERE  source_key = ANY($1) AND list = 'set'
		ORDER  BY source_key, rank`, setKeys)
	if err != nil {
		return nil, fmt.Errorf("read outside set titles: %w", err)
	}
	defer rows.Close()
	var out []externalSetTitles
	for rows.Next() {
		var key string
		var k ExternalTitleKey
		if err := rows.Scan(&key, &k.MediaType, &k.TMDBID); err != nil {
			return nil, fmt.Errorf("read outside set titles: %w", err)
		}
		if len(out) == 0 || out[len(out)-1].key != key {
			out = append(out, externalSetTitles{key: key})
		}
		last := &out[len(out)-1]
		last.titles = append(last.titles, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read outside set titles: %w", err)
	}
	return out, nil
}

// externalStoredTitleIDs returns every stored outside title with its other
// IDs.
func (r *Repo) externalStoredTitleIDs(ctx context.Context) ([]externalTitleIDs, error) {
	rows, err := r.pool.Query(ctx, `SELECT media_type, tmdb_id, imdb_id, tvdb_id FROM external_titles`)
	if err != nil {
		return nil, fmt.Errorf("list outside titles: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (externalTitleIDs, error) {
		var t externalTitleIDs
		err := row.Scan(&t.key.MediaType, &t.key.TMDBID, &t.imdbID, &t.tvdbID)
		return t, err
	})
	if err != nil {
		return nil, fmt.Errorf("list outside titles: %w", err)
	}
	return out, nil
}

// PruneExternalTitles applies outside-title retention: titles whose last
// detail fetch is externalRetentionDetail old, and titles and sources nothing
// has referenced for externalRetentionUnreferenced. It calls neither TMDB nor
// the embedding provider, so it runs whether or not the feature is enabled.
func (e *Engine) PruneExternalTitles(ctx context.Context) (ExternalRetentionResult, error) {
	return pruneExternalTitles(ctx, e.repo)
}

func pruneExternalTitles(ctx context.Context, repo *Repo) (ExternalRetentionResult, error) {
	now, err := repo.Now(ctx)
	if err != nil {
		return ExternalRetentionResult{}, err
	}
	res, err := repo.PruneExternalTitles(ctx, now.Add(-externalRetentionDetail), now.Add(-externalRetentionUnreferenced))
	if err != nil {
		return res, err
	}
	if res.Titles > 0 || res.Sources > 0 {
		slog.InfoContext(ctx, "outside titles pruned", "component", "recommendations", "titles", res.Titles, "sources", res.Sources, "links", res.Links)
	}
	return res, nil
}

// externalRetentionInterval is how often RunExternalRetention prunes.
const externalRetentionInterval = 24 * time.Hour

// RunExternalRetention applies outside-title retention now and then daily,
// until ctx ends. It is for a server started with recommendations off: such
// a server has no Worker, so nothing else would delete the TMDB data an
// earlier configuration stored. Every API server runs it; the deletes are
// idempotent.
func RunExternalRetention(ctx context.Context, pool *pgxpool.Pool) {
	repo := NewRepo(pool)
	ticker := time.NewTicker(externalRetentionInterval)
	defer ticker.Stop()
	for {
		if _, err := pruneExternalTitles(ctx, repo); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "outside-title retention failed", "component", "recommendations", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
