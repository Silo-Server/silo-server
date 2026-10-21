package recommendations

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// The relevance harness builds each persona's personal rows with the
// production row builder over a seeded toy catalog held in memory, and checks
// what the served rows contain. The catalog's vectors are structured so a
// genre is a direction: titles sharing a genre sit near each other, like real
// embeddings of text that leads with the genres. The checks are thresholds,
// never exact lists, so retuning the catalog or a constant moves numbers
// without breaking the intent.

// relevanceNow is the fixed time every persona's signals are dated from.
var relevanceNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

const (
	gThriller    = "Thriller"
	gSciFi       = "Science Fiction"
	gAction      = "Action"
	gHorror      = "Horror"
	gRomance     = "Romance"
	gComedy      = "Comedy"
	gDrama       = "Drama"
	gDocumentary = "Documentary"
	gAnimation   = "Animation"
	gFamily      = "Family"
	gCrime       = "Crime"
	gFantasy     = "Fantasy"
)

// toyGenres lists each genre with the genres its titles take as a secondary
// genre, so the catalog has blends such as horror-comedies.
var toyGenres = []struct {
	name    string
	related []string
}{
	{gThriller, []string{gCrime, gAction, gHorror, gSciFi, gDrama}},
	{gSciFi, []string{gAction, gThriller, gFantasy, gDrama}},
	{gAction, []string{gThriller, gSciFi, gCrime, gComedy}},
	{gHorror, []string{gThriller, gComedy, gFantasy}},
	{gRomance, []string{gComedy, gDrama}},
	{gComedy, []string{gRomance, gFamily, gAction, gHorror}},
	{gDrama, []string{gRomance, gCrime, gThriller}},
	{gDocumentary, []string{gDrama, gCrime}},
	{gAnimation, []string{gFamily, gComedy, gFantasy}},
	{gFamily, []string{gAnimation, gComedy, gFantasy}},
	{gCrime, []string{gThriller, gDrama, gAction}},
	{gFantasy, []string{gAction, gFamily, gSciFi}},
}

const (
	toyMoviesPerGenre = 60
	toySeriesPerGenre = 15
	toyNoiseDims      = 48
	// toyCommon, toySecondary, toyType and toyNoise shape the vectors:
	// titles sharing a primary genre and a type score a cosine near 0.6,
	// unrelated titles near 0.3, roughly what a small sentence-embedding
	// model gives. The embedding text names the type, so a movie sits a
	// little nearer other movies than series.
	toyCommon    = 0.8
	toySecondary = 0.55
	toyType      = 0.5
	toyNoise     = 1.0
)

type toyItem struct {
	id            string
	mediaType     string
	genres        []string
	vec           []float32
	rating        float64 // 0 when the title has no usable rating
	contentRating string
	addedAt       time.Time
}

type toyCatalog struct {
	items []toyItem
	byID  map[string]*toyItem
}

