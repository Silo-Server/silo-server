package catalog

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
)

// TestItemRepo_GetByIDsWithAccess_SingleQueryWithLibraryFilter verifies that
// the SQL emitted by buildGetByIDsWithAccessSQL pushes the library access
// constraint into the same query as the content_id batch lookup, replacing
// the per-item EnsureAccessible fan-out called out in the catalog SQL audit
// (2026-05-01 §3.3).
func TestItemRepo_GetByIDsWithAccess_SingleQueryWithLibraryFilter(t *testing.T) {
	repo := &ItemRepository{}
	sql, args := repo.buildGetByIDsWithAccessSQL([]string{"a", "b"}, AccessFilter{
		AllowedLibraryIDs: []int{1, 2},
	})
	if !strings.Contains(sql, "content_id = ANY($1)") {
		t.Fatalf("expected ANY for content IDs; got %s", sql)
	}
	if !strings.Contains(sql, "media_folder_id = ANY($2)") {
		t.Fatalf("expected library access pushed into JOIN/WHERE; got %s", sql)
	}
	if len(args) != 2 {
		t.Fatalf("expected 2 args; got %v", args)
	}
}

// TestItemRepo_GetByIDsWithAccess_DisabledOnlyRequiresLibraryMembership pins
// the fix for the disabled-only access path: when AllowedLibraryIDs is nil
// and only DisabledLibraryIDs is set, the SQL must additionally require
// positive library membership (an EXISTS over media_item_libraries with
// no media_folder_id filter). Otherwise orphan items (rows in media_items
// with no media_item_libraries link — mid-scan, stale rows from a removed
// library, or metadata-refresh inserts not yet linked) would pass the
// NOT EXISTS — which is true over an empty subquery — and become visible
// to users restricted by DisabledLibraryIDs. EnsureAccessible's prior
// INNER JOIN on media_item_libraries enforced this implicitly.
//
// Regression guard for Codex P2 follow-up to PR #42.
func TestItemRepo_GetByIDsWithAccess_DisabledOnlyRequiresLibraryMembership(t *testing.T) {
	repo := &ItemRepository{}
	sql, _ := repo.buildGetByIDsWithAccessSQL([]string{"a"}, AccessFilter{
		DisabledLibraryIDs: []int{9},
	})
	// Positive-membership EXISTS must be present. It has no media_folder_id
	// filter so the NOT EXISTS that follows is what enforces the disabled-list.
	if strings.Count(sql, "EXISTS (") < 2 {
		t.Fatalf("expected both a membership EXISTS and a disabled NOT EXISTS clause; got %s", sql)
	}
	// Pin the membership predicate's specific shape: an EXISTS against
	// media_item_libraries that does NOT bind a media_folder_id ANY(...) arg.
	// (The disabled NOT EXISTS does bind one — we want the membership EXISTS
	// to be argument-free so it doesn't reorder placeholder indices.)
	if !strings.Contains(sql, "EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id)") {
		t.Fatalf("expected argument-free membership EXISTS over media_item_libraries; got %s", sql)
	}
}

// TestItemRepo_EnsureAccessibleSQL_DisabledOnlyRequiresMembership mirrors the
// GetByIDsWithAccess orphan-item guard for the per-item path: disabled-only
// scopes still require positive library membership.
func TestItemRepo_EnsureAccessibleSQL_DisabledOnlyRequiresMembership(t *testing.T) {
	sql, args := buildEnsureAccessibleSQL("item-1", AccessFilter{
		DisabledLibraryIDs: []int{9},
	})
	if !strings.Contains(sql, "EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id)") {
		t.Fatalf("expected argument-free membership EXISTS; got %s", sql)
	}
	if !strings.Contains(sql, "NOT EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id AND mil.media_folder_id = ANY($2))") {
		t.Fatalf("expected disabled-library NOT EXISTS bound at $2; got %s", sql)
	}
	if len(args) != 2 {
		t.Fatalf("expected 2 args (id, disabled); got %v", args)
	}
}

// TestItemRepo_GetByIDsWithAccess_MaxContentRatingProducesINClause pins the
// maturity-ceiling branch: a comparison against the stored minimum age, with
// the ceiling's own age bound as a single arg.
func TestItemRepo_GetByIDsWithAccess_MaxContentRatingProducesINClause(t *testing.T) {
	repo := &ItemRepository{}
	sql, args := repo.buildGetByIDsWithAccessSQL([]string{"a"}, AccessFilter{
		MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"},
	})
	if !strings.Contains(sql, "mi.content_rating_age IS NOT NULL AND mi.content_rating_age <= $") {
		t.Fatalf("expected stored-age ceiling clause; got %s", sql)
	}
	if len(args) != 2 {
		t.Fatalf("expected 2 args (ids + ceiling age); got %v", args)
	}
	if args[1] != 14 {
		t.Fatalf("expected the PG-13 ceiling to bind age 14, the top of its US tier; got %v", args[1])
	}
}

