package recommendations

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
	"golang.org/x/sync/errgroup"
)

// The holdout evaluation measures how well the personal rows predict what a
// profile goes on to enjoy. Each persona's interests are pools of titles
// around one title of a genre and media type in the toy catalog (see
// newToyCatalog); a seeded split of each pool gives the titles the persona
// had watched, rated, favorited or marked Not interested by the cutoff T, and
// the rest are the titles it enjoys after T, held out as targets. T is the
// database's now: only the signals before T are written, the catalog holds
// only titles added before T, and co-watch pairs come from those signals, so
// nothing from after T reaches the engine. The real refresh (refreshProfile)
// then builds each persona's rows, which the evaluation reads from the cache
// in their built order, before the daily rotation a read applies.
//
// The catalog adds titles, none of them a target, that each test a ranking
// rule the numbers should keep honest:
//
//   - multi-genre decoys, tagged with four genres and with vectors between
//     them, which a cluster row must not rank above nearer titles of fewer
//     genres (case 1);
//   - unembedded titles, which MMR must neither lift above embedded titles
//     nor bury (case 2): some co-watched with the focused persona's latest
//     watch, for Because You Watched, and some two of its three peers rate 5
//     stars, which Similar Users serves;
//   - obscure titles (no logo or keywords) rated 8.8 to 9.4 near four
//     interests, which the quality prior must not lift over notable titles
//     (case 3);
//   - every Horror and Documentary title stored at a length from 0.4 to 3,
//     so the scaled persona's k-means sees vectors that are not unit length
//     (case 4).
//
// Run it against a disposable, migrated database (see CONTRIBUTING.md):
//
//	SILO_TEST_DATABASE_URL=... go test -count=1 -v \
//	  -run '^TestRecommendationHoldoutEvalPostgres$' ./internal/recommendations/
//
// It logs a table with a line per persona and row type: main (For You),
// clusters (the "Because you enjoy" rows), byw (Because You Watched), similar
// (Similar Users) and page (every personal row's served window together),
// then the mean over personas. n is how many non-empty rows were measured.
// R@20 is the share of the persona's targets in a row's served window, its
// first ServedRowSize titles; N@20 is that window's NDCG with every target
// equally relevant; ILD is its mean pairwise cosine distance over titles with
// embeddings; decoy, unemb and obscure count the window's case titles; taste
// clusters lists the member counts of the persona's clusters. A row type
// with several rows shows their mean, and page shows only recall. The run is
// the same every time, so compare the table before and after a ranking
// change; each persona's name lists the cases it shows.

// evalFocusedMainRecallFloor is the least main-row Recall@20 of the focused
// persona, whose one interest is dense and unambiguous. A ranking that misses
// most of it has lost the taste profile's direction.
const evalFocusedMainRecallFloor = 0.25

// evalInterest is one interest of a persona: size titles of mediaType whose
// primary genre is genre, nearest the seed-th such title, of which history
// were engaged with before T.
type evalInterest struct {
	genre, mediaType string
	seed, size       int
	history          int
}

// evalPersonaSpec describes a persona; split seeds its history split.
type evalPersonaSpec struct {
	name      string
	interests []evalInterest
	split     uint64
	// rateEvery rates every rateEvery-th history title 5 stars.
	rateEvery int
	// notInterested marks that many of the first interest's history titles
	// Not interested instead of watching them.
	notInterested int
	// peer, from 1, makes the persona one of the focused persona's three
	// peers: it is not measured, and it rates 5 stars the peer-loved titles
	// two peers of three like, the fewest accounts Similar Users counts.
	peer int
}