// newToyCatalog builds the same catalog on every run.
func newToyCatalog() *toyCatalog {
	rng := rand.New(rand.NewPCG(20261001, 7))
	const typeAxes = 2
	genreAxis := make(map[string]int, len(toyGenres))
	for i, g := range toyGenres {
		genreAxis[g.name] = 1 + typeAxes + i
	}
	noiseAxis := 1 + typeAxes + len(toyGenres)
	dims := noiseAxis + toyNoiseDims
	c := &toyCatalog{byID: map[string]*toyItem{}}
	add := func(id, mediaType string, primary, secondary string) {
		vec := make([]float64, dims)
		vec[0] = toyCommon
		if mediaType == "series" {
			vec[2] = toyType
		} else {
			vec[1] = toyType
		}
		vec[genreAxis[primary]] = 1
		genres := []string{primary}
		if secondary != "" {
			vec[genreAxis[secondary]] = toySecondary
			genres = append(genres, secondary)
		}
		var noiseNorm float64
		noise := make([]float64, toyNoiseDims)
		for d := range noise {
			noise[d] = rng.NormFloat64()
			noiseNorm += noise[d] * noise[d]
		}
		noiseNorm = math.Sqrt(noiseNorm)
		for d := range noise {
			vec[noiseAxis+d] = toyNoise * noise[d] / noiseNorm
		}
		out := make([]float32, dims)
		var norm float64
		for _, v := range vec {
			norm += v * v
		}
		norm = math.Sqrt(norm)
		for d, v := range vec {
			out[d] = float32(v / norm)
		}
		rating := 0.0
		if rng.IntN(10) != 0 {
			rating = 6.4 + rng.NormFloat64()
			if mediaType == "series" {
				// Series ratings run higher than movie ratings.
				rating += 0.6
			}
			rating = math.Round(min(9.3, max(2.0, rating))*10) / 10
		}
		contentRating := []string{"PG-13", "R"}[rng.IntN(2)]
		if primary == gAnimation || primary == gFamily {
			contentRating = "G"
			if rng.IntN(4) == 0 {
				contentRating = "PG"
			}
		}
		c.items = append(c.items, toyItem{
			id:            id,
			mediaType:     mediaType,
			genres:        genres,
			vec:           out,
			rating:        rating,
			contentRating: contentRating,
			addedAt:       relevanceNow.Add(-time.Duration(30+rng.IntN(700)) * 24 * time.Hour),
		})
	}
	for _, g := range toyGenres {
		slug := strings.ToLower(strings.ReplaceAll(g.name, " ", ""))
		for i := range toyMoviesPerGenre + toySeriesPerGenre {
			mediaType, n := "movie", i
			if i >= toyMoviesPerGenre {
				mediaType, n = "series", i-toyMoviesPerGenre
			}
			secondary := ""
			if rng.IntN(10) < 6 {
				secondary = g.related[rng.IntN(len(g.related))]
			}
			add(fmt.Sprintf("%s-%s-%02d", slug, mediaType, n), mediaType, g.name, secondary)
		}
	}
	for i := range c.items {
		c.byID[c.items[i].id] = &c.items[i]
	}
	return c
}

// pick returns the first n titles of mediaType whose primary genre is genre,
// skipping the first skip of them.
func (c *toyCatalog) pick(genre, mediaType string, skip, n int) []string {
	var ids []string
	for _, item := range c.items {
		if item.genres[0] != genre || item.mediaType != mediaType {
			continue
		}
		if skip > 0 {
			skip--
			continue
		}
		ids = append(ids, item.id)
		if len(ids) == n {
			break
		}
	}
	return ids
}

// toyStore is a rowStore over the toy catalog for one persona. Its candidate
// queries rank exactly, the way the SQL ranks with an exact scan.
type toyStore struct {
	catalog  *toyCatalog
	taste    []float32
	clusters []TasteCluster
	allowed  func(*toyItem) bool
	// candidateErrAt fails the candidateErrAt-th FindTasteProfileCandidates
	// call (1-based; 0 never), and embeddingsErr every GetBatchEmbeddings.
	candidateErrAt int
	candidateCalls int
	embeddingsErr  error
}

func (s *toyStore) GetItemQualityRatings(_ context.Context, ids []string) (map[string]float64, error) {
	out := make(map[string]float64, len(ids))
	for _, id := range ids {
		if item, ok := s.catalog.byID[id]; ok && item.rating > 0 {
			out[id] = item.rating
		}
	}
	return out, nil
}

func (s *toyStore) GetTasteProfile(context.Context, int, string) ([]float32, error) {
	return s.taste, nil
}

func (s *toyStore) GetTasteClusters(context.Context, int, string) ([]TasteCluster, error) {
	return s.clusters, nil
}