// TestItemRepo_GetByIDsWithAccess_CombinedClausesIndexCorrectly verifies that
// when AllowedLibraryIDs, DisabledLibraryIDs, and MaxContentRating are all
// set, placeholder indices advance in the documented order: $1 = ids,
// $2 = allowed libs, $3 = disabled libs, $4 = rating ladder.
func TestItemRepo_GetByIDsWithAccess_CombinedClausesIndexCorrectly(t *testing.T) {
	repo := &ItemRepository{}
	sql, args := repo.buildGetByIDsWithAccessSQL([]string{"a"}, AccessFilter{
		AllowedLibraryIDs:  []int{1, 2},
		DisabledLibraryIDs: []int{9},
		MaturityLimits:     access.MaturityLimits{MaxContentRating: "PG-13"},
	})
	// Expect: $1 = ids, $2 = allowed libs, $3 = disabled libs, $4 = ceiling age.
	if !strings.Contains(sql, "media_folder_id = ANY($2)") {
		t.Fatalf("expected AllowedLibraryIDs at $2; got %s", sql)
	}
	if !strings.Contains(sql, "media_folder_id = ANY($3)") {
		t.Fatalf("expected DisabledLibraryIDs at $3; got %s", sql)
	}
	if !strings.Contains(sql, "mi.content_rating_age <= $4") {
		t.Fatalf("expected the ceiling age at $4; got %s", sql)
	}
	if len(args) != 4 {
		t.Fatalf("expected 4 args (ids, allowed, disabled, ceiling age); got %v", args)
	}
}

// TestItemRepo_Search_EmptyQueryReturnsEmpty pins the early-return contract
// when input parses to no searchable text. Downstream callers rely on
// (dataSQL == "") to short-circuit without binding any args.
func TestItemRepo_Search_EmptyQueryReturnsEmpty(t *testing.T) {
	repo := &ItemRepository{}
	dataSQL, countSQL, args := repo.buildSearchSQL("   ", nil, 20, 0, AccessFilter{})
	if dataSQL != "" || countSQL != "" || args != nil {
		t.Fatalf("expected (\"\", \"\", nil) for whitespace-only query; got (%q, %q, %v)", dataSQL, countSQL, args)
	}
}

// TestEligibleForFuzzy pins the min-token gate that guards the fuzzy title
// fallback: the longest normalized token must clear fuzzyMinTokenLen, so short,
// non-selective queries stay on the exact FTS/prefix path.
func TestEligibleForFuzzy(t *testing.T) {
	cases := []struct {
		query string
		want  bool
	}{
		{"avegners", true},         // 8-char typo
		{"sponge bob", true},       // unquoted input retains fuzzy typo recovery
		{"a vengers", true},        // judged on "vengers" (7), not "a"
		{"dune", true},             // exactly at the floor
		{"star wars", true},        // final token is long enough for normal fuzzy fallback
		{"star w", true},           // unquoted typeahead retains fuzzy augmentation
		{`"Star Wars" zen`, false}, // quoted remainder stays on the leading-title path
		{"the", false},             // 3 chars
		{"a b c", false},           // all short
		{"and the", false},         // "and" normalized away, "the" is 3
		{"   ", false},             // empty
	}
	for _, tc := range cases {
		if got := eligibleForFuzzy(parseSearchQuery(tc.query)); got != tc.want {
			t.Errorf("eligibleForFuzzy(%q) = %v, want %v", tc.query, got, tc.want)
		}
	}
}