var evalPersonaSpecs = []evalPersonaSpec{
	{name: "focused (cases 1-3)", interests: []evalInterest{{gThriller, "movie", 0, 40, 26}}, split: 1, rateEvery: 4},
	{name: "peer-1", interests: []evalInterest{{gThriller, "movie", 0, 40, 26}}, split: 2, rateEvery: 1, peer: 1},
	{name: "peer-2", interests: []evalInterest{{gThriller, "movie", 0, 40, 26}}, split: 3, rateEvery: 1, peer: 2},
	{name: "peer-3", interests: []evalInterest{{gThriller, "movie", 0, 40, 26}}, split: 4, rateEvery: 1, peer: 3},
	{name: "two tastes (cases 1, 3)", interests: []evalInterest{{gSciFi, "movie", 3, 26, 17}, {gRomance, "movie", 5, 26, 17}}, split: 5, rateEvery: 4},
	{name: "series, marks (cases 1, 3)", interests: []evalInterest{{gComedy, "movie", 2, 24, 16}, {gCrime, "series", 0, 14, 9}}, split: 6, rateEvery: 3, notInterested: 3},
	{name: "scaled norms (case 4)", interests: []evalInterest{{gHorror, "movie", 1, 26, 17}, {gDocumentary, "movie", 1, 24, 16}}, split: 7, rateEvery: 4},
}

// evalTitle is one catalog title as of T. vec is nil for a title without
// an embedding; stored is the vector written to the database, vec scaled.
type evalTitle struct {
	id, mediaType string
	genres        []string
	vec, stored   []float32
	rating        float64
	notable       bool
	addedDaysAgo  int
}

type evalCatalog struct {
	toy    *toyCatalog
	titles []evalTitle
	vecs   map[string][]float32
	// cases maps each case title to its case: evalDecoy, evalUnembedded or
	// evalObscure.
	cases map[string]int
	// cowatched and peerLoved are the unembedded titles of case 2.
	cowatched, peerLoved []string
}

// The kinds of case title a row's served window is counted for.
const (
	evalDecoy = iota
	evalUnembedded
	evalObscure
	evalCaseKinds
)

// newEvalCatalog builds the toy catalog and the case titles, the same on
// every run, under prefix.
func newEvalCatalog(prefix string) *evalCatalog {
	toy := newToyCatalog()
	c := &evalCatalog{toy: toy, vecs: map[string][]float32{}, cases: map[string]int{}}
	rng := rand.New(rand.NewPCG(20261123, 11))
	add := func(title evalTitle, kind int) {
		title.id = prefix + title.id
		if kind >= 0 {
			c.cases[title.id] = kind
		}
		title.stored = title.vec
		if title.vec != nil && (title.genres[0] == gHorror || title.genres[0] == gDocumentary) {
			title.stored = scaledVector(title.vec, 0.4+2.6*rng.Float64())
		}
		if title.vec != nil {
			c.vecs[title.id] = title.vec
		}
		c.titles = append(c.titles, title)
	}
	for _, item := range toy.items {
		add(evalTitle{id: item.id, mediaType: item.mediaType, genres: item.genres, vec: item.vec, rating: item.rating, notable: true,
			addedDaysAgo: int(relevanceNow.Sub(item.addedAt).Hours() / 24)}, -1)
	}
	decoys := []struct {
		mediaType string
		genres    []string
		n         int
	}{
		{"movie", []string{gThriller, gCrime, gAction, gDrama}, 45},
		{"movie", []string{gSciFi, gAction, gThriller, gFantasy}, 35},
		{"movie", []string{gRomance, gComedy, gDrama, gFamily}, 35},
		{"series", []string{gCrime, gDrama, gThriller, gComedy}, 15},
	}
	for f, d := range decoys {
		weights := map[string]float64{}
		for _, g := range d.genres {
			weights[g] = 0.45
		}
		for i := range d.n {
			add(evalTitle{id: fmt.Sprintf("decoy-%d-%02d", f, i), mediaType: d.mediaType, genres: d.genres,
				vec: toyVector(rng, d.mediaType, weights), rating: 6.4 + rng.NormFloat64(), notable: true, addedDaysAgo: 100 + i}, evalDecoy)
		}
	}
	for _, g := range []string{gThriller, gSciFi, gRomance, gComedy} {
		for i := range 10 {
			add(evalTitle{id: fmt.Sprintf("obscure-%s-%02d", strings.ToLower(strings.ReplaceAll(g, " ", "")), i), mediaType: "movie",
				genres: []string{g}, vec: toyVector(rng, "movie", map[string]float64{g: 1}), rating: 8.8 + 0.6*rng.Float64(), addedDaysAgo: 200 + i}, evalObscure)
		}
	}
	for i := range 6 {
		c.cowatched = append(c.cowatched, prefix+fmt.Sprintf("cowatched-%d", i))
		add(evalTitle{id: fmt.Sprintf("cowatched-%d", i), mediaType: "movie", genres: []string{gThriller}, rating: 6.5, notable: true, addedDaysAgo: 90}, evalUnembedded)
		c.peerLoved = append(c.peerLoved, prefix+fmt.Sprintf("peer-loved-%d", i))
		add(evalTitle{id: fmt.Sprintf("peer-loved-%d", i), mediaType: "movie", genres: []string{gThriller}, rating: 7.5, notable: true, addedDaysAgo: 90}, evalUnembedded)
	}
	return c
}

