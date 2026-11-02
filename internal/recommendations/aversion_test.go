package recommendations

import (
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// avertedRows builds persona p's main and cluster rows the way the cache
// build does, with turnedAway added to the titles every row leaves out and
// ranked against, and near ranked against as turned-away titles that are not
// in the catalog. Every turned-away title has full weight. With neither, the
// rows are ranked against an empty aversion.
func avertedRows(t *testing.T, c *toyCatalog, p persona, turnedAway []string, near ...[]float32) personaRows {
	t.Helper()
	b, store, exclude := personaBuilder(t, c, p, func(*toyStore) {})
	exclude = append(exclude, turnedAway...)
	av := &aversion{}
	for _, id := range turnedAway {
		av.titles = append(av.titles, aversionTitle{embedding: c.byID[id].vec, weight: 1})
	}
	for _, emb := range near {
		av.titles = append(av.titles, aversionTitle{embedding: emb, weight: 1})
	}
	b.aversion = av
	return buildRows(t, b, store, p, c, exclude)
}

func buildRows(t *testing.T, b rowBuilder, store *toyStore, p persona, c *toyCatalog, exclude []string) personaRows {
	t.Helper()
	ctx := t.Context()
	main, err := b.mainRow(ctx, 1, "p", CacheCandidateLimit, exclude, catalog.AccessFilter{})
	if err != nil || main == nil {
		t.Fatalf("%s: main row: %v, %v", p.name, main, err)
	}
	clusters, _, err := b.clusterRows(ctx, 1, "p", CacheCandidateLimit, exclude, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("%s: cluster rows: %v", p.name, err)
	}
	return personaRows{persona: p, catalog: c, store: store, exclude: exclude, main: main.Items, clusters: clusters}
}

// nearCopy returns vec nudged by a little noise and renormalized: a sequel
// or remake of the title, as an embedding model sees it.
func nearCopy(vec []float32, seed uint64) []float32 {
	rng := rand.New(rand.NewPCG(seed, 1))
	out := make([]float32, len(vec))
	var norm float64
	for i, v := range vec {
		x := float64(v) + 0.03*rng.NormFloat64()
		out[i] = float32(x)
		norm += x * x
	}
	norm = math.Sqrt(norm)
	for i := range out {
		out[i] = float32(float64(out[i]) / norm)
	}
	return out
}

// A profile that turned nothing away gets exactly the rows it got before
// turned-away titles were ranked against.
func TestAversionWithoutTitlesChangesNothing(t *testing.T) {
	c := newToyCatalog()
	for key, p := range relevancePersonas(c) {
		before := buildPersonaRows(t, c, p)
		after := avertedRows(t, c, p, nil)
		if !reflect.DeepEqual(before.main, after.main) || !reflect.DeepEqual(before.clusters, after.clusters) {
			t.Fatalf("persona %s: rows changed with no turned-away titles", key)
		}
	}
}

// One mark sends a near copy of the title, a sequel, out of the served
// window. Its closest neighbors may trade places with others of their genre,
// but the window's genre mix moves by at most one title a genre, and the row
// keeps its length.
func TestAversionPushesANearCopyOutOfTheServedWindow(t *testing.T) {
	c := newToyCatalog()
	a := relevancePersonas(c)["A"]
	before := buildPersonaRows(t, c, a)
	window := served(before.main)
	for i, item := range window[:5] {
		sequelOf := item.MediaItemID
		after := avertedRows(t, c, a, nil, nearCopy(c.byID[sequelOf].vec, uint64(i+1)))
		got := served(after.main)
		if slices.Contains(scoredItemIDs(got), sequelOf) {
			t.Fatalf("a near copy of %s was marked, but it is still served", sequelOf)
		}
		for _, g := range toyGenres {
			if was, now := before.tasteCount(window, g.name), after.tasteCount(got, g.name); now < was-1 || now > was+1 {
				t.Fatalf("marking a near copy of %s moved %s from %d to %d served titles", sequelOf, g.name, was, now)
			}
		}
		if len(after.main) != len(before.main) {
			t.Fatalf("marking a near copy of %s left a row of %d, was %d", sequelOf, len(after.main), len(before.main))
		}
	}
}

// Marks at the heart of a small interest drop its own row and its share of
// the main row; a large interest the profile mostly kept watching keeps its
// row through as many marks.
func TestAversionDropsAnInterestTurnedAway(t *testing.T) {
	c := newToyCatalog()
	a := relevancePersonas(c)["A"]
	before := buildPersonaRows(t, c, a)
	smallest, largest := -1, -1
	for i, cl := range before.store.clusters {
		if smallest < 0 || cl.TotalWeight < before.store.clusters[smallest].TotalWeight {
			smallest = i
		}
		if largest < 0 || cl.TotalWeight > before.store.clusters[largest].TotalWeight {
			largest = i
		}
	}
	rowOf := func(r personaRows, idx int) []ScoredItem {
		for _, row := range r.clusters {
			if row.ClusterIndex == before.store.clusters[idx].ClusterIdx {
				return row.Items
			}
		}
		t.Fatalf("no row for cluster %d", idx)
		return nil
	}
	small := scoredItemIDs(rowOf(before, smallest))[:4]
	after := avertedRows(t, c, a, small)
	if got := rowOf(after, smallest); len(got) != 0 {
		t.Fatalf("four marks at the heart of the smallest interest (weight %.1f) left its row with %d items",
			before.store.clusters[smallest].TotalWeight, len(got))
	}
	if len(after.main) != CacheCandidateLimit {
		t.Fatalf("main row of %d, want %d", len(after.main), CacheCandidateLimit)
	}

	large := scoredItemIDs(rowOf(before, largest))[:4]
	if got := rowOf(avertedRows(t, c, a, large), largest); len(got) == 0 {
		t.Fatalf("four marks dropped the largest interest's row (weight %.1f)", before.store.clusters[largest].TotalWeight)
	}
}

// clusterRowIDs returns the item IDs of persona p's cluster rows, by cluster
// index, as built with no turned-away titles.
func clusterRowIDs(r personaRows) map[int][]string {
	out := map[int][]string{}
	for _, row := range r.clusters {
		out[row.ClusterIndex] = scoredItemIDs(row.Items)
	}
	return out
}

// When marks drop a dominant interest, the small interest left fetches again
// for the slots it gained: the main row keeps its length rather than ending
// where the small interest's first, small fetch ran out.
func TestAversionRefillsTheMainRowAfterDroppingAnInterest(t *testing.T) {
	c := newToyCatalog()
	a := relevancePersonas(c)["A"]
	before := buildPersonaRows(t, c, a)
	clusters := clustersHeaviestFirst(before.store.clusters)
	heavy, light := clusters[0], clusters[len(clusters)-1]
	anchors := []mainRowAnchor{
		{embedding: heavy.Embedding, weight: 9},
		{embedding: light.Embedding, weight: 1},
	}
	if slots := allocateAnchorSlots([]float64{9, 1}, CacheCandidateLimit); anchorPoolFactor*slots[1] >= CacheCandidateLimit {
		t.Fatalf("slots %v leave the light anchor a full row of candidates; the test needs it short", slots)
	}
	b, _, exclude := personaBuilder(t, c, a, func(*toyStore) {})
	marked := clusterRowIDs(before)[heavy.ClusterIdx][:20]
	b.aversion = &aversion{}
	for _, id := range marked {
		b.aversion.titles = append(b.aversion.titles, aversionTitle{embedding: c.byID[id].vec, weight: 1})
	}
	row, _, allAverted, err := b.anchoredRow(t.Context(), anchors, CacheCandidateLimit, append(exclude, marked...), catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if allAverted {
		t.Fatal("the light interest was turned away too; the test needs it kept")
	}
	if len(row) != CacheCandidateLimit {
		t.Fatalf("composed row of %d after dropping the dominant interest, want %d", len(row), CacheCandidateLimit)
	}
}

// A profile that turned away every interest gets a main row built empty,
// rather than a row around its averaged taste that would serve those
// interests again.
func TestAversionDropsTheMainRowWhenEveryInterestIsTurnedAway(t *testing.T) {
	c := newToyCatalog()
	a := relevancePersonas(c)["A"]
	var marked []string
	for _, ids := range clusterRowIDs(buildPersonaRows(t, c, a)) {
		marked = append(marked, ids[:min(len(ids), 25)]...)
	}
	b, _, exclude := personaBuilder(t, c, a, func(*toyStore) {})
	b.aversion = &aversion{}
	for _, id := range marked {
		b.aversion.titles = append(b.aversion.titles, aversionTitle{embedding: c.byID[id].vec, weight: 1})
	}
	main, err := b.mainRow(t.Context(), 1, "p", CacheCandidateLimit, append(exclude, marked...), catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if main == nil || len(main.Items) != 0 {
		t.Fatalf("main row %v after every interest was turned away, want one built empty", main)
	}
}