// TestSearchTextFromParsed pins the shared searchText derivation used by both
// the FTS and fuzzy builders: parsed Text when present, else a quote-stripped
// fallback off the raw query. Both builders must agree so they search the same
// text.
func TestSearchTextFromParsed(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"interstellar", "interstellar"},
		{`"the matrix"`, "the matrix"},
		{"  spaced   out  ", "spaced out"},
		{`"`, ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		if got := searchTextFromParsed(parseSearchQuery(tc.raw)); got != tc.want {
			t.Errorf("searchTextFromParsed(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestSearchBlockHasExactTitle(t *testing.T) {
	items := []*models.MediaItem{
		{Title: "Game of Thrones"},
		{Title: "Dune: Part Two"},
		nil,
	}
	for _, query := range []string{"game of thrones", "dune part 2"} {
		if !searchBlockHasExactTitle(items, query) {
			t.Fatalf("expected normalized exact title %q to suppress fuzzy fallback", query)
		}
	}
	for _, query := range []string{"", "game throne", "breking bad"} {
		if searchBlockHasExactTitle(items, query) {
			t.Fatalf("non-exact title %q must retain fuzzy fallback", query)
		}
	}
}

// TestParseSearchQuery_NormalizedText asserts parseSearchQuery precomputes the
// full normalized text that eligibleForFuzzy's token gate reads (so the gate
// does not re-normalize on every sparse search).
func TestParseSearchQuery_NormalizedText(t *testing.T) {
	if got := parseSearchQuery("The Avengers").NormalizedText; got != "the avengers" {
		t.Fatalf("NormalizedText = %q, want %q", got, "the avengers")
	}
	// "and" is dropped and folded, matching normalizeTitleForComparison.
	if got := parseSearchQuery("law and order").NormalizedText; got != "law order" {
		t.Fatalf("NormalizedText = %q, want %q", got, "law order")
	}
}

// TestItemRepo_Search_LibraryScopeUsesIndependentExistsPredicates pins the
// leak-safe library scoping shared by the FTS search and the fuzzy fallback via
// appendSearchScopeFilters. The prior JOIN + NOT(mil.media_folder_id = ANY(...))
// form let an item linked to BOTH a disabled and a non-disabled library survive
// the deny check on the passing membership row, so a typo (fuzzy) or exact
// search could surface items from a disabled library. Both builders must instead
// emit independent EXISTS/NOT EXISTS subqueries and no membership JOIN.
func TestItemRepo_Search_LibraryScopeUsesIndependentExistsPredicates(t *testing.T) {
	repo := &ItemRepository{}
	filter := AccessFilter{DisabledLibraryIDs: []int{9}}

	ftsSQL, _, _ := repo.buildSearchSQL("avatar", []string{"movie"}, 20, 0, filter)
	fuzzySQL, _, _ := repo.buildFuzzySearchSQL("avatar", []string{"movie"}, 20, 0, filter, true, nil, 0)

	for name, sql := range map[string]string{"fts": ftsSQL, "fuzzy": fuzzySQL} {
		if strings.Contains(sql, "JOIN media_item_libraries") {
			t.Fatalf("%s query must not JOIN media_item_libraries for scoping; got:\n%s", name, sql)
		}
		// Disabled-only path requires positive membership (argument-free EXISTS)
		// so orphan items don't slip through the vacuous NOT EXISTS...
		if !strings.Contains(sql, "EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id)") {
			t.Fatalf("%s query must require library membership via an argument-free EXISTS; got:\n%s", name, sql)
		}
		// ...plus the disabled NOT EXISTS.
		if !strings.Contains(sql, "NOT EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id AND mil.media_folder_id = ANY($") {
			t.Fatalf("%s query must exclude disabled libraries via NOT EXISTS; got:\n%s", name, sql)
		}
	}
}

func TestItemRepo_Search_EpisodeAccessUsesIndependentMembershipPredicates(t *testing.T) {
	repo := &ItemRepository{}
	sql, _, _ := repo.buildSearchSQL("Who Are You?", []string{"episode"}, 20, 0, AccessFilter{
		AllowedLibraryIDs:  []int{1},
		DisabledLibraryIDs: []int{2},
	})
	if !strings.Contains(sql, "ece.media_folder_id = ANY($") {
		t.Fatalf("episode search missing direct allowed-library catalog filter:\n%s", sql)
	}
	if !strings.Contains(sql, "NOT EXISTS (SELECT 1 FROM episode_catalog_entries disabled_ece") {
		t.Fatalf("episode search missing independent disabled-library NOT EXISTS:\n%s", sql)
	}
	assertEpisodeParentDisabledAccess(t, sql, "ece.series_id")
	if strings.Contains(sql, "WHERE e_parent.content_id = ece.episode_id") {
		t.Fatalf("episode candidate branch must use ece.series_id without an episode lookup:\n%s", sql)
	}
}

// TestItemRepo_ListUnmatchedByFolderAndPathPrefix_ExcludesMangaChapters pins
// the manga-chapter exclusion in the unmatched-item lister. Manga chapters are
// type='ebook' items that stay status='pending' by design (the type='manga'
// series item carries all provider metadata), so without a NOT EXISTS guard
// against manga_chapters every library scan funnels each chapter through the
// matcher's retry loop — one rate-limited ebook-plugin search per chapter
// (observed live 2026-06-12: 31,564 chapters x ~1s = 8h46m per scan, 100%
// no-match). Mirrors the same exclusion in the ebook enricher's claim query.
func TestItemRepo_ListUnmatchedByFolderAndPathPrefix_ExcludesMangaChapters(t *testing.T) {
	repo := &ItemRepository{}

	sql, args := repo.buildListUnmatchedByFolderAndPathPrefixSQL(10, "/mnt/media/manga", 0)
	if !strings.Contains(sql, "NOT EXISTS") || !strings.Contains(sql, "manga_chapters") {
		t.Fatalf("expected manga_chapters NOT EXISTS guard in unmatched lister; got:\n%s", sql)
	}
	if len(args) != 3 {
		t.Fatalf("expected 3 args without limit; got %v", args)
	}

	sql, args = repo.buildListUnmatchedByFolderAndPathPrefixSQL(10, "/mnt/media/manga", 25)
	if !strings.Contains(sql, "LIMIT $4") {
		t.Fatalf("expected LIMIT $4 when limit > 0; got:\n%s", sql)
	}
	if len(args) != 4 {
		t.Fatalf("expected 4 args with limit; got %v", args)
	}
}

func TestRerankFuzzyItemsUsesTokenEditCoverage(t *testing.T) {
	items := []*models.MediaItem{
		{ContentID: "throne", Title: "Justice League: Throne of Atlantis"},
		{ContentID: "game", Title: "Game of Thrones"},
		{ContentID: "last-watch", Title: "Game of Thrones: The Last Watch"},
	}
	got, err := rerankFuzzyItems(context.Background(), "Gane of Throns", items, nil, false)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if ids := fuzzyItemIDs(got); !slices.Equal(ids, []string{"game", "last-watch"}) {
		t.Fatalf("results = %v, want both Game of Thrones titles and no title missing a word", ids)
	}

	items = []*models.MediaItem{
		{ContentID: "miss", Title: "Miss Potter"},
		{ContentID: "harry", Title: "Harry Potter and the Order of the Phoenix"},
	}
	got, err = rerankFuzzyItems(context.Background(), "Hary Poter", items, nil, false)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if ids := fuzzyItemIDs(got); !slices.Equal(ids, []string{"harry"}) {
		t.Fatalf("results = %v, want only the Harry Potter title", ids)
	}
}

func fuzzyItemIDs(items []*models.MediaItem) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ContentID
	}
	return ids
}