func scaledVector(vec []float32, by float64) []float32 {
	out := make([]float32, len(vec))
	for i, v := range vec {
		out[i] = float32(float64(v) * by)
	}
	return out
}

// pool returns the titles of interest in, nearest its seed title first,
// under prefix.
func (c *evalCatalog) pool(prefix string, in evalInterest) []string {
	var items []*toyItem
	for i := range c.toy.items {
		if item := &c.toy.items[i]; item.genres[0] == in.genre && item.mediaType == in.mediaType {
			items = append(items, item)
		}
	}
	seed := items[in.seed].vec
	slices.SortStableFunc(items, func(a, b *toyItem) int {
		return cmp.Compare(cosineSimilarity(seed, b.vec), cosineSimilarity(seed, a.vec))
	})
	ids := make([]string, 0, in.size)
	for _, item := range items[:in.size] {
		ids = append(ids, prefix+item.id)
	}
	return ids
}

// seed writes the catalog: every title matched, notable titles with a logo
// and five keywords, and the embedded ones with their stored vectors. The
// embeddings go in over several connections, since each insert updates the
// HNSW index.
func (c *evalCatalog) seed(t *testing.T, f *relevanceFixture) {
	t.Helper()
	items := &pgx.Batch{}
	const workers = 8
	embeddings := make([]*pgx.Batch, workers)
	for i := range embeddings {
		embeddings[i] = &pgx.Batch{}
	}
	for i, title := range c.titles {
		logo, keywords := "", []string{}
		if title.notable {
			logo, keywords = "logo.png", []string{"k1", "k2", "k3", "k4", "k5"}
		}
		var rating *float64
		if title.rating > 0 {
			r := math.Round(min(9.4, max(2.0, title.rating))*10) / 10
			rating = &r
		}
		items.Queue(`
			INSERT INTO media_items(content_id, type, title, status, genres, rating_imdb, logo_path, keywords, created_at)
			VALUES ($1, $2, $1, 'matched', $3, $4, $5, $6, NOW() - $7 * INTERVAL '1 day')`,
			title.id, title.mediaType, title.genres, rating, logo, keywords, title.addedDaysAgo)
		if title.stored != nil {
			padded, err := ensureCanonicalDimensions(title.stored)
			if err != nil {
				t.Fatal(err)
			}
			embeddings[i%workers].Queue(`INSERT INTO media_item_embeddings(media_item_id, embedding, model, canonical_text) VALUES ($1, $2, 'test-model', $1)`,
				title.id, pgvector.NewVector(padded))
		}
	}
	if err := f.pool.SendBatch(t.Context(), items).Close(); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	g, ctx := errgroup.WithContext(t.Context())
	for _, batch := range embeddings {
		g.Go(func() error { return f.pool.SendBatch(ctx, batch).Close() })
	}
	if err := g.Wait(); err != nil {
		t.Fatalf("seed embeddings: %v", err)
	}
}

