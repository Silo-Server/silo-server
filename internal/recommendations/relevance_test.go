package recommendations

import (
	"cmp"
	"context"
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
	// typeQueries counts the per-type candidate queries by media type;
	// noTypeQueries makes them return nothing.
	typeQueries   map[string]int
	noTypeQueries bool
	// noRatings hides every title's quality rating.
	noRatings bool
}

func (s *toyStore) GetItemQualityRatings(_ context.Context, ids []string) (map[string]float64, error) {
	out := make(map[string]float64, len(ids))
	if s.noRatings {
		return out, nil
	}
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
	return s.FindTasteProfileCandidatesByMediaType(ctx, embedding, excludeIDs, genres, limit, filter, "")
}

// FindTasteProfileCandidatesByMediaType mirrors findTasteProfileCandidates:
// the nearest titles first, and with genres the nearest limit×5 that share one
// of them, re-ordered by how many they share.
func (s *toyStore) FindTasteProfileCandidatesByMediaType(_ context.Context, embedding []float32, excludeIDs []string, genres []string, limit int, _ catalog.AccessFilter, mediaType string) ([]ScoredItem, map[string][]string, error) {
	if mediaType != "" {
		if s.typeQueries == nil {
			s.typeQueries = map[string]int{}
		}
		s.typeQueries[mediaType]++
		if s.noTypeQueries {
			return []ScoredItem{}, map[string][]string{}, nil
		}
	}
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
	// rawTop holds the nearest titles to the taste vector, the row a plain
	// nearest-neighbor query would serve.
	rawTop []ScoredItem
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
	b := rowBuilder{store: store, lambda: defaultMMRLambda, now: relevanceNow}
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
	rawTop, _, _ := store.FindTasteProfileCandidates(ctx, store.taste, exclude, nil, ServedRowSize, catalog.AccessFilter{})
	return personaRows{persona: p, catalog: c, store: store, exclude: exclude, main: main.Items, clusters: clusters, rawTop: rawTop}
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

// historyShare is the fraction of the persona's titles that carry genre.
func (r personaRows) historyShare(genre string) float64 {
	n := 0
	for _, title := range r.persona.history {
		if slices.Contains(r.catalog.byID[title.id].genres, genre) {
			n++
		}
	}
	return float64(n) / float64(len(r.persona.history))
}

// topHistoryGenre is the genre most of the persona's titles carry.
func (r personaRows) topHistoryGenre() string {
	best, bestShare := "", -1.0
	for _, g := range toyGenres {
		if s := r.historyShare(g.name); s > bestShare {
			best, bestShare = g.name, s
		}
	}
	return best
}

// amplification is how much more of the served row than of the history the
// persona's top genre takes.
func (r personaRows) amplification() float64 {
	g := r.topHistoryGenre()
	return r.share(served(r.main), g) - r.historyShare(g)
}

// fit is the mean, over items, of each item's highest cosine to a title in
// the persona's history.
func (r personaRows) fit(items []ScoredItem) float64 {
	if len(items) == 0 {
		return 0
	}
	var sum float64
	for _, item := range items {
		best := -1.0
		for _, title := range r.persona.history {
			best = max(best, cosineSimilarity(r.catalog.byID[item.MediaItemID].vec, r.catalog.byID[title.id].vec))
		}
		sum += best
	}
	return sum / float64(len(items))
}

// meanRating is the mean rating of the rated items.
func (r personaRows) meanRating(items []ScoredItem) float64 {
	var sum float64
	n := 0
	for _, item := range items {
		if rating := r.catalog.byID[item.MediaItemID].rating; rating > 0 {
			sum += rating
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// meanTasteCosine is the mean cosine of items to the taste vector.
func (r personaRows) meanTasteCosine(items []ScoredItem) float64 {
	if len(items) == 0 {
		return 0
	}
	var sum float64
	for _, item := range items {
		sum += cosineSimilarity(r.store.taste, r.catalog.byID[item.MediaItemID].vec)
	}
	return sum / float64(len(items))
}

// count is how many items have mediaType.
func (r personaRows) count(items []ScoredItem, mediaType string) int {
	n := 0
	for _, item := range items {
		if r.catalog.byID[item.MediaItemID].mediaType == mediaType {
			n++
		}
	}
	return n
}

// rawOverlap is the fraction of the served row that the plain
// nearest-neighbor row would also serve.
func (r personaRows) rawOverlap() float64 {
	raw := make(map[string]struct{}, len(r.rawTop))
	for _, item := range r.rawTop {
		raw[item.MediaItemID] = struct{}{}
	}
	s := served(r.main)
	if len(s) == 0 {
		return 0
	}
	n := 0
	for _, item := range s {
		if _, ok := raw[item.MediaItemID]; ok {
			n++
		}
	}
	return float64(n) / float64(len(s))
}

// tasteCount is how many items carry genre.
func (r personaRows) tasteCount(items []ScoredItem, genre string) int {
	return int(math.Round(r.share(items, genre) * float64(len(items))))
}

func (r personaRows) summary() string {
	s := served(r.main)
	top := r.topHistoryGenre()
	tastes := make([]string, len(r.persona.tastes))
	for i, g := range r.persona.tastes {
		tastes[i] = fmt.Sprintf("%s=%d", g, r.tasteCount(s, g))
	}
	return fmt.Sprintf("%s: row=%d clusters=%d %s served=%.2f hist=%.2f amp=%+.2f raw-overlap=%.2f fit=%.3f cos=%.3f rating=%.2f series(20/60)=%d/%d served20[%s]",
		r.persona.name, len(r.main), len(r.store.clusters), top, r.share(s, top), r.historyShare(top), r.amplification(),
		r.rawOverlap(), r.fit(s), r.meanTasteCosine(s), r.meanRating(s), r.count(s, "series"), r.count(r.main, "series"), strings.Join(tastes, " "))
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

// TestRelevancePersonaReport logs what each persona is served, for comparing
// a change's effect (go test -run TestRelevancePersonaReport -v).
func TestRelevancePersonaReport(t *testing.T) {
	rows := buildAllPersonaRows(t)
	for _, key := range []string{"A", "B", "C", "D", "E", "F"} {
		r := rows[key]
		t.Log(r.summary())
		want, got := r.anchorCounts()
		var clusters []string
		for _, c := range r.store.clusters {
			clusters = append(clusters, fmt.Sprintf("%s(%d titles, w=%.2f)", c.Label, c.MemberCount, c.TotalWeight))
		}
		window := map[string]struct{}{}
		for _, item := range served(r.main) {
			window[item.MediaItemID] = struct{}{}
		}
		var overlaps, lengths []int
		for _, row := range r.clusters {
			n := 0
			for _, item := range served(row.Items) {
				if _, ok := window[item.MediaItemID]; ok {
					n++
				}
			}
			overlaps = append(overlaps, n)
		}
		for _, row := range withoutMainRowItems(slices.Clone(r.clusters), r.main) {
			lengths = append(lengths, len(row.Items))
		}
		t.Logf("    clusters %s; anchor slots of 20 %v, served nearest each %v; cluster rows' served 20 repeating the main row %v, cached lengths after removal %v",
			strings.Join(clusters, "; "), want, got, overlaps, lengths)
		week := map[string]struct{}{}
		for d := range 7 {
			for _, item := range r.page(t, relevanceNow.AddDate(0, 0, d))[0].Items {
				week[item.MediaItemID] = struct{}{}
			}
		}
		t.Logf("    page %q; main row titles served over 7 days %d", rowLabels(r.page(t, relevanceNow)), len(week))
	}
}

// Every persona gets a main row of distinct titles it can see and has not
// watched, and C's restricted row holds only titles its scope admits.
func TestRelevanceRowsHoldOnlyRecommendableTitles(t *testing.T) {
	for key, r := range buildAllPersonaRows(t) {
		watched := map[string]struct{}{}
		for _, title := range r.persona.history {
			watched[title.id] = struct{}{}
		}
		rows := append([]ForYouRow{{Label: "main", Items: r.main}}, r.clusters...)
		for _, row := range rows {
			seen := map[string]struct{}{}
			for _, item := range row.Items {
				if _, dup := seen[item.MediaItemID]; dup {
					t.Fatalf("%s: %s row repeats %s", key, row.Label, item.MediaItemID)
				}
				seen[item.MediaItemID] = struct{}{}
				if _, ok := watched[item.MediaItemID]; ok {
					t.Fatalf("%s: %s row recommends watched title %s", key, row.Label, item.MediaItemID)
				}
				if r.persona.allowed != nil && !r.persona.allowed(r.catalog.byID[item.MediaItemID]) {
					t.Fatalf("%s: %s row holds %s, outside the persona's scope", key, row.Label, item.MediaItemID)
				}
			}
		}
		if len(served(r.main)) < ServedRowSize {
			t.Fatalf("%s: served main row has %d items, want %d", key, len(served(r.main)), ServedRowSize)
		}
	}
}

// A profile under ten titles has one cluster; the k-means profiles have
// several.
func TestRelevancePersonaClusterCounts(t *testing.T) {
	rows := buildAllPersonaRows(t)
	for _, key := range []string{"B", "D"} {
		if n := len(rows[key].store.clusters); n != 1 {
			t.Fatalf("%s: %d clusters, want 1 under ten titles", key, n)
		}
	}
	for _, key := range []string{"A", "E", "F"} {
		if n := len(rows[key].store.clusters); n < 2 {
			t.Fatalf("%s: %d clusters, want several", key, n)
		}
	}
}

// The main row stays near the profile's taste: its served titles are, on
// average, closer to the persona's own titles than the catalog is.
func TestRelevanceMainRowFitsTheHistory(t *testing.T) {
	for key, r := range buildAllPersonaRows(t) {
		var all []ScoredItem
		for _, item := range r.catalog.items {
			all = append(all, ScoredItem{MediaItemID: item.id})
		}
		if got, base := r.fit(served(r.main)), r.fit(all); got < base+0.1 {
			t.Fatalf("%s: served fit %.3f, want at least 0.1 above the catalog's %.3f", key, got, base)
		}
	}
}

// No title leaves a cached row: every main row keeps all CacheCandidateLimit
// places, the served window first and the headroom after it.
func TestRelevanceMainRowKeepsItsLength(t *testing.T) {
	for key, r := range buildAllPersonaRows(t) {
		if len(r.main) != CacheCandidateLimit {
			t.Fatalf("%s: main row has %d items, want %d", key, len(r.main), CacheCandidateLimit)
		}
	}
}

// The served window no longer amplifies a movie buff's top genre: its share
// of the served 20 stays within 0.20 of its share of the history.
func TestRelevanceServedWindowLimitsGenreAmplification(t *testing.T) {
	r := buildAllPersonaRows(t)["A"]
	if amp := r.amplification(); amp > 0.20 {
		t.Fatalf("A: %s amplification %+.2f, want at most +0.20", r.topHistoryGenre(), amp)
	}
}

// The genre pass reorders only the served window. Past it, a row built from
// the averaged taste vector keeps the order MMR chose; neither the pass nor
// the type supplements re-sort it by score.
func TestRelevanceRowKeepsMMROrderAfterTheWindow(t *testing.T) {
	for key, r := range buildAllPersonaRows(t) {
		if mainRowAnchors(r.store.clusters) != nil {
			continue
		}
		ctx := t.Context()
		pool, _, _ := r.store.FindTasteProfileCandidates(ctx, r.store.taste, r.exclude, nil, CacheCandidateLimit*3, catalog.AccessFilter{})
		pool = rowBuilder{store: r.store}.withQualityPrior(ctx, pool, 0)
		emb, _ := r.store.GetBatchEmbeddings(ctx, scoredItemIDs(pool))
		position := map[string]int{}
		for i, item := range applyMMR(pool, emb, defaultMMRLambda, CacheCandidateLimit) {
			position[item.MediaItemID] = i
		}
		last := -1
		for _, item := range r.main[ServedRowSize:] {
			at, ok := position[item.MediaItemID]
			if !ok {
				// A type supplement: it comes after every MMR pick.
				last = len(position)
				continue
			}
			if at < last {
				t.Fatalf("%s: %s at MMR position %d follows position %d after the window", key, item.MediaItemID, at, last)
			}
			last = at
		}
	}
}

// Library sections scope the whole cached row to one type, so each row holds
// at least mediaTypeFloor of movies and of series. The extra titles sit after
// the served window: the window is the same with the per-type queries
// switched off.
func TestRelevanceTypeSupplementsStayInTheTail(t *testing.T) {
	c := newToyCatalog()
	floor := mediaTypeFloor(CacheCandidateLimit)
	for key, p := range relevancePersonas(c) {
		r := buildPersonaRows(t, c, p)
		for _, mediaType := range []string{"movie", "series"} {
			if n := r.count(r.main, mediaType); n < floor {
				t.Fatalf("%s: main row has %d %s items, want at least %d", key, n, mediaType, floor)
			}
		}
		bare := buildPersonaRowsWith(t, c, p, func(s *toyStore) { s.noTypeQueries = true })
		if got, want := mmrItemIDs(served(r.main)), mmrItemIDs(served(bare.main)); !slices.Equal(got, want) {
			t.Fatalf("%s: served window %v changes without type supplements to %v", key, got, want)
		}
	}
}

// A media type the profile has no titles of is never queried for
// supplements: a movies-only scope has no series. A type the scope has but
// the candidates lack is queried. Books are outside recommendableMediaTypes,
// so they are never queried.
func TestRelevanceTypeSupplementsSkipAbsentTypes(t *testing.T) {
	c := newToyCatalog()
	scoped := func(allowed func(*toyItem) bool) map[string]int {
		p := relevancePersonas(c)["A"]
		p.allowed = allowed
		return buildPersonaRows(t, c, p).store.typeQueries
	}

	fewSeries := scoped(func(item *toyItem) bool { return item.mediaType == "movie" || item.genres[0] == gDocumentary })
	if fewSeries["series"] == 0 {
		t.Fatalf("type queries %v, want a series query when the candidates hold few series", fewSeries)
	}
	for _, queries := range []map[string]int{fewSeries, scoped(func(item *toyItem) bool { return item.mediaType == "movie" })} {
		for _, absent := range []string{"audiobook", "ebook"} {
			if queries[absent] != 0 {
				t.Fatalf("type queries %v, want none for %s", queries, absent)
			}
		}
	}
	if queries := scoped(func(item *toyItem) bool { return item.mediaType == "movie" }); queries["series"] != 0 {
		t.Fatalf("type queries %v, want no series query for a movies-only scope", queries)
	}
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

// A profile split between two tastes gets both: each holds at least 40% of
// the served window.
func TestRelevanceMixedTasteServesBothInterests(t *testing.T) {
	r := buildAllPersonaRows(t)["E"]
	for _, genre := range []string{gHorror, gRomance} {
		if n := r.tasteCount(served(r.main), genre); n < ServedRowSize*2/5 {
			t.Fatalf("E: %d of the served %d carry %s, want at least 40%%", n, ServedRowSize, genre)
		}
	}
}

// Cached cluster rows leave out the main row's served window, so the page
// does not repeat its opening titles; a row left thin is emptied.
func TestRelevanceClusterRowsLeaveOutTheServedMainRow(t *testing.T) {
	for key, r := range buildAllPersonaRows(t) {
		window := map[string]struct{}{}
		for _, item := range served(r.main) {
			window[item.MediaItemID] = struct{}{}
		}
		for i, row := range withoutMainRowItems(slices.Clone(r.clusters), r.main) {
			if n := len(row.Items); n > 0 && n < minClusterRowItems && n != len(r.clusters[i].Items) {
				t.Fatalf("%s: cluster row %d keeps %d items, want at least %d or none", key, row.ClusterIndex, n, minClusterRowItems)
			}
			for _, item := range row.Items {
				if _, ok := window[item.MediaItemID]; ok {
					t.Fatalf("%s: cluster row %d repeats served main-row title %s", key, row.ClusterIndex, item.MediaItemID)
				}
			}
		}
	}
}

// The quality prior lifts the served titles' mean rating, while their fit to
// the persona's titles and their cosine to its taste barely move. Small
// cluster pools are left alone.
func TestRelevanceQualityPriorLiftsRatingsAtLittleCost(t *testing.T) {
	c := newToyCatalog()
	var gain float64
	personas := relevancePersonas(c)
	for _, key := range []string{"A", "B", "C", "D", "E", "F"} {
		p := personas[key]
		with := buildPersonaRows(t, c, p)
		without := buildPersonaRowsWith(t, c, p, func(s *toyStore) { s.noRatings = true })
		s, base := served(with.main), served(without.main)
		delta := with.meanRating(s) - without.meanRating(base)
		if delta < 0 {
			t.Fatalf("%s: mean served rating fell %.2f -> %.2f", key, without.meanRating(base), with.meanRating(s))
		}
		if drop := without.fit(base) - with.fit(s); drop > 0.02 {
			t.Fatalf("%s: fit fell %.3f, want at most 0.02", key, drop)
		}
		if drop := without.meanTasteCosine(base) - with.meanTasteCosine(s); drop > 0.03 {
			t.Fatalf("%s: taste cosine fell %.3f, want at most 0.03", key, drop)
		}
		gain += delta / 6
	}
	if gain < 0.3 {
		t.Fatalf("mean served rating rose %.2f across personas, want at least 0.3", gain)
	}

	ratings, types := map[string]float64{}, map[string]string{}
	small := qualityPool("m", "movie", qualityMinSmallPool-1, func(i int) float64 { return float64(3 + i%6) }, ratings, types)
	b := rowBuilder{store: &toyStore{catalog: &toyCatalog{byID: map[string]*toyItem{}}}}
	if got := b.withQualityPrior(t.Context(), small, qualityMinSmallPool); !slices.Equal(got, small) {
		t.Fatalf("a pool under %d candidates changed", qualityMinSmallPool)
	}
}

// reader serves the persona's rows as the cache holds them, the cluster rows
// less the main row's served window, on day.
func (r personaRows) reader(day time.Time) *Reader {
	personal := map[string][]ScoredItem{RecTypeForYouMain + "|": r.main}
	for _, row := range withoutMainRowItems(slices.Clone(r.clusters), r.main) {
		personal[RecTypeForYouClusterPrefix+itoa(row.ClusterIndex)+"|"] = row.Items
	}
	return &Reader{
		repo: &fakeReaderRepo{
			meta:     &TasteProfileMeta{SignalCounts: map[string]int{signalCountPositiveTitles: len(r.persona.history)}},
			clusters: r.store.clusters,
			personal: personal,
		},
		signals: NewSignalReader(&fakeSignalRepo{}, nil),
		now:     func() time.Time { return day },
	}
}

// page reads the persona's For You page on day, ServedRowSize items a row.
func (r personaRows) page(t *testing.T, day time.Time) []ForYouRow {
	t.Helper()
	rows, err := r.reader(day).GetForYouPage(t.Context(), 1, "p", ServedRowSize, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("%s: page: %v", r.persona.name, err)
	}
	return rows
}

// labelFit is the share of items carrying at least one genre label names.
func (r personaRows) labelFit(items []ScoredItem, label string) float64 {
	named := strings.Split(label, clusterLabelSeparator)
	if len(items) == 0 {
		return 0
	}
	n := 0
	for _, item := range items {
		if slices.ContainsFunc(named, func(g string) bool { return slices.Contains(r.catalog.byID[item.MediaItemID].genres, g) }) {
			n++
		}
	}
	return float64(n) / float64(len(items))
}

// Cluster rows say what they hold: at least 60% of a row's served titles
// carry a genre its title names, and no title names more than two genres
// unless it was extended to tell two rows apart.
func TestRelevanceClusterTitlesDescribeTheirRows(t *testing.T) {
	for key, r := range buildAllPersonaRows(t) {
		for _, row := range r.page(t, relevanceNow) {
			if row.personalKey == RecTypeForYouMain || row.Type != "cluster" || row.Subject == "" {
				continue
			}
			if n := len(strings.Split(row.Subject, clusterLabelSeparator)); n > labelMaxGenres+1 {
				t.Fatalf("%s: %q names %d genres", key, row.Label, n)
			}
			if fit := r.labelFit(row.Items, row.Subject); fit < 0.6 {
				t.Fatalf("%s: %.0f%% of %q's served titles carry a genre it names, want at least 60%%", key, 100*fit, row.Label)
			}
		}
	}
}

// No persona's page shows two rows under one title; the kid's two Family
// clusters are told apart or one is hidden.
func TestRelevancePagesNeverRepeatATitle(t *testing.T) {
	for key, r := range buildAllPersonaRows(t) {
		labels := rowLabels(r.page(t, relevanceNow))
		for i, label := range labels {
			if slices.Contains(labels[:i], label) {
				t.Fatalf("%s: page %v repeats %q", key, labels, label)
			}
		}
	}
}

// The personal rows keep their best-matched head and refresh their tail
// daily: the same all day, the first ten fixed, at least two of the served
// 20 new each day, and more titles over a week than an unrotated row.
func TestRelevanceRotationKeepsTheHeadAndRefreshesTheTail(t *testing.T) {
	for _, key := range []string{"A", "E", "F"} {
		r := buildAllPersonaRows(t)[key]
		mainOn := func(day time.Time) []string { return scoredIDs(r.page(t, day)[0].Items) }
		today := mainOn(relevanceNow)
		if !slices.Equal(today, mainOn(relevanceNow.Add(6*time.Hour))) {
			t.Fatalf("%s: the main row changed within the day", key)
		}
		week := map[string]struct{}{}
		for d := range 7 {
			day := mainOn(relevanceNow.AddDate(0, 0, d))
			if !slices.Equal(day[:rotationMaxPinned], scoredIDs(served(r.main)[:rotationMaxPinned])) {
				t.Fatalf("%s: day %d moved the head", key, d)
			}
			if d > 0 {
				if n := ServedRowSize - len(intersect(day, mainOn(relevanceNow.AddDate(0, 0, d-1)))); n < 2 {
					t.Fatalf("%s: day %d changed %d served titles, want at least 2", key, d, n)
				}
			}
			for _, id := range day {
				week[id] = struct{}{}
			}
		}
		if len(week) < 25 {
			t.Fatalf("%s: %d distinct titles served in a week, want at least 25", key, len(week))
		}
	}
}

func intersect(a, b []string) []string {
	var out []string
	for _, id := range a {
		if slices.Contains(b, id) {
			out = append(out, id)
		}
	}
	return out
}

// A few new titles in the catalog reach the served rows without displacing
// the best-matched head; a freshly imported catalog, every title new, gets
// exactly the rows it would get with none.
func TestRelevanceFreshnessAddsNewTitlesWithoutDisplacingTheHead(t *testing.T) {
	newCatalog := func(isNew func(i int) bool) (*toyCatalog, map[string]bool) {
		c := newToyCatalog()
		ids := map[string]bool{}
		for i := range c.items {
			if isNew(i) {
				c.items[i].addedAt = relevanceNow.Add(-time.Duration(1+i%5) * 24 * time.Hour)
				ids[c.items[i].id] = true
			}
		}
		return c, ids
	}
	old := newToyCatalog()
	// One title in 60 is new, about a week's additions to a large library.
	fresh, newIDs := newCatalog(func(i int) bool { return i%60 == 7 })
	imported, _ := newCatalog(func(int) bool { return true })
	countNew := func(items []ScoredItem) int {
		n := 0
		for _, item := range items {
			if newIDs[item.MediaItemID] {
				n++
			}
		}
		return n
	}
	gained := 0
	for _, key := range []string{"A", "B", "C", "D", "E", "F"} {
		before := buildPersonaRows(t, old, relevancePersonas(old)[key])
		after := buildPersonaRows(t, fresh, relevancePersonas(fresh)[key])
		gained += countNew(served(after.main)) - countNew(served(before.main))
		if kept := len(intersect(scoredIDs(served(before.main)[:rotationMaxPinned]), scoredIDs(served(after.main)))); kept < rotationMaxPinned-1 {
			t.Fatalf("%s: %d of the head's %d titles still served, want at least %d", key, kept, rotationMaxPinned, rotationMaxPinned-1)
		}
		bulk := buildPersonaRows(t, imported, relevancePersonas(imported)[key])
		if !slices.Equal(scoredIDs(bulk.main), scoredIDs(before.main)) {
			t.Fatalf("%s: a freshly imported catalog changes the main row", key)
		}
	}
	if gained < 2 {
		t.Fatalf("new titles in the served main rows rose by %d across personas, want at least 2", gained)
	}
}