func TestRerankFuzzyItemsRequiresEveryWord(t *testing.T) {
	// The typo of "The Good Place" is the only title matching all three words.
	// Each other title shares "the" and "place" and was listed after it.
	items := []*models.MediaItem{
		{ContentID: "hiding", Title: "The Hiding Place"},
		{ContentID: "plant", Title: "The God Plant"},
		{ContentID: "good", Title: "The Good Place"},
		{ContentID: "palace", Title: "The Palace"},
		{ContentID: "sun", Title: "A Place in the Sun"},
	}
	got, err := rerankFuzzyItems(context.Background(), "the god place", items, nil, false)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if ids := fuzzyItemIDs(got); !slices.Equal(ids, []string{"good"}) {
		t.Fatalf("results = %v, want only The Good Place", ids)
	}

	// With no title matching every word and no exact hit, the titles missing
	// the fewest words remain.
	got, err = rerankFuzzyItems(context.Background(), "the god place", items[:2], nil, false)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if ids := fuzzyItemIDs(got); !slices.Equal(slices.Sorted(slices.Values(ids)), []string{"hiding", "plant"}) {
		t.Fatalf("relaxed results = %v, want the two titles missing one word", ids)
	}

	// Exact search hits already answer the query; a partial typo match adds
	// nothing.
	got, err = rerankFuzzyItems(context.Background(), "the god place", items[:2], nil, true)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("results beside exact hits = %v, want none", fuzzyItemIDs(got))
	}

	// A title matching only stopwords never qualifies.
	got, err = rerankFuzzyItems(context.Background(), "the god place", []*models.MediaItem{{ContentID: "other", Title: "The Other"}}, nil, false)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("stopword-only results = %v, want none", fuzzyItemIDs(got))
	}
}