// evalPersona is a persona written to the database: its profile, the titles
// it engaged with before T and the targets it enjoys after T.
type evalPersona struct {
	spec      evalPersonaSpec
	userID    int
	profileID string
	history   map[string]bool
	targets   map[string]bool
	// latest is its most recent completed watch.
	latest string
}

// newEvalPersona splits the persona's pools and writes its signals before T.
// History title j is a completed watch 2+7j%170 days before T; every
// rateEvery-th is rated 5 stars and every ninth favorited. The first
// notInterested of the first interest are marked instead.
func newEvalPersona(t *testing.T, f *relevanceFixture, c *evalCatalog, spec evalPersonaSpec) *evalPersona {
	t.Helper()
	p := &evalPersona{spec: spec, history: map[string]bool{}, targets: map[string]bool{}}
	p.userID, p.profileID = newTasteTestAccount(t, f.pool, fmt.Sprintf("%spersona-%d-", f.prefix, spec.split))
	batch := &pgx.Batch{}
	j, latestDays := 0, 0
	for k, in := range spec.interests {
		ids := c.pool(f.prefix, in)
		rng := rand.New(rand.NewPCG(spec.split, uint64(k)))
		rng.Shuffle(len(ids), func(a, b int) { ids[a], ids[b] = ids[b], ids[a] })
		for i, id := range ids {
			if i >= in.history {
				p.targets[id] = true
				continue
			}
			p.history[id] = true
			days := 2 + (7*j)%170
			j++
			if k == 0 && i < spec.notInterested {
				batch.Queue(`INSERT INTO user_not_interested(user_id, profile_id, media_item_id, added_at) VALUES ($1, $2, $3, NOW() - $4 * INTERVAL '1 day')`, p.userID, p.profileID, id, days)
				continue
			}
			queueEvalWatch(batch, p.userID, p.profileID, id, days)
			if p.latest == "" || days < latestDays {
				p.latest, latestDays = id, days
			}
			if j%spec.rateEvery == 0 {
				queueEvalRating(batch, p.userID, p.profileID, id, days)
			}
			if j%9 == 0 {
				batch.Queue(`INSERT INTO user_favorites(user_id, profile_id, media_item_id, added_at) VALUES ($1, $2, $3, NOW() - $4 * INTERVAL '1 day')`, p.userID, p.profileID, id, days)
			}
		}
	}
	for i, id := range c.peerLoved {
		if spec.peer > 0 && i%3 != spec.peer-1 {
			queueEvalRating(batch, p.userID, p.profileID, id, 20)
		}
	}
	if err := f.pool.SendBatch(t.Context(), batch).Close(); err != nil {
		t.Fatalf("%s: seed signals: %v", spec.name, err)
	}
	return p
}

func queueEvalWatch(batch *pgx.Batch, userID int, profileID, id string, days int) {
	batch.Queue(`
		INSERT INTO user_watch_progress(user_id, profile_id, media_item_id, completed, position_seconds, duration_seconds, updated_at)
		VALUES ($1, $2, $3, true, 5400, 5400, NOW() - $4 * INTERVAL '1 day')`, userID, profileID, id, days)
}

func queueEvalRating(batch *pgx.Batch, userID int, profileID, id string, days int) {
	batch.Queue(`INSERT INTO user_ratings(user_id, profile_id, media_item_id, rating, rated_at) VALUES ($1, $2, $3, 10, NOW() - $4 * INTERVAL '1 day')`, userID, profileID, id, days)
}