func (s *toyStore) FindTasteProfileCandidates(ctx context.Context, embedding []float32, excludeIDs []string, genres []string, limit int, filter catalog.AccessFilter) ([]ScoredItem, map[string][]string, error) {
	s.candidateCalls++
	if s.candidateCalls == s.candidateErrAt {
		return nil, nil, errors.New("candidate query failed")
	}
	return s.FindTasteProfileCandidatesByMediaType(ctx, embedding, excludeIDs, genres, limit, filter, "")
}

// FindTasteProfileCandidatesByMediaType mirrors findTasteProfileCandidates:
// the nearest titles first, and with genres the nearest limit×5 that share one
// of them, re-ordered by how many they share.
func (s *toyStore) FindTasteProfileCandidatesByMediaType(_ context.Context, embedding []float32, excludeIDs []string, genres []string, limit int, _ catalog.AccessFilter, mediaType string) ([]ScoredItem, map[string][]string, error) {
	if embedding == nil || limit <= 0 {
		return []ScoredItem{}, map[string][]string{}, nil
	}
	type hit struct {
		item    *toyItem
		sim     float64
		matches int
	}
	var hits []hit
	for i := range s.catalog.items {
		item := &s.catalog.items[i]
		if slices.Contains(excludeIDs, item.id) || (mediaType != "" && item.mediaType != mediaType) {
			continue
		}
		if s.allowed != nil && !s.allowed(item) {
			continue
		}
		matches := 0
		for _, g := range genres {
			if slices.Contains(item.genres, g) {
				matches++
			}
		}
		if len(genres) > 0 && matches == 0 {
			continue
		}
		hits = append(hits, hit{item, cosineSimilarity(embedding, item.vec), matches})
	}
	byDistance := func(a, b hit) int {
		if c := cmp.Compare(b.sim, a.sim); c != 0 {
			return c
		}
		return strings.Compare(a.item.id, b.item.id)
	}
	slices.SortFunc(hits, byDistance)
	annLimit := limit
	if len(genres) > 0 {
		annLimit = min(limit*5, 2000)
	}
	hits = hits[:min(len(hits), annLimit)]
	slices.SortStableFunc(hits, func(a, b hit) int {
		if c := cmp.Compare(b.matches, a.matches); c != 0 {
			return c
		}
		return byDistance(a, b)
	})
	hits = hits[:min(len(hits), limit)]
	items := make([]ScoredItem, len(hits))
	genreMap := make(map[string][]string, len(hits))
	for i, h := range hits {
		items[i] = ScoredItem{MediaItemID: h.item.id, Score: h.sim, Reason: "taste_profile_match"}
		genreMap[h.item.id] = h.item.genres
	}
	return items, genreMap, nil
}

func (s *toyStore) GetBatchEmbeddings(_ context.Context, ids []string) (map[string][]float32, error) {
	if s.embeddingsErr != nil {
		return nil, s.embeddingsErr
	}
	out := make(map[string][]float32, len(ids))
	for _, id := range ids {
		if item, ok := s.catalog.byID[id]; ok {
			out[id] = item.vec
		}
	}
	return out, nil
}

func (s *toyStore) GetItemMediaTypes(_ context.Context, ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if item, ok := s.catalog.byID[id]; ok {
			out[id] = item.mediaType
		}
	}
	return out, nil
}

func (s *toyStore) GetItemAddedDates(_ context.Context, ids []string) (map[string]time.Time, error) {
	out := make(map[string]time.Time, len(ids))
	for _, id := range ids {
		if item, ok := s.catalog.byID[id]; ok {
			out[id] = item.addedAt
		}
	}
	return out, nil
}

func (s *toyStore) PresentMediaTypes(_ context.Context, mediaTypes []string, _ catalog.AccessFilter) (map[string]struct{}, error) {
	present := map[string]struct{}{}
	for i := range s.catalog.items {
		item := &s.catalog.items[i]
		if slices.Contains(mediaTypes, item.mediaType) && (s.allowed == nil || s.allowed(item)) {
			present[item.mediaType] = struct{}{}
		}
	}
	return present, nil
}

