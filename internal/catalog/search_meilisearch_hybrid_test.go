package catalog

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

// hybridSearchFixture is a provider with semantic search on, backed by a fake
// Meilisearch, so no database is needed. Keyword queries find nothing; the
// meaning-only query answers meaningHits.
type hybridSearchFixture struct {
	provider    *MeilisearchSearchProvider
	itemTypes   []string
	meaningHits string
	mu          sync.Mutex
	bodies      map[string][]byte
}

func newHybridSearchFixture(t *testing.T) *hybridSearchFixture {
	t.Helper()
	f := &hybridSearchFixture{itemTypes: []string{"movie", "series"}, meaningHits: `[]`, bodies: map[string][]byte{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/settings") {
			_, _ = w.Write([]byte(`{"pagination":{"maxTotalHits":1000}}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies[r.URL.Path] = body
		f.mu.Unlock()
		if r.URL.Path != "/multi-search" {
			_, _ = w.Write([]byte(`{"hits":[],"estimatedTotalHits":0}`))
			return
		}
		var sent struct{ Queries []map[string]json.RawMessage }
		_ = json.Unmarshal(body, &sent)
		results := make([]string, 0, len(sent.Queries))
		for _, query := range sent.Queries {
			hits := `[]`
			if _, meaning := query["hybrid"]; meaning {
				hits = f.meaningHits
			}
			results = append(results, `{"hits":`+hits+`,"estimatedTotalHits":0}`)
		}
		_, _ = w.Write([]byte(`{"results":[` + strings.Join(results, ",") + `]}`))
	}))
	t.Cleanup(server.Close)
	provider, err := NewMeilisearchSearchProvider(nil, nil, nil, MeilisearchProviderConfig{
		URL:             server.URL,
		IndexTypes:      f.itemTypes,
		SemanticEnabled: true,
		SemanticRatio:   0.5,
		Vectorizer:      &fakeCatalogSearchVectorizer{vector: []float32{0.5, 0.25}},
		Coverage:        fakeCoverageGate{ready: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider.stateRepo = fakeMeilisearchIndexStateStore{state: SearchIndexState{
		ActiveIndexUID: "catalog_hybrid",
		SchemaVersion:  catalogSearchMeilisearchSchemaVersion(provider.config.Embedder, provider.config.IndexTypes, true, false),
	}}
	f.provider = provider
	return f
}

// settingsCurrent records the active index as having this server's settings,
// so searches send attributesToSearchOn and the people query.
func (f *hybridSearchFixture) settingsCurrent() {
	store := f.provider.stateRepo.(fakeMeilisearchIndexStateStore)
	store.state.SettingsFingerprint = catalogSearchMeilisearchSettingsFingerprint
	f.provider.stateRepo = store
}

// search runs one search and returns its result and the body sent to path.
func (f *hybridSearchFixture) search(t *testing.T, req CatalogSearchRequest, path string) (*CatalogSearchResult, []byte) {
	t.Helper()
	req.ItemTypes = f.itemTypes
	result, err := f.provider.Search(t.Context(), req)
	if err != nil {
		t.Fatalf("search %q: %v", req.Query, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	body, ok := f.bodies[path]
	if !ok {
		t.Fatalf("search %q sent nothing to %s; sent %d requests", req.Query, path, len(f.bodies))
	}
	clear(f.bodies)
	return result, body
}

// A hybrid search sends a keyword query and a meaning-only query. The meaning
// query carries no rankingScoreThreshold: with one, Meilisearch runs a vector
// query to its search cutoff whenever fewer than limit hits clear it, which
// outlives Silo's timeout. Its limit is capped instead.
func TestMeilisearchHybridSearchRequestShape(t *testing.T) {
	f := newHybridSearchFixture(t)
	_, body := f.search(t, CatalogSearchRequest{CursorPaging: true, Query: "Model Citizen", Limit: 60}, "/multi-search")
	var sent struct {
		Federation json.RawMessage
		Queries    []map[string]json.RawMessage
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Federation != nil || len(sent.Queries) != 2 {
		t.Fatalf("multi-search request = %s", body)
	}
	keyword, meaning := sent.Queries[0], sent.Queries[1]
	if string(keyword["q"]) != `"Model Citizen"` || string(keyword["matchingStrategy"]) != `"all"` || string(keyword["limit"]) != "1000" {
		t.Fatalf("keyword query = %s", body)
	}
	for _, field := range []string{"vector", "hybrid", "rankingScoreThreshold"} {
		if _, present := keyword[field]; present {
			t.Fatalf("keyword query carries %q", field)
		}
	}
	var hybrid meilisearchHybridRequest
	if err := json.Unmarshal(meaning["hybrid"], &hybrid); err != nil || hybrid.SemanticRatio != 1 || hybrid.Embedder != f.provider.config.Embedder {
		t.Fatalf("meaning query hybrid = %s (%v)", meaning["hybrid"], err)
	}
	if string(meaning["q"]) != `""` || string(meaning["limit"]) != "100" {
		t.Fatalf("meaning query q/limit = %s/%s", meaning["q"], meaning["limit"])
	}
	if _, present := meaning["rankingScoreThreshold"]; present {
		t.Fatal("meaning query carries rankingScoreThreshold")
	}
	// Only keyword hits are cut by their ranking details.
	if string(keyword["showRankingScoreDetails"]) != "true" {
		t.Fatalf("keyword query without showRankingScoreDetails: %s", body)
	}
	if _, present := meaning["showRankingScoreDetails"]; present {
		t.Fatal("meaning query carries showRankingScoreDetails")
	}
	for _, query := range sent.Queries {
		if string(query["showRankingScore"]) != "true" {
			t.Fatalf("query without showRankingScore: %s", body)
		}
	}
}

// A hybrid search reports hybrid only when a meaning hit cleared the floor
// and made the ranking; otherwise it ranked by keyword alone.
func TestMeilisearchHybridSearchModeFollowsMeaningHits(t *testing.T) {
	f := newHybridSearchFixture(t)
	f.meaningHits = `[{"content_id":"weak","_rankingScore":0.6}]` // floor is 0.65
	result, _ := f.search(t, CatalogSearchRequest{CursorPaging: true, Query: "Model Citizen", Limit: 60}, "/multi-search")
	if result.Mode != searchModeKeyword || result.SemanticUsed || result.FallbackReason != "" {
		t.Fatalf("below-floor mode/semantic/fallback = %q/%v/%q, want keyword without fallback", result.Mode, result.SemanticUsed, result.FallbackReason)
	}
}

// A one-word search stays a plain keyword search with no vector.
func TestMeilisearchOneWordSearchStaysKeyword(t *testing.T) {
	f := newHybridSearchFixture(t)
	result, body := f.search(t, CatalogSearchRequest{CursorPaging: true, Query: "Conclave", Limit: 60}, "/indexes/catalog_hybrid/search")
	if result.Mode != searchModeKeyword {
		t.Fatalf("one-word search mode = %q, want keyword", result.Mode)
	}
	for _, field := range []string{`"vector"`, `"hybrid"`} {
		if strings.Contains(string(body), field) {
			t.Fatalf("keyword search carries %s: %s", field, body)
		}
	}
	if !strings.Contains(string(body), `"showRankingScoreDetails":true`) {
		t.Fatalf("keyword search without showRankingScoreDetails: %s", body)
	}
}

// Once the index has this server's settings, every keyword search sends a
// keyword query without people and, after it in the same multi-search, a
// people query: the words as one quoted phrase on people only, with the
// keyword query's filter, limit and exclusions. Before that, a search sends
// no people query (TestMeilisearchHybridSearchRequestShape).
func TestMeilisearchPeopleQueryRequestShape(t *testing.T) {
	type query struct {
		Q                       string   `json:"q"`
		Filter                  string   `json:"filter"`
		Limit                   int      `json:"limit"`
		Offset                  int      `json:"offset"`
		AttributesToSearchOn    []string `json:"attributesToSearchOn"`
		MatchingStrategy        string   `json:"matchingStrategy"`
		ShowRankingScoreDetails bool     `json:"showRankingScoreDetails"`
		Hybrid                  json.RawMessage
	}
	for _, tc := range []struct {
		name        string
		req         CatalogSearchRequest
		wantQueries int
		wantPeopleQ string
	}{
		{"cursor keyword", CatalogSearchRequest{CursorPaging: true, Query: `heat -"michael mann"`, Limit: 60}, 2, `"heat" -"michael mann"`},
		{"offset keyword", CatalogSearchRequest{Query: "hanks", Limit: 20}, 2, `"hanks"`},
		{"cursor hybrid", CatalogSearchRequest{CursorPaging: true, Query: "tom hanks -splash", Limit: 60}, 3, `"tom hanks" -splash`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newHybridSearchFixture(t)
			f.settingsCurrent()
			// Only the hybrid case sends the meaning query, last.
			f.provider.config.SemanticEnabled = tc.wantQueries == 3
			_, body := f.search(t, tc.req, "/multi-search")
			var sent struct{ Queries []query }
			if err := json.Unmarshal(body, &sent); err != nil {
				t.Fatal(err)
			}
			if len(sent.Queries) != tc.wantQueries {
				t.Fatalf("multi-search sent %d queries, want %d: %s", len(sent.Queries), tc.wantQueries, body)
			}
			keyword, people := sent.Queries[0], sent.Queries[1]
			if keyword.Q != tc.req.Query || slices.Contains(keyword.AttributesToSearchOn, meilisearchPeopleAttribute) || len(keyword.AttributesToSearchOn) == 0 {
				t.Fatalf("keyword query = %+v", keyword)
			}
			if people.Q != tc.wantPeopleQ || !reflect.DeepEqual(people.AttributesToSearchOn, []string{meilisearchPeopleAttribute}) ||
				!people.ShowRankingScoreDetails || people.Hybrid != nil {
				t.Fatalf("people query = %+v", people)
			}
			if people.Filter != keyword.Filter || people.Limit != keyword.Limit || people.Offset != 0 || keyword.Offset != 0 ||
				people.MatchingStrategy != keyword.MatchingStrategy {
				t.Fatalf("people query %+v does not share the keyword query's %+v", people, keyword)
			}
		})
	}
}

// The people query is the search's words as one quoted phrase, which
// Meilisearch matches whole-word, followed by its exclusions as typed. A year
// hint stays outside the phrase.
func TestMeilisearchPeopleQuery(t *testing.T) {
	for _, tc := range []struct {
		query, want string
		withYear    bool
	}{
		{query: "heat", want: `"heat"`},
		{query: "  Tom   Hanks ", want: `"Tom Hanks"`},
		{query: `"tom hanks" forrest`, want: `"tom hanks forrest"`},
		{query: `tom -hanks -"colin hanks"`, want: `"tom" -hanks -"colin hanks"`},
		{query: `tom hanks 1994`, want: `"tom hanks" 1994`, withYear: true},
		{query: `1994 tom -cruise`, want: `"tom" 1994 -cruise`, withYear: true},
		{query: "1917", want: `"1917"`},
		{query: `"blade runner 2049"`, want: `"blade runner 2049"`},
		{query: `o"brien`, want: `"o brien"`},
		{query: `tom -"unclosed phrase`, want: `"tom phrase" -unclosed`},
		{query: `tom "" -"" - hanks`, want: `"tom hanks"`},
		{query: "-alien -predator", want: ""},
		{query: "", want: ""},
	} {
		got, withYear := meilisearchPeopleQuery(tc.query)
		if got != tc.want || withYear != tc.withYear {
			t.Errorf("meilisearchPeopleQuery(%q) = %q, %v; want %q, %v", tc.query, got, withYear, tc.want, tc.withYear)
		}
	}

	// A year hint is searched on the year attribute as well; a search with
	// nothing to match sends no people query.
	people, ok := peopleSearchRequest(meilisearchSearchRequest{Query: "tom hanks 1994", AttributesToSearchOn: meilisearchTwoTermSearchAttributes})
	if !ok || !reflect.DeepEqual(people.AttributesToSearchOn, []string{meilisearchPeopleAttribute, "year"}) {
		t.Fatalf("year people query = %+v, %v", people, ok)
	}
	if _, ok := peopleSearchRequest(meilisearchSearchRequest{Query: "-alien", AttributesToSearchOn: meilisearchKeywordSearchAttributes}); ok {
		t.Fatal("an exclusion-only search sent a people query")
	}
}

// A quoted phrase scores lower than the same words unquoted, so kept people
// hits are scaled onto the keyword hits' scale before a hybrid merge weighs
// them against matches by meaning.
func TestMeilisearchPeopleScoresOnKeywordScale(t *testing.T) {
	hit := func(id string, score float64) meilisearchSearchHit {
		return meilisearchSearchHit{ContentID: id, RankingScore: score}
	}
	scores := func(hits []meilisearchSearchHit) []float64 {
		out := []float64{}
		for _, h := range hits {
			out = append(out, math.Round(h.RankingScore*1000)/1000)
		}
		return out
	}
	people := []meilisearchSearchHit{hit("lead", 0.6), hit("support", 0.57)}
	if got, want := scores(meilisearchPeopleScoresOnKeywordScale(nil, people)), []float64{1, 0.95}; !reflect.DeepEqual(got, want) {
		t.Fatalf("no keyword hit kept: scores = %v, want %v", got, want)
	}
	keyword := []meilisearchSearchHit{hit("title", 0.9)}
	if got, want := scores(meilisearchPeopleScoresOnKeywordScale(keyword, people)), []float64{0.9, 0.855}; !reflect.DeepEqual(got, want) {
		t.Fatalf("keyword hit kept: scores = %v, want %v", got, want)
	}
	if people[0].RankingScore != 0.6 {
		t.Fatal("input hits were modified")
	}
}

// The keyword and people lists are each cut against the best hit of the two,
// then ordered keyword first without repeats.
func TestCombineMeilisearchKeywordHits(t *testing.T) {
	hit := func(id string, missed, typos int, proximity float64) meilisearchSearchHit {
		return meilisearchSearchHit{ContentID: id, RankingScoreDetails: &meilisearchRankingScoreDetails{
			Words:     &meilisearchWordsDetail{MatchingWords: 2 - missed, MaxMatchingWords: 2},
			Typo:      &meilisearchTypoDetail{TypoCount: typos},
			Proximity: &meilisearchProximityDetail{Score: proximity},
		}}
	}
	ids := func(hits []meilisearchSearchHit) []string {
		out := []string{}
		for _, h := range hits {
			out = append(out, h.ContentID)
		}
		return out
	}
	for _, tc := range []struct {
		name          string
		other, people []meilisearchSearchHit
		limit         int
		want          []string
		otherKept     int
		peopleKept    int
	}{
		{name: "nothing", limit: 10, want: []string{}},
		{
			name:  "keyword hits first, then people",
			other: []meilisearchSearchHit{hit("heat", 0, 0, 1), hit("the-heat", 0, 0, 1)},
			people: []meilisearchSearchHit{
				hit("heat-person", 0, 0, 1),
			},
			limit: 10, want: []string{"heat", "the-heat", "heat-person"}, otherKept: 2, peopleKept: 1,
		},
		{
			name:   "an exact name cuts title hits with a typo",
			other:  []meilisearchSearchHit{hit("idle-hands", 0, 1, 1)},
			people: []meilisearchSearchHit{hit("big", 0, 0, 1), hit("splash", 0, 0, 1)},
			limit:  10, want: []string{"big", "splash"}, otherKept: 0, peopleKept: 2,
		},
		{
			name:   "an exact title cuts scattered and partial name hits",
			other:  []meilisearchSearchHit{hit("matrix", 0, 0, 1)},
			people: []meilisearchSearchHit{hit("scattered", 0, 0, 0.25), hit("partial", 1, 0, 1)},
			limit:  10, want: []string{"matrix"}, otherKept: 1, peopleKept: 0,
		},
		{
			name:   "a partial keyword match is cut by a full name match",
			other:  []meilisearchSearchHit{hit("one-word-title", 1, 0, 1)},
			people: []meilisearchSearchHit{hit("hoffman", 0, 0, 1)},
			limit:  10, want: []string{"hoffman"}, otherKept: 0, peopleKept: 1,
		},
		{
			name:   "a hit in both lists keeps its keyword place",
			other:  []meilisearchSearchHit{hit("a", 0, 0, 1), hit("both", 0, 0, 1)},
			people: []meilisearchSearchHit{hit("both", 0, 0, 1), hit("b", 0, 0, 1)},
			limit:  10, want: []string{"a", "both", "b"}, otherKept: 2, peopleKept: 2,
		},
		{
			name:   "limit",
			other:  []meilisearchSearchHit{hit("a", 0, 0, 1), hit("b", 0, 0, 1)},
			people: []meilisearchSearchHit{hit("c", 0, 0, 1)},
			limit:  2, want: []string{"a", "b"}, otherKept: 2, peopleKept: 1,
		},
		{
			name:   "people only",
			people: []meilisearchSearchHit{hit("a", 0, 0, 1), hit("typo", 0, 1, 1)},
			limit:  10, want: []string{"a"}, peopleKept: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := combineMeilisearchKeywordHits(tc.other, tc.people, tc.limit)
			if !reflect.DeepEqual(ids(got.Hits), tc.want) || got.OtherKept != tc.otherKept || got.PeopleKept != tc.peopleKept {
				t.Fatalf("combined = %v kept %d/%d, want %v kept %d/%d", ids(got.Hits), got.OtherKept, got.PeopleKept, tc.want, tc.otherKept, tc.peopleKept)
			}
		})
	}
}

// A keyword ranking keeps its hits while they match as many words as the
// first, with no more typos, and their words at least
// meilisearchKeywordProximityShare as close. Cases follow searches measured
// on a 1.65M-document index.
func TestMeilisearchKeywordMatchCount(t *testing.T) {
	type detail struct {
		words, typos *int
		proximity    *float64
	}
	n := func(v int) *int { return &v }
	f := func(v float64) *float64 { return &v }
	hit := func(d *detail) meilisearchSearchHit {
		if d == nil {
			return meilisearchSearchHit{ContentID: "x"}
		}
		details := &meilisearchRankingScoreDetails{}
		if d.words != nil {
			details.Words = &meilisearchWordsDetail{MatchingWords: *d.words}
		}
		if d.typos != nil {
			details.Typo = &meilisearchTypoDetail{TypoCount: *d.typos}
		}
		if d.proximity != nil {
			details.Proximity = &meilisearchProximityDetail{Score: *d.proximity}
		}
		return meilisearchSearchHit{ContentID: "x", RankingScoreDetails: details}
	}
	tests := []struct {
		name string
		hits []*detail
		want int
	}{
		{"empty", nil, 0},
		{"back to the future: one word cut", []*detail{
			{words: n(4), typos: n(0), proximity: f(1)},
			{words: n(4), typos: n(0), proximity: f(1)},
			{words: n(2), typos: n(0), proximity: f(1)},
			{words: n(1), typos: n(0), proximity: f(1)},
		}, 2},
		{"scrubs: typo cut", []*detail{
			{words: n(1), typos: n(0), proximity: f(1)},
			{words: n(1), typos: n(0), proximity: f(1)},
			{words: n(1), typos: n(1), proximity: f(1)},
		}, 2},
		{"tom hanks: names in different cast entries cut", []*detail{
			{typos: n(0), proximity: f(1)},
			{typos: n(0), proximity: f(1)},
			{typos: n(0), proximity: f(0.25)},
			{typos: n(1), proximity: f(1)},
		}, 2},
		{"the matrix: near matches kept", []*detail{
			{typos: n(0), proximity: f(1)},
			{typos: n(0), proximity: f(0.75)},
			{typos: n(0), proximity: f(0.5)},
		}, 2},
		{"forest gump: best hit's typo keeps its peers", []*detail{
			{typos: n(1), proximity: f(1)},
			{typos: n(1), proximity: f(1)},
		}, 2},
		{"lord rings: scattered best keeps its peers", []*detail{
			{typos: n(0), proximity: f(0.5)},
			{typos: n(0), proximity: f(0.5)},
			{typos: n(0), proximity: f(0.25)},
		}, 2},
		{"absent objects do not separate hits", []*detail{
			{words: n(2)},
			{typos: n(3), proximity: f(0.1)},
			nil,
			{words: n(2), typos: n(9)},
			{words: n(1)},
		}, 4},
		{"best hit without details keeps everything", []*detail{
			nil,
			{words: n(1), typos: n(2), proximity: f(0.1)},
		}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits := make([]meilisearchSearchHit, len(tt.hits))
			for i, d := range tt.hits {
				hits[i] = hit(d)
			}
			got := 0
			if len(hits) > 0 {
				got = meilisearchKeywordMatchCount(hits[0], hits)
			}
			if got != tt.want {
				t.Fatalf("kept %d hits, want %d", got, tt.want)
			}
		})
	}
}

func TestMergeMeilisearchHybridHits(t *testing.T) {
	hit := func(id string, score float64) meilisearchSearchHit {
		return meilisearchSearchHit{ContentID: id, RankingScore: score}
	}
	keyword := []meilisearchSearchHit{hit("k1", 0.9), hit("both", 0.5), hit("tie-k", 0.4)}
	meaning := []meilisearchSearchHit{
		hit("both", 0.95),  // outranks its keyword occurrence
		hit("m1", 0.93),    // within the margin of the best meaning hit
		hit("far", 0.80),   // above the floor, but too far below the best
		hit("below", 0.64), // under the floor
	}
	// Floor 0.65 gives a margin of 0.035, so meaning hits need 0.915. Ratio
	// 0.5: keyword k1 0.45, both 0.25, tie-k 0.20; meaning both 0.475, m1
	// 0.465.
	got := mergeMeilisearchHybridHits(keyword, meaning, 0.5, 0.65, 10)
	if want := []string{"both", "m1", "k1", "tie-k"}; !reflect.DeepEqual(got.IDs, want) {
		t.Fatalf("merged = %v, want %v", got.IDs, want)
	}
	if got.SemanticKept != 2 || got.SemanticRanked != 2 {
		t.Fatalf("kept/ranked = %d/%d, want 2/2", got.SemanticKept, got.SemanticRanked)
	}

	// On a tie the keyword hit comes first.
	tie := mergeMeilisearchHybridHits([]meilisearchSearchHit{hit("k", 0.8)}, []meilisearchSearchHit{hit("m", 0.8)}, 0.5, 0.65, 10)
	if want := []string{"k", "m"}; !reflect.DeepEqual(tie.IDs, want) {
		t.Fatalf("tie order = %v, want %v", tie.IDs, want)
	}

	// The limit cuts the ranking, and a meaning hit cut off is not counted
	// as ranked.
	cut := mergeMeilisearchHybridHits(keyword, meaning, 0.5, 0.65, 1)
	if want := []string{"both"}; !reflect.DeepEqual(cut.IDs, want) || cut.SemanticRanked != 1 {
		t.Fatalf("limited merge = %v ranked %d, want %v ranked 1", cut.IDs, cut.SemanticRanked, want)
	}
}

// Excluded terms reach the keyword query only; the vector is embedded from
// the rest, and a search of nothing but exclusions embeds nothing.
func TestNormalizeCatalogSearchQueryForVectorStripsExclusions(t *testing.T) {
	for query, want := range map[string]string{
		"  space   -alien ":             "space",
		`space -"alien invasion" opera`: "space opera",
		`"space opera" -alien`:          `"space opera"`,
		"spider-man -venom":             "spider-man",
		"-alien -predator":              "",
		`space -"unclosed phrase`:       "space phrase",
		"found family - space":          "found family space",
		"tom hanks":                     "tom hanks",
	} {
		if got := normalizeCatalogSearchQueryForVector(query); got != want {
			t.Errorf("normalizeCatalogSearchQueryForVector(%q) = %q, want %q", query, got, want)
		}
	}
}