// seedEvalCowatch writes six accounts that each finished anchor and every
// co-watched title before T, and stores the co-watch pairs the nightly job
// computes from this run's watches.
func seedEvalCowatch(t *testing.T, f *relevanceFixture, c *evalCatalog, anchor string) {
	t.Helper()
	ctx := t.Context()
	batch := &pgx.Batch{}
	for i := range 6 {
		userID, profileID := newTasteTestAccount(t, f.pool, fmt.Sprintf("%scrowd-%d-", f.prefix, i))
		for _, id := range append([]string{anchor}, c.cowatched...) {
			queueEvalWatch(batch, userID, profileID, id, 10+i)
		}
	}
	if err := f.pool.SendBatch(ctx, batch).Close(); err != nil {
		t.Fatalf("seed crowd: %v", err)
	}
	watchers, err := f.repo.GetItemWatchers(ctx, DefaultMinWatchers, DefaultMaxWatchesPerUser)
	if err != nil {
		t.Fatal(err)
	}
	maps.DeleteFunc(watchers, func(id string, _ []string) bool { return !strings.HasPrefix(id, f.prefix) })
	pairs, err := computeCowatchMatrix(ctx, watchers, DefaultMinWatchers, DefaultMinShared, DefaultTopN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM item_cowatch WHERE item_id LIKE $1 OR similar_item_id LIKE $1`, f.prefix+"%")
	})
	if err := f.repo.UpsertCowatchPairs(ctx, pairs); err != nil {
		t.Fatal(err)
	}
}

// evalRowScore is one row's served window measured against the targets, and
// how many of each kind of case title it holds.
type evalRowScore struct {
	recall, ndcg, ild float64
	cases             [evalCaseKinds]float64
}

// scoreEvalWindow measures the served window of ids. Recall and NDCG count
// every target equally; ILD is the mean pairwise cosine distance of the
// window's titles that have an embedding.
func (c *evalCatalog) scoreEvalWindow(ids []string, targets map[string]bool) evalRowScore {
	window := ids[:min(len(ids), ServedRowSize)]
	var s evalRowScore
	var dcg, idcg float64
	hits := 0
	for i, id := range window {
		if targets[id] {
			hits++
			dcg += 1 / math.Log2(float64(i+2))
		}
		if kind, ok := c.cases[id]; ok {
			s.cases[kind]++
		}
	}
	for i := range min(len(targets), ServedRowSize) {
		idcg += 1 / math.Log2(float64(i+2))
	}
	if len(targets) > 0 {
		s.recall = float64(hits) / float64(len(targets))
		s.ndcg = dcg / idcg
	}
	var distance float64
	pairs := 0
	for i, a := range window {
		for _, b := range window[i+1:] {
			if c.vecs[a] != nil && c.vecs[b] != nil {
				distance += 1 - cosineSimilarity(c.vecs[a], c.vecs[b])
				pairs++
			}
		}
	}
	if pairs > 0 {
		s.ild = distance / float64(pairs)
	}
	return s
}

// evalRowKinds are the row types the report measures, in its order.
var evalRowKinds = []string{"main", "clusters", "byw", "similar"}

// evalRowKind names the kind of a cached row's rec_type, or "" for a row the
// report leaves out.
func evalRowKind(recType string) string {
	switch {
	case recType == RecTypeForYouMain:
		return "main"
	case strings.HasPrefix(recType, RecTypeForYouClusterPrefix):
		return "clusters"
	case recType == RecTypeBecauseWatched:
		return "byw"
	case recType == RecTypeSimilarUsersLiked:
		return "similar"
	}
	return ""
}

// evalResult is one persona's scores: each kind's non-empty rows, the
// recall of every personal row's served window together, and its taste
// clusters' sizes.
type evalResult struct {
	name       string
	rows       map[string][]evalRowScore
	pageRecall float64
	clusters   []int
}

// measureEvalPersona scores the rows the refresh cached for p, after
// checking that no held-out title reached the engine's input and that no row
// holds a title p engaged with before T.
func measureEvalPersona(t *testing.T, f *relevanceFixture, c *evalCatalog, p *evalPersona) evalResult {
	t.Helper()
	ctx := t.Context()
	excluded, err := f.engine.recommendationExclusionSet(ctx, p.userID, p.profileID)
	if err != nil {
		t.Fatal(err)
	}
	for id := range p.targets {
		if _, ok := excluded[id]; ok {
			t.Fatalf("%s: held-out title %s is in the engine's input before T", p.spec.name, id)
		}
	}
	res := evalResult{name: p.spec.name, rows: map[string][]evalRowScore{}}
	clusters, err := f.repo.GetTasteClusters(ctx, p.userID, p.profileID)
	if err != nil {
		t.Fatal(err)
	}
	for _, cl := range clusters {
		res.clusters = append(res.clusters, cl.MemberCount)
	}
	page := map[string]bool{}
	for key, ids := range f.cachedPersonalRows(t, p.userID, p.profileID) {
		for _, id := range ids {
			if p.history[id] {
				t.Errorf("%s: row %q holds %s, which it engaged with before T", p.spec.name, key, id)
			}
		}
		window := ids[:min(len(ids), ServedRowSize)]
		if len(slices.Compact(slices.Sorted(slices.Values(window)))) != len(window) {
			t.Errorf("%s: row %q serves a title twice: %v", p.spec.name, key, window)
		}
		recType, _, _ := strings.Cut(key, "|")
		kind := evalRowKind(recType)
		if kind == "" || len(ids) == 0 {
			continue
		}
		res.rows[kind] = append(res.rows[kind], c.scoreEvalWindow(ids, p.targets))
		for _, id := range ids[:min(len(ids), ServedRowSize)] {
			page[id] = true
		}
	}
	for id := range p.targets {
		if page[id] {
			res.pageRecall++
		}
	}
	res.pageRecall /= float64(len(p.targets))
	return res
}

// meanEvalScore is the mean of scores.
func meanEvalScore(scores []evalRowScore) evalRowScore {
	var m evalRowScore
	n := float64(len(scores))
	for _, s := range scores {
		m.recall += s.recall / n
		m.ndcg += s.ndcg / n
		m.ild += s.ild / n
		for k := range m.cases {
			m.cases[k] += s.cases[k] / n
		}
	}
	return m
}

// evalReport formats results as the table the evaluation logs.
func evalReport(results []evalResult) string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	printf := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
	line := func(name, kind string, n int, m evalRowScore, clusters string) {
		printf("%s\t%s\t%d\t%.3f\t%.3f\t%.3f\t%.1f\t%.1f\t%.1f\t%s\n",
			name, kind, n, m.recall, m.ndcg, m.ild, m.cases[evalDecoy], m.cases[evalUnembedded], m.cases[evalObscure], clusters)
	}
	printf("persona\trow\tn\tR@20\tN@20\tILD\tdecoy\tunemb\tobscure\ttaste clusters\n")
	means := map[string][]evalRowScore{}
	var pages []evalRowScore
	for _, r := range results {
		for _, kind := range evalRowKinds {
			rows := r.rows[kind]
			if len(rows) == 0 {
				printf("%s\t%s\t0\t-\t-\t-\t-\t-\t-\t\n", r.name, kind)
				continue
			}
			m := meanEvalScore(rows)
			means[kind] = append(means[kind], m)
			clusters := ""
			if kind == "clusters" {
				clusters = fmt.Sprint(r.clusters)
			}
			line(r.name, kind, len(rows), m, clusters)
		}
		pages = append(pages, evalRowScore{recall: r.pageRecall})
		printf("%s\tpage\t-\t%.3f\t-\t-\t-\t-\t-\t\n", r.name, r.pageRecall)
	}
	for _, kind := range evalRowKinds {
		if rows := means[kind]; len(rows) > 0 {
			line("mean", kind, len(rows), meanEvalScore(rows), "")
		}
	}
	printf("mean\tpage\t%d\t%.3f\t-\t-\t-\t-\t-\t\n", len(pages), meanEvalScore(pages).recall)
	_ = w.Flush()
	return b.String()
}

// evalPrefix prefixes every title the evaluation writes. Title IDs seed
// k-means (see kmeansSeed), so the clusters, and every row after them, are
// the same from run to run only under a fixed prefix.
const evalPrefix = "trel-holdout-eval-"

// newEvalFixture is newRelevanceFixture under evalPrefix, without the
// titles or the accounts, with their ratings, watches and taste profiles, a
// run that did not finish left behind: reused title IDs would otherwise
// carry that run's activity into this one's peer and co-watch thresholds.
// Two runs sharing a database would still clear each other's fixtures, so
// each needs its own; CI gives every package its own copy.
func newEvalFixture(t *testing.T) *relevanceFixture {
	t.Helper()
	f := newRelevanceFixture(t, "eval")
	f.prefix = evalPrefix
	for _, q := range []string{
		`DELETE FROM users WHERE username LIKE $1`,
		`DELETE FROM item_cowatch WHERE item_id LIKE $1 OR similar_item_id LIKE $1`,
		`DELETE FROM media_items WHERE content_id LIKE $1`,
	} {
		if _, err := f.pool.Exec(t.Context(), q, f.prefix+"%"); err != nil {
			t.Fatal(err)
		}
	}
	// Cleanups run last first, so this vacuum follows the delete below. The
	// evaluation deletes about a thousand embeddings, which stay in the HNSW
	// graph until a vacuum; left there, they lower the recall of the next
	// run's nearest-neighbor tests on a reused database.
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `VACUUM media_item_embeddings`)
	})
	cleanupRecoMediaItems(t, f.pool, f.prefix)
	return f
}

// seedEval writes the evaluation's catalog, its personas' signals and the
// co-watch pairs, and returns the catalog and the personas.
func seedEval(t *testing.T, f *relevanceFixture) (*evalCatalog, []*evalPersona) {
	t.Helper()
	c := newEvalCatalog(f.prefix)
	c.seed(t, f)
	personas := make([]*evalPersona, len(evalPersonaSpecs))
	for i, spec := range evalPersonaSpecs {
		personas[i] = newEvalPersona(t, f, c, spec)
	}
	seedEvalCowatch(t, f, c, personas[0].latest)
	return c, personas
}

// rebuildEvalTasteProfiles builds every persona's taste profile, so each
// persona's Similar Users row finds its peers. Peers are not measured: they
// share the focused persona's interest.
func rebuildEvalTasteProfiles(t *testing.T, f *relevanceFixture, personas []*evalPersona) {
	t.Helper()
	ctx := t.Context()
	started, err := f.repo.Now(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range personas {
		if _, err := f.engine.rebuildTasteProfile(ctx, p.userID, p.profileID, started); err != nil {
			t.Fatalf("%s: taste profile: %v", p.spec.name, err)
		}
	}
}

// TestRecommendationHoldoutEvalPostgres runs the holdout evaluation above. It
// fails when a held-out title reaches the engine's input, a row holds a title
// its persona engaged with before T or serves one twice, a measured persona
// lacks a row kind, or the focused persona's main row falls below
// evalFocusedMainRecallFloor. It takes about five seconds: a thousand
// embeddings to index and four full refreshes.
func TestRecommendationHoldoutEvalPostgres(t *testing.T) {
	f := newEvalFixture(t)
	ctx := t.Context()
	start := time.Now()
	c, personas := seedEval(t, f)
	seeded := time.Since(start)
	rebuildEvalTasteProfiles(t, f, personas)
	var results []evalResult
	for _, p := range personas {
		if p.spec.peer > 0 {
			continue
		}
		if err := f.worker.refreshProfile(ctx, nil, p.userID, p.profileID); err != nil {
			t.Fatalf("%s: refresh: %v", p.spec.name, err)
		}
		results = append(results, measureEvalPersona(t, f, c, p))
	}
	t.Logf("holdout evaluation (seed %s, refresh %s)\n%s", seeded.Round(time.Millisecond), (time.Since(start) - seeded).Round(time.Millisecond), evalReport(results))

	// Every measured persona builds every row kind; a refresh that stops
	// writing one would otherwise leave the scorecard short without failing.
	for _, r := range results {
		for _, kind := range evalRowKinds {
			if len(r.rows[kind]) == 0 {
				t.Errorf("%s: no %s row", r.name, kind)
			}
		}
	}
	if main := results[0].rows["main"]; len(main) != 1 || main[0].recall < evalFocusedMainRecallFloor {
		t.Errorf("%s: main row %+v, want Recall@20 of at least %.2f", results[0].name, main, evalFocusedMainRecallFloor)
	}
}

// statementLog records the statements a pool sends while it is on, each
// statement of a batch on its own, leaving out those that set up a new
// connection: how many connections a run opens varies.
type statementLog struct {
	on         atomic.Bool
	connecting sync.Map // *pgx.Conn in AfterConnect
	mu         sync.Mutex
	statements []string
}

// install makes l cfg's tracer.
func (l *statementLog) install(cfg *pgxpool.Config) {
	cfg.ConnConfig.Tracer = l
	setup := cfg.AfterConnect
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if setup == nil {
			return nil
		}
		l.connecting.Store(conn, true)
		defer l.connecting.Delete(conn)
		return setup(ctx, conn)
	}
}

func (l *statementLog) record(conn *pgx.Conn, sql string) {
	if _, ok := l.connecting.Load(conn); ok || !l.on.Load() {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.statements = append(l.statements, strings.Join(strings.Fields(sql), " "))
}

func (l *statementLog) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	l.record(conn, data.SQL)
	return ctx
}

func (*statementLog) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (*statementLog) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return ctx
}

func (l *statementLog) TraceBatchQuery(_ context.Context, conn *pgx.Conn, data pgx.TraceBatchQueryData) {
	l.record(conn, data.SQL)
}

func (*statementLog) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}

// evalFocusedRefreshStatements is how many statements a refresh of the
// evaluation's focused persona sends. The candidate scans of the main row's
// anchors, the cluster rows, the type supplements and Because You Watched
// return the details ranking reads (embedding, media type, added date and
// quality rating); reading them per pool instead cost 31 statements more.
// Similar Users reads each peer's liked ratings with one filtered query and
// keeps only its movie and series favorites, which costs one type check per
// peer (6 for this persona). A deliberate change must update this budget and
// its comment together.
const evalFocusedRefreshStatements = 119

// A profile refresh sends evalFocusedRefreshStatements statements for the
// evaluation's focused persona. Its rows are the evaluation's, the same on
// every run, so the count is too. It takes a few seconds: it seeds the
// evaluation's catalog.
func TestProfileRefreshStatementBudgetPostgres(t *testing.T) {
	f := newEvalFixture(t)
	ctx := t.Context()
	_, personas := seedEval(t, f)
	rebuildEvalTasteProfiles(t, f, personas)

	cfg := f.pool.Config()
	log := &statementLog{}
	log.install(cfg)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	counted := relevanceFixtureOn(pool, f.prefix)

	focused := personas[0]
	log.on.Store(true)
	err = counted.worker.refreshProfile(ctx, nil, focused.userID, focused.profileID)
	log.on.Store(false)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(log.statements); got != evalFocusedRefreshStatements {
		t.Fatalf("refresh sent %d statements, want %d:\n%s", got, evalFocusedRefreshStatements, strings.Join(log.statements, "\n"))
	}
}