// personaTitle is one title in a persona's history: its final signal weight
// is a completed watch daysAgo before relevanceNow, plus a 5-star rating when
// rated5 is set.
type personaTitle struct {
	id      string
	daysAgo int
	rated5  bool
}

func (p personaTitle) weight() float64 {
	at := relevanceNow.Add(-time.Duration(p.daysAgo) * 24 * time.Hour)
	decay := timeDecay(at, relevanceNow, 180)
	w := WeightWatchHigh * decay
	if p.rated5 {
		w += WeightRated5 * decay
	}
	return w
}

type persona struct {
	name    string
	history []personaTitle
	allowed func(*toyItem) bool
	// tastes are the genres the persona's interests are named by; the
	// summary counts the served items carrying each.
	tastes []string
}

// personaRows is what the harness measures for one persona.
type personaRows struct {
	persona  persona
	catalog  *toyCatalog
	store    *toyStore
	exclude  []string
	main     []ScoredItem
	clusters []ForYouRow
}

func titles(ids []string, daysAgo int) []personaTitle {
	out := make([]personaTitle, len(ids))
	for i, id := range ids {
		out[i] = personaTitle{id: id, daysAgo: daysAgo + i*5}
	}
	return out
}

// relevancePersonas builds personas A–F over c.
func relevancePersonas(c *toyCatalog) map[string]persona {
	a := append(titles(c.pick(gThriller, "movie", 0, 33), 5), titles(c.pick(gSciFi, "movie", 0, 18), 10)...)
	a = append(a, titles(c.pick(gAction, "movie", 0, 6), 40)...)
	a = append(a, titles(c.pick(gDrama, "movie", 0, 3), 90)...)
	for i := range 6 {
		a[i*7].rated5 = true
	}

	b := titles(c.pick(gComedy, "series", 0, 3), 700)
	b = append(b, titles(c.pick(gCrime, "series", 0, 2), 20)...)
	b = append(b, titles(c.pick(gDrama, "series", 0, 2), 60)...)
	b = append(b, titles(c.pick(gThriller, "series", 0, 1), 30)...)

	kid := append(titles(c.pick(gAnimation, "movie", 0, 12), 3), titles(c.pick(gFamily, "movie", 0, 6), 20)...)
	kid = append(kid, titles(c.pick(gAnimation, "series", 0, 2), 1)...)

	d := []personaTitle{
		{id: c.pick(gSciFi, "movie", 30, 1)[0], daysAgo: 1},
		{id: c.pick(gRomance, "movie", 30, 1)[0], daysAgo: 1},
		{id: c.pick(gDrama, "movie", 30, 1)[0], daysAgo: 1},
	}

	e := append(titles(c.pick(gHorror, "movie", 0, 26), 5), titles(c.pick(gRomance, "movie", 0, 24), 5)...)

	f := append(titles(c.pick(gCrime, "movie", 0, 20), 600), titles(c.pick(gThriller, "movie", 20, 20), 610)...)
	f = append(f, titles(c.pick(gDocumentary, "movie", 0, 2), 10)...)

	return map[string]persona{
		"A": {name: "A movie buff", history: a, tastes: []string{gThriller, gSciFi, gAction}},
		"B": {name: "B series binger", history: b, tastes: []string{gComedy, gCrime, gDrama, gThriller}},
		"C": {name: "C kid", history: kid, allowed: func(item *toyItem) bool { return item.contentRating == "G" }, tastes: []string{gAnimation, gFamily}},
		"D": {name: "D new profile", history: d, tastes: []string{gSciFi, gRomance, gDrama}},
		"E": {name: "E mixed taste", history: e, tastes: []string{gHorror, gRomance}},
		"F": {name: "F lapsed", history: f, tastes: []string{gCrime, gThriller, gDocumentary}},
	}
}