func TestRerankFuzzyItemsPrefersSingleWordMatches(t *testing.T) {
	// "ofice" is exactly "of" + "ice" joined, but one edit from "office".
	items := []*models.MediaItem{
		{ContentID: "ice", Title: "The Clink of Ice"},
		{ContentID: "office", Title: "The Office"},
	}
	got, err := rerankFuzzyItems(context.Background(), "the ofice", items, nil, false)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if ids := fuzzyItemIDs(got); !slices.Equal(ids, []string{"office", "ice"}) {
		t.Fatalf("results = %v, want The Office first", ids)
	}
}

func TestScoreFuzzyTitleVariantRunTogetherWords(t *testing.T) {
	for _, tc := range []struct {
		query, title string
		missing      int
	}{
		{"lordoftherings", "The Lord of the Rings: The Two Towers", 0},
		{"gameofthrones", "Game of Thrones", 0},
		// The run must start and end on word boundaries.
		{"heat", "Theater Camp", 1},
		{"ordofthe", "The Lord of the Rings", 1},
	} {
		tokens, _ := boundedFuzzyTokens(tc.query, fuzzyRerankMaxQueryTokens)
		if got := scoreFuzzyTitleVariant(tc.query, tokens, tc.title).missingWords; got != tc.missing {
			t.Errorf("%q vs %q missing %d words, want %d", tc.query, tc.title, got, tc.missing)
		}
	}
}

func TestFuzzyCandidateTextDropsStopwords(t *testing.T) {
	for query, want := range map[string]string{
		"The Godfathr":       "godfathr",
		"Pirates of the Car": "pirates car",
		"the in":             "the in",
		"from up":            "from up",
		"Spider-Man":         "spider man",
	} {
		if got := fuzzyCandidateText(query); got != want {
			t.Errorf("fuzzyCandidateText(%q) = %q, want %q", query, got, want)
		}
	}
}

func TestScoreFuzzyTitleVariantStopwords(t *testing.T) {
	for _, tc := range []struct {
		query, title string
		missing      int
	}{
		// A stopword need not appear in the title.
		{"a quiet place", "Quiet Place", 0},
		{"the good place", "Good Place", 0},
		// A stopword takes no typo, so "she" does not stand in for "the".
		{"the good place", "She Good Place", 0},
		// A query of only stopwords requires each of them.
		{"the in", "In", 1},
		{"ed wood", "Wood", 1},
	} {
		tokens, _ := boundedFuzzyTokens(tc.query, fuzzyRerankMaxQueryTokens)
		score := scoreFuzzyTitleVariant(tc.query, tokens, tc.title)
		if score.missingWords != tc.missing {
			t.Errorf("%q vs %q missing %d words, want %d", tc.query, tc.title, score.missingWords, tc.missing)
		}
	}
	tokens, _ := boundedFuzzyTokens("the good place", fuzzyRerankMaxQueryTokens)
	if got := scoreFuzzyTitleVariant("the good place", tokens, "She Good Place").matchedTokens; got != 2 {
		t.Errorf("\"the\" matched \"she\": matched %d words, want 2", got)
	}
}

func TestRerankFuzzyItemsIgnoresWordSpacing(t *testing.T) {
	// "livestream" matches neither "live" nor "stream" on its own, so titles
	// sharing more separate words with the query used to outrank the film.
	items := []*models.MediaItem{
		{ContentID: "volbeat", Title: "Volbeat - Live From Beyond Hell/Above Heaven"},
		{ContentID: "heaven", Title: "Heaven & Hell: Live From Radio City Music Hall"},
		{ContentID: "birds", Title: "Angry Birds: Live Stream"},
		{ContentID: "liza", Title: "Liza: Warden from Hell"},
		{ContentID: "film", Title: "Livestream from Hell"},
	}
	got, err := rerankFuzzyItems(context.Background(), "Live Stream From Hell", items, nil, false)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if ids := fuzzyItemIDs(got); !slices.Equal(ids, []string{"film"}) {
		t.Fatalf("results = %v, want only Livestream from Hell", ids)
	}

	// A typo keeps the title from matching exactly, so the joined words
	// "live stream" must match "livestream" to keep it.
	items = []*models.MediaItem{
		{ContentID: "volbeat", Title: "Volbeat - Live From Beyond Hell/Above Heaven"},
		{ContentID: "heaven", Title: "Heaven & Hell: Live From Radio City Music Hall"},
		{ContentID: "film", Title: "Livestream from Hell"},
	}
	got, err = rerankFuzzyItems(context.Background(), "Live Stream From Hel", items, nil, false)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if ids := fuzzyItemIDs(got); !slices.Equal(ids, []string{"film"}) {
		t.Fatalf("results = %v, want only Livestream from Hell", ids)
	}

	items = []*models.MediaItem{
		{ContentID: "compound", Title: "Livestream from Hell"},
		{ContentID: "spaced", Title: "Live Stream from Hell"},
	}
	got, err = rerankFuzzyItems(context.Background(), "Live Stream From Hell", items, nil, false)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if ids := fuzzyItemIDs(got); !slices.Equal(ids, []string{"spaced", "compound"}) {
		t.Fatalf("results = %v, want the title spelled as typed first", ids)
	}

	items = []*models.MediaItem{
		{ContentID: "superman", Title: "Superman"},
		{ContentID: "spider", Title: "Spider"},
		{ContentID: "spiderman", Title: "Spider-Man: Homecoming"},
	}
	got, err = rerankFuzzyItems(context.Background(), "spiderman homecoming", items, nil, false)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if ids := fuzzyItemIDs(got); !slices.Equal(ids, []string{"spiderman"}) {
		t.Fatalf("results = %v, want only Spider-Man: Homecoming", ids)
	}
}