// buildPersonaRows builds the persona's taste profile and clusters the way
// RefreshTasteProfile does, then its main and cluster rows the way the cache
// build does.
func buildPersonaRows(t *testing.T, c *toyCatalog, p persona) personaRows {
	t.Helper()
	return buildPersonaRowsWith(t, c, p, func(*toyStore) {})
}

// buildPersonaRowsWith is buildPersonaRows with tweak applied to the store
// before the rows are built.
func buildPersonaRowsWith(t *testing.T, c *toyCatalog, p persona, tweak func(*toyStore)) personaRows {
	t.Helper()
	b, store, exclude := personaBuilder(t, c, p, tweak)
	ctx := t.Context()
	main, err := b.mainRow(ctx, 1, "p", CacheCandidateLimit, exclude, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("%s: main row: %v", p.name, err)
	}
	if main == nil {
		t.Fatalf("%s: no main row", p.name)
	}
	clusters, _, err := b.clusterRows(ctx, 1, "p", CacheCandidateLimit, exclude, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("%s: cluster rows: %v", p.name, err)
	}
	return personaRows{persona: p, catalog: c, store: store, exclude: exclude, main: main.Items, clusters: clusters}
}

// personaBuilder returns a row builder over a store holding the persona's
// taste profile and clusters, built the way RefreshTasteProfile builds them,
// with tweak applied, and the persona's titles, which every row leaves out.
func personaBuilder(t *testing.T, c *toyCatalog, p persona, tweak func(*toyStore)) (rowBuilder, *toyStore, []string) {
	t.Helper()
	vecs := make([][]float32, 0, len(p.history))
	weights := make([]float64, 0, len(p.history))
	items := make([]clusterItem, 0, len(p.history))
	exclude := make([]string, 0, len(p.history))
	for _, title := range p.history {
		item := c.byID[title.id]
		if item == nil {
			t.Fatalf("%s: unknown title %s", p.name, title.id)
		}
		vecs = append(vecs, item.vec)
		weights = append(weights, title.weight())
		items = append(items, clusterItem{itemID: item.id, embedding: item.vec, weight: title.weight(), genres: item.genres})
		exclude = append(exclude, item.id)
	}
	store := &toyStore{catalog: c, taste: weightedAverage(vecs, weights), clusters: buildTasteClusters(items), allowed: p.allowed}
	tweak(store)
	return rowBuilder{store: store, lambda: defaultMMRLambda, now: relevanceNow}, store, exclude
}

// served is the part of a cached row a public read serves.
func served(items []ScoredItem) []ScoredItem {
	return items[:min(len(items), ServedRowSize)]
}

// share is the fraction of items that carry genre.
func (r personaRows) share(items []ScoredItem, genre string) float64 {
	if len(items) == 0 {
		return 0
	}
	n := 0
	for _, item := range items {
		if slices.Contains(r.catalog.byID[item.MediaItemID].genres, genre) {
			n++
		}
	}
	return float64(n) / float64(len(items))
}

// tasteCount is how many items carry genre.
func (r personaRows) tasteCount(items []ScoredItem, genre string) int {
	return int(math.Round(r.share(items, genre) * float64(len(items))))
}

// anchorCounts returns, for each of the persona's main-row anchors, its share
// of the served window in slots and how many served items lie nearer it than
// any other anchor. A persona without anchors gets nil.
func (r personaRows) anchorCounts() (want, got []int) {
	anchors := mainRowAnchors(r.store.clusters)
	if len(anchors) == 0 {
		return nil, nil
	}
	weights := make([]float64, len(anchors))
	for i, a := range anchors {
		weights[i] = a.weight
	}
	want = allocateAnchorSlots(weights, ServedRowSize)
	got = make([]int, len(anchors))
	for _, item := range served(r.main) {
		best, bestSim := 0, -2.0
		for i, a := range anchors {
			if sim := cosineSimilarity(a.embedding, r.catalog.byID[item.MediaItemID].vec); sim > bestSim {
				best, bestSim = i, sim
			}
		}
		got[best]++
	}
	return want, got
}

func buildAllPersonaRows(t *testing.T) map[string]personaRows {
	t.Helper()
	c := newToyCatalog()
	out := map[string]personaRows{}
	for key, p := range relevancePersonas(c) {
		out[key] = buildPersonaRows(t, c, p)
	}
	return out
}

// A profile with several interests is served each in proportion: every
// anchor holds at least half its share of the served window, so a minor
// interest is not crowded out by the heaviest one.
func TestRelevanceComposedRowServesEveryInterest(t *testing.T) {
	composed := 0
	for key, r := range buildAllPersonaRows(t) {
		want, got := r.anchorCounts()
		if want == nil {
			continue
		}
		composed++
		for i := range want {
			if got[i] < want[i]/2 {
				t.Fatalf("%s: anchor %d is nearest %d served items, want at least half its %d slots (all anchors: want %v, got %v)", key, i, got[i], want[i], want, got)
			}
		}
	}
	if composed < 3 {
		t.Fatalf("%d personas have a composed main row, want at least 3", composed)
	}
}

// A profile whose interests weigh about 3:1 is served about 3:1: the composed
// row keeps the heavier interest's share of the served window rather than
// capping its genre at half.
func TestRelevanceComposedRowKeepsAHeavyInterestsShare(t *testing.T) {
	c := newToyCatalog()
	history := append(titles(c.pick(gHorror, "movie", 0, 12), 5), titles(c.pick(gRomance, "movie", 0, 6), 5)...)
	for i := range 4 {
		history[i].rated5 = true
	}
	r := buildPersonaRows(t, c, persona{name: "heavy horror", history: history, tastes: []string{gHorror, gRomance}})
	want, got := r.anchorCounts()
	if len(want) != 2 {
		t.Fatalf("anchors = %d, want horror and romance", len(want))
	}
	if got[0] < want[0]-1 {
		t.Fatalf("horror anchor is nearest %d served items, want its %d slots (want %v, got %v)", got[0], want[0], want, got)
	}
	if n := r.tasteCount(served(r.main), gHorror); n <= ServedRowSize/2 {
		t.Fatalf("%d of the served %d carry %s, want more than half", n, ServedRowSize, gHorror)
	}
}

// A main row missing an interest is not built: a failed anchor query or
// embeddings read fails the row rather than letting the other interests take
// its slots, and a cluster whose embeddings cannot be read counts as failed
// and gets no row, so the cached rows stay.
func TestPersonalRowsFailRatherThanThin(t *testing.T) {
	c := newToyCatalog()
	p := relevancePersonas(c)["E"]
	b, store, exclude := personaBuilder(t, c, p, func(s *toyStore) { s.candidateErrAt = 2 })
	if anchors := mainRowAnchors(store.clusters); len(anchors) < 2 {
		t.Fatalf("persona E has %d anchors, want a composed row", len(anchors))
	}
	// The second anchor's query fails.
	if row, err := b.mainRow(t.Context(), 1, "p", CacheCandidateLimit, exclude, catalog.AccessFilter{}); err == nil {
		t.Fatalf("main row = %d items, want an error for the failed anchor", len(row.Items))
	}

	b, store, exclude = personaBuilder(t, c, p, func(s *toyStore) { s.embeddingsErr = errors.New("embeddings down") })
	if _, err := b.mainRow(t.Context(), 1, "p", CacheCandidateLimit, exclude, catalog.AccessFilter{}); err == nil {
		t.Fatal("main row built without embeddings, want an error")
	}
	rows, failed, err := b.clusterRows(t.Context(), 1, "p", CacheCandidateLimit, exclude, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 || failed != len(store.clusters) {
		t.Fatalf("cluster rows = %d, failed = %d; want none built and all %d failed", len(rows), failed, len(store.clusters))
	}
}