func TestScoreFuzzyTitleVariantJoinsWordsWithinTheirTypoAllowance(t *testing.T) {
	for _, tc := range []struct {
		query, title string
		want         int
	}{
		{"live stream from hel", "Livestream from Hell", 4}, //nolint:misspell
		{"spiderman", "Spider-Man", 1},
		// Joining must not drop a short word that has no typo allowance.
		{"ed wood", "Wood", 1},
		{"a spider", "Spider", 1},
		{"live streem", "Livestream", 2},
		// Adjacent numbers stay separate words: "1 2" is not "12".
		{"1 2 monkeys", "Twelve Monkeys", 1},
		{"12 monkeys", "One Two Monkeys", 1},
	} {
		tokens, _ := boundedFuzzyTokens(tc.query, fuzzyRerankMaxQueryTokens)
		if got := scoreFuzzyTitleVariant(tc.query, tokens, tc.title).matchedTokens; got != tc.want {
			t.Errorf("%q vs %q matched %d words, want %d", tc.query, tc.title, got, tc.want)
		}
	}
}

func TestRerankFuzzyItemsUsesBoundedAliases(t *testing.T) {
	items := []*models.MediaItem{
		{ContentID: "distractor", Title: "Tokyo Ghoul"},
		{ContentID: "localized", Title: "倒凶十将伝"},
	}
	aliases := map[string][]string{
		"localized": {"10 tokyo warriors"},
	}
	got, err := rerankFuzzyItems(context.Background(), "Tokyo Warriros", items, aliases, false) //nolint:misspell // the misspelled query is the input under test
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if ids := fuzzyItemIDs(got); !slices.Equal(ids, []string{"localized"}) {
		t.Fatalf("results = %v, want only the alias-backed localized item", ids)
	}
}

func TestRerankFuzzyItemsHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	items := []*models.MediaItem{{ContentID: "one", Title: "One"}, {ContentID: "two", Title: "Two"}}
	if _, err := rerankFuzzyItems(ctx, "three", items, nil, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("rerank error = %v, want context.Canceled", err)
	}
}

func TestFuzzyAutoMaxEditsMirrorsBoundedSearchEnginePolicy(t *testing.T) {
	for _, tc := range []struct {
		length int
		want   int
	}{{1, 0}, {2, 0}, {3, 1}, {5, 1}, {6, 2}, {20, 2}} {
		if got := fuzzyAutoMaxEdits(tc.length); got != tc.want {
			t.Fatalf("fuzzyAutoMaxEdits(%d) = %d, want %d", tc.length, got, tc.want)
		}
	}
}

func TestBoundedLevenshteinRunes(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		max  int
		want int
	}{
		{"game", "gane", 1, 1},
		{"breaking", "breking", 2, 1},
		{"furious", "furios", 2, 1},
		{"thrones", "throns", 2, 1},
		{"game", "throne", 2, 3},
	} {
		if got := boundedLevenshteinRunes([]rune(tc.a), []rune(tc.b), tc.max); got != tc.want {
			t.Fatalf("boundedLevenshteinRunes(%q, %q, %d) = %d, want %d", tc.a, tc.b, tc.max, got, tc.want)
		}
	}
}
