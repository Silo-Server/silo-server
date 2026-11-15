package audiobooks

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

// editionProvider answers search with fixed results and GetMetadata with the
// record for the requested provider ID, like the audiobook plugin.
type editionProvider struct {
	results []metadata.SearchResult
	records map[string]*metadata.MetadataResult
	// lookupASIN answers GetMetadata for a lone asin, as a plugin that
	// declares asin in lookup_provider_ids would.
	lookupASIN bool
	// onlyFor, when set, is the one search title that finds the results.
	onlyFor string
}

func (p *editionProvider) Slug() string       { return "audiobook-metadata" }
func (p *editionProvider) Name() string       { return "audiobook-metadata" }
func (p *editionProvider) ForTypes() []string { return []string{"audiobook"} }
func (p *editionProvider) Search(_ context.Context, q metadata.SearchQuery) ([]metadata.SearchResult, error) {
	if p.onlyFor != "" && q.Title != p.onlyFor {
		return nil, nil
	}
	return p.results, nil
}

func (p *editionProvider) GetMetadata(_ context.Context, req metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	id := req.ProviderIDs["audiobook-metadata"]
	if id == "" && p.lookupASIN {
		id = req.ProviderIDs["asin"]
	}
	return p.records[id], nil
}

func edition(title, asin, author, narrator string) (metadata.SearchResult, *metadata.MetadataResult) {
	ids := map[string]string{"audiobook-metadata": asin, "asin": asin}
	return metadata.SearchResult{Name: title, ProviderIDs: ids}, &metadata.MetadataResult{
		HasMetadata: true,
		Title:       title,
		ProviderIDs: ids,
		People: []models.ItemPerson{
			{Person: models.Person{Name: author}, Kind: models.PersonKindAuthor},
			{Person: models.Person{Name: narrator}, Kind: models.PersonKindNarrator},
		},
	}
}

func TestFindMatchChecksEditionAgainstItem(t *testing.T) {
	wheaton, wheatonRec := edition("The Martian", "A0WHEATON1", "Andy Weir", "Wil Wheaton")
	bray, brayRec := edition("The Martian", "Z0BRAY0001", "Andy Weir", "R. C. Bray")
	other, otherRec := edition("Unrelated Cookbook", "B002V1O7UG", "Someone Else", "Anyone")
	_, book1Rec := edition("Dungeon Crawler Carl, Book 1", "B08V8B2CGV", "Matt Dinniman", "Jeff Hays")
	_, feralRec := edition("The Gate of the Feral Gods", "B0FERAL004", "Matt Dinniman", "Jeff Hays")
	feralRec.TitleAliases = []metadata.TitleAlias{{Title: "Dungeon Crawler Carl, Book 4"}}
	gone, goneRec := edition("Gone", "S3GONE0001", "Jo Smith", "Anne Reader")
	gone4, gone4Rec := edition("Gone: Series Name, Book 4", "S4GONE0001", "Jo Smith", "Anne Reader")
	_, aliasRec := edition("El marciano", "B079YDJ18Z", "Andy Weir", "Xavi Fernández")
	aliasRec.TitleAliases = []metadata.TitleAlias{{Title: "The Martian", Kind: "original"}}

	tests := []struct {
		name     string
		item     enrichmentItemRow
		provider *editionProvider
		wantASIN string // "" means unmatched
	}{
		{
			name: "narrator contradiction moves on to the next candidate",
			item: enrichmentItemRow{Title: "The Martian", Author: "Andy Weir", Narrators: []string{"R. C. Bray"}},
			provider: &editionProvider{
				results: []metadata.SearchResult{wheaton, bray},
				records: map[string]*metadata.MetadataResult{"A0WHEATON1": wheatonRec, "Z0BRAY0001": brayRec},
			},
			wantASIN: "Z0BRAY0001",
		},
		{
			name: "tagged ASIN is looked up first",
			item: enrichmentItemRow{Title: "ASIN Only", ProviderIDs: map[string]string{"asin": "Z0BRAY0001"}},
			provider: &editionProvider{
				lookupASIN: true,
				records:    map[string]*metadata.MetadataResult{"Z0BRAY0001": brayRec},
			},
			wantASIN: "Z0BRAY0001",
		},
		{
			name: "tagged ASIN record that contradicts the author leaves the item unmatched",
			item: enrichmentItemRow{Title: "The Martian", Author: "Andy Weir", ProviderIDs: map[string]string{"asin": "B002V1O7UG"}},
			provider: &editionProvider{
				lookupASIN: true,
				results:    []metadata.SearchResult{other, bray},
				records:    map[string]*metadata.MetadataResult{"B002V1O7UG": otherRec, "Z0BRAY0001": brayRec},
			},
		},
		{
			name: "a candidate whose fetch returns nothing gives way to the next",
			item: enrichmentItemRow{Title: "The Martian", Author: "Andy Weir", Narrators: []string{"R. C. Bray"}},
			provider: &editionProvider{
				results: []metadata.SearchResult{wheaton, bray},
				records: map[string]*metadata.MetadataResult{"Z0BRAY0001": brayRec}, // nothing for Wheaton
			},
			wantASIN: "Z0BRAY0001",
		},
		{
			name: "tagged ASIN record for another stated volume leaves the item unmatched",
			item: enrichmentItemRow{
				Title: "Dungeon Crawler Carl 2", Author: "Matt Dinniman",
				ProviderIDs: map[string]string{"asin": "B08V8B2CGV"},
			},
			provider: &editionProvider{lookupASIN: true, records: map[string]*metadata.MetadataResult{"B08V8B2CGV": book1Rec}},
		},
		{
			name: "tagged ASIN record whose alias names another volume leaves the item unmatched",
			item: enrichmentItemRow{
				Title: "Dungeon Crawler Carl 2", Author: "Matt Dinniman",
				ProviderIDs: map[string]string{"asin": "B0FERAL004"},
			},
			provider: &editionProvider{lookupASIN: true, records: map[string]*metadata.MetadataResult{"B0FERAL004": feralRec}},
		},
		{
			name: "series-prefixed title is searched and scored by its bare title",
			item: enrichmentItemRow{
				Title: "Series Name 3 - Gone", Author: "Jo Smith",
				SeriesName: "Series Name", SeriesPosition: 3,
			},
			provider: &editionProvider{
				onlyFor: "Gone",
				results: []metadata.SearchResult{gone},
				records: map[string]*metadata.MetadataResult{"S3GONE0001": goneRec},
			},
			wantASIN: "S3GONE0001",
		},
		{
			name: "series-prefixed title searched by its bare title still rejects another volume",
			item: enrichmentItemRow{
				Title: "Series Name 3 - Gone", Author: "Jo Smith",
				SeriesName: "Series Name", SeriesPosition: 3,
			},
			provider: &editionProvider{
				onlyFor: "Gone",
				results: []metadata.SearchResult{gone4},
				records: map[string]*metadata.MetadataResult{"S4GONE0001": gone4Rec},
			},
		},
		{
			name: "tagged ASIN record matching the item's title only through an alias is accepted",
			item: enrichmentItemRow{
				Title: "The Martian", Author: "Andy Weir",
				ProviderIDs: map[string]string{"asin": "B079YDJ18Z"},
			},
			provider: &editionProvider{lookupASIN: true, records: map[string]*metadata.MetadataResult{"B079YDJ18Z": aliasRec}},
			wantASIN: "B079YDJ18Z",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &Enricher{}
			tt.item.ContentID = "item"
			m := e.findMatch(context.Background(), tt.item, []metadata.Provider{tt.provider})
			got := ""
			if m.metadata.HasMetadata {
				got = m.ids["audiobook-metadata"]
			}
			if got != tt.wantASIN {
				t.Fatalf("matched %q, want %q (errors %v)", got, tt.wantASIN, m.providerErrs)
			}
		})
	}
}

func TestBareSeriesTitle(t *testing.T) {
	tests := []struct {
		title, series string
		position      int
		bare, volume  string
	}{
		{"Series Name 3 - Book Title", "Series Name", 3, "Book Title", "3"},
		{"series name 03 – Book Title", "Series Name", 3, "Book Title", "03"},
		{"Series Name, Book 3: Book Title", "Series Name", 3, "Book Title", "3"},
		{"Series Name #3 - Book Title", "Series Name", 0, "Book Title", "3"},
		{"Series Name 0.5 - Prequel Title", "Series Name", 0, "Prequel Title", "0.5"},
		{"Series Name 4 - Book Title", "Series Name", 3, "", ""},  // another position
		{"Series Name 3 - Book Title", "", 3, "", ""},             // no series
		{"Book Title", "Series Name", 3, "", ""},                  // no prefix
		{"Series Name 3", "Series Name", 3, "", ""},               // nothing after the number
		{"Series Names 3 - Book Title", "Series Name", 3, "", ""}, // another series
		{"Area 51 2 - Book Title", "Area 51", 2, "Book Title", "2"},
	}
	for _, tt := range tests {
		bare, volume := bareSeriesTitle(tt.title, tt.series, tt.position)
		if bare != tt.bare || volume != tt.volume {
			t.Errorf("bareSeriesTitle(%q, %q, %d) = %q, %q, want %q, %q", tt.title, tt.series, tt.position, bare, volume, tt.bare, tt.volume)
		}
	}
}

// crossIDProvider answers a lone asin lookup with a record that carries its
// own isbn, like a second provider that maps the ASIN to another record.
type crossIDProvider struct {
	slug string
	rec  *metadata.MetadataResult
}

func (p *crossIDProvider) Slug() string       { return p.slug }
func (p *crossIDProvider) Name() string       { return p.slug }
func (p *crossIDProvider) ForTypes() []string { return []string{"audiobook"} }
func (p *crossIDProvider) GetMetadata(context.Context, metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	return p.rec, nil
}

// Two providers answering the tagged ASIN must not both be merged when their
// records carry different cross-IDs: the second one names another record.
func TestFindMatchTaggedASINKeepsOneProvidersCrossIDs(t *testing.T) {
	record := func(isbn string) *metadata.MetadataResult {
		return &metadata.MetadataResult{
			HasMetadata: true, Title: "The Martian",
			ProviderIDs: map[string]string{"asin": "B00B5HZGUG", "isbn": isbn},
		}
	}
	first := &crossIDProvider{slug: "first", rec: record("9780553418026")}
	second := &crossIDProvider{slug: "second", rec: record("9781491544662")}
	second.rec.Overview = "from the second provider"

	e := &Enricher{}
	m := e.findMatch(context.Background(), enrichmentItemRow{
		ContentID: "item", Title: "The Martian", Author: "Andy Weir",
		ProviderIDs: map[string]string{"asin": "B00B5HZGUG"},
	}, []metadata.Provider{first, second})
	if !m.metadata.HasMetadata || m.ids["isbn"] != "9780553418026" {
		t.Fatalf("ids = %v, want the first provider's isbn", m.ids)
	}
	if m.metadata.Overview != "" {
		t.Fatalf("overview = %q, want nothing merged from the contradicting provider", m.metadata.Overview)
	}
}

func TestRedactedErrorMasksNestedCredentials(t *testing.T) {
	err := errors.New(`rpc error: code = Unavailable desc = api_key=sk-live-123 request failed: GET https://user:pw@example.test/b?token=abc`)
	got := redactProviderText(err.Error())
	for _, secret := range []string{"sk-live-123", "abc", "user:pw"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted error %q still holds %q", got, secret)
		}
	}
}

// fixedProvider answers search with results and its GetMetadata calls with
// answers in turn, repeating the last; failFirst makes the first call fail.
type fixedProvider struct {
	slug      string
	results   []metadata.SearchResult
	answers   []*metadata.MetadataResult
	failFirst bool
	calls     int
}

func (p *fixedProvider) Slug() string       { return p.slug }
func (p *fixedProvider) Name() string       { return p.slug }
func (p *fixedProvider) ForTypes() []string { return []string{"audiobook"} }
func (p *fixedProvider) Search(context.Context, metadata.SearchQuery) ([]metadata.SearchResult, error) {
	return p.results, nil
}

func (p *fixedProvider) GetMetadata(context.Context, metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	p.calls++
	n := p.calls
	if p.failFirst {
		if n == 1 {
			return nil, errors.New("provider unavailable")
		}
		n--
	}
	return p.answers[min(n, len(p.answers))-1], nil
}

// The merge asks a provider without an admitted record again once other
// providers have added IDs. A contradicting answer then leaves the item
// unmatched, unless that provider's previous answer had already failed the
// credit check: only then is its record dropped and the match kept.
func TestFindMatchMergeRefetchRules(t *testing.T) {
	_, wrongRec := edition("The Martian", "A0WHEATON1", "Andy Weir", "Wil Wheaton")
	wrong := metadata.SearchResult{Name: "The Martian", ProviderIDs: map[string]string{"first": "A0WHEATON1"}}
	right, rightRec := edition("The Martian", "Z0BRAY0001", "Andy Weir", "R. C. Bray")
	right.ProviderIDs = map[string]string{"second": "Z0BRAY0001"}
	rightRec.ProviderIDs = map[string]string{"second": "Z0BRAY0001", "isbn": "9780553418026"}
	empty := &metadata.MetadataResult{}

	tests := []struct {
		name      string
		first     metadata.Provider
		wantMatch bool
	}{
		{
			name:  "first-time contradiction leaves the item unmatched",
			first: &lookupOnly{&fixedProvider{slug: "first", answers: []*metadata.MetadataResult{wrongRec}}},
		},
		{
			name:      "a repeated rejection drops that record and keeps the match",
			first:     &fixedProvider{slug: "first", results: []metadata.SearchResult{wrong}, answers: []*metadata.MetadataResult{wrongRec}},
			wantMatch: true,
		},
		{
			name:  "an earlier empty answer then a contradiction leaves the item unmatched",
			first: &fixedProvider{slug: "first", results: []metadata.SearchResult{wrong}, answers: []*metadata.MetadataResult{empty, wrongRec}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			second := &fixedProvider{slug: "second", results: []metadata.SearchResult{right}, answers: []*metadata.MetadataResult{rightRec}}
			m := (&Enricher{}).findMatch(context.Background(), enrichmentItemRow{
				ContentID: "item", Title: "The Martian", Author: "Andy Weir", Narrators: []string{"R. C. Bray"},
			}, []metadata.Provider{tt.first, second})
			if m.metadata.HasMetadata != tt.wantMatch {
				t.Fatalf("matched = %v (ids %v), want %v", m.metadata.HasMetadata, m.ids, tt.wantMatch)
			}
			if tt.wantMatch && m.ids["second"] != "Z0BRAY0001" {
				t.Fatalf("ids = %v, want the second provider's record", m.ids)
			}
		})
	}
}

// A tagged-ASIN lookup that fails must be retried in the merge rather than
// counted as already asked.
func TestFindMatchRetriesFailedASINLookup(t *testing.T) {
	p := &fixedProvider{slug: "lookup", failFirst: true, answers: []*metadata.MetadataResult{{
		HasMetadata: true, Title: "The Way of Kings",
		ProviderIDs: map[string]string{"asin": "B003ZWFO7E", "lookup": "B003ZWFO7E"},
	}}}
	e := &Enricher{}
	m := e.findMatch(context.Background(), enrichmentItemRow{
		ContentID: "item", Title: "ASIN Only", ProviderIDs: map[string]string{"asin": "B003ZWFO7E"},
	}, []metadata.Provider{&lookupOnly{p}})
	if !m.metadata.HasMetadata || m.ids["lookup"] != "B003ZWFO7E" {
		t.Fatalf("matched %v after %d calls, want the record from the retry", m.ids, p.calls)
	}
}

// lookupOnly hides fixedProvider's Search, as a metadata-only provider.
type lookupOnly struct{ p *fixedProvider }

func (l *lookupOnly) Slug() string       { return l.p.slug }
func (l *lookupOnly) Name() string       { return l.p.slug }
func (l *lookupOnly) ForTypes() []string { return []string{"audiobook"} }
func (l *lookupOnly) GetMetadata(ctx context.Context, req metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	return l.p.GetMetadata(ctx, req)
}

type narratorRuleCase struct {
	name      string
	item      enrichmentItemRow
	results   []metadata.SearchResult
	records   map[string]*metadata.MetadataResult
	providers []metadata.Provider // overrides results and records
	wantMatch bool
	wantID    string // the audiobook-metadata ID matched, when set
	// wantNarrators are the merged record's narrators, when set.
	wantNarrators []string
}

func narratorRuleCases() []narratorRuleCase {
	bare := func(asin string) (metadata.SearchResult, *metadata.MetadataResult) {
		r, rec := edition("The Martian (Unabridged)", asin, "Andy Weir", "")
		rec.People = rec.People[:1] // author only
		return r, rec
	}
	plain, plainRec := bare("1492084861")
	// Both sort after the narrator-less "1492084861".
	bray, brayRec2 := edition("The Martian", "Z0BRAY0001", "Andy Weir", "R. C. Bray")
	wheaton, wheatonRec := edition("The Martian", "A0WHEATON1", "Andy Weir", "Wil Wheaton")
	tagged, taggedRec := bare("B00B5HZGUG")

	// The tagged-ASIN lookup answered with the provider's own ID only and
	// no narrators.
	ownIDOnly := &metadata.MetadataResult{
		HasMetadata: true, Title: "The Martian",
		ProviderIDs: map[string]string{"lookup": "L1"},
		People:      []models.ItemPerson{{Person: models.Person{Name: "Andy Weir"}, Kind: models.PersonKindAuthor}},
	}
	// A search-only provider supplies the ID, a metadata-only provider's
	// record with the right narrator establishes the match during the merge,
	// and a later metadata-only provider lists no narrators.
	_, brayRec := edition("The Martian", "Z0BRAY0001", "Andy Weir", "R. C. Bray")
	brayRec.ProviderIDs = map[string]string{"finder": "F1"}
	plainMerged := &metadata.MetadataResult{
		HasMetadata: true, Title: "The Martian", Overview: "from a source without narrators",
		ProviderIDs: map[string]string{"finder": "F1"},
	}
	finder := &searchOnly{&fixedProvider{slug: "finder", results: []metadata.SearchResult{
		{Name: "The Martian", ProviderIDs: map[string]string{"finder": "F1"}},
	}}}
	// The narrator tag holds the author, as some taggers write it; the tagged
	// ASIN names the R. C. Bray edition.
	authorAsNarrator := enrichmentItemRow{
		Title: "The Martian", Author: "Andy Weir", Narrators: []string{"Andy Weir"},
		ProviderIDs: map[string]string{"asin": "Z0BRAY0001"},
	}
	brayLookup := &editionProvider{lookupASIN: true, records: map[string]*metadata.MetadataResult{"Z0BRAY0001": brayRec2}}
	_, sameEdition := edition("The Martian", "", "Andy Weir", "R. C. Bray")
	sameEdition.ProviderIDs = map[string]string{"adds": "X1"}
	_, cookbookRec := edition("Unrelated Cookbook", "B002V1O7UG", "Andy Weir", "Anyone")
	_, siblingRec := edition("Series Name: The Well", "S2WELL0001", "Jo Smith", "Other Voice")
	// A second provider's record for the tagged edition with other narrators.
	_, otherNarrators := edition("The Martian", "", "Andy Weir", "Wil Wheaton")
	otherNarrators.ProviderIDs = map[string]string{"other": "O1"}

	return []narratorRuleCase{
		{
			name:      "narrator tag, record lists none: match",
			item:      enrichmentItemRow{Title: "The Martian", Author: "Andy Weir", Narrators: []string{"R. C. Bray"}},
			results:   []metadata.SearchResult{plain},
			records:   map[string]*metadata.MetadataResult{"1492084861": plainRec},
			wantMatch: true,
			wantID:    "1492084861",
		},
		{
			// No further fetches are made hoping for a record with narrators.
			name:      "narrator tag, a narrator-less record ranked first is taken as it is",
			item:      enrichmentItemRow{Title: "The Martian", Author: "Andy Weir", Narrators: []string{"R. C. Bray"}},
			results:   []metadata.SearchResult{plain, bray},
			records:   map[string]*metadata.MetadataResult{"1492084861": plainRec, "Z0BRAY0001": brayRec2},
			wantMatch: true,
			wantID:    "1492084861",
		},
		{
			name:    "narrator tag, narrators that disagree: no match",
			item:    enrichmentItemRow{Title: "The Martian", Author: "Andy Weir", Narrators: []string{"R. C. Bray"}},
			results: []metadata.SearchResult{wheaton},
			records: map[string]*metadata.MetadataResult{"A0WHEATON1": wheatonRec},
		},
		{
			name: "narrator tag, record carries the tagged ASIN: match",
			item: enrichmentItemRow{
				Title: "The Martian", Author: "Andy Weir", Narrators: []string{"R. C. Bray"},
				ProviderIDs: map[string]string{"asin": "B00B5HZGUG"},
			},
			results:   []metadata.SearchResult{tagged},
			records:   map[string]*metadata.MetadataResult{"B00B5HZGUG": taggedRec},
			wantMatch: true,
		},
		{
			name: "narrator tag, tagged-ASIN lookup answers with its own ID and no narrators: match",
			item: enrichmentItemRow{
				Title: "The Martian", Author: "Andy Weir", Narrators: []string{"R. C. Bray"},
				ProviderIDs: map[string]string{"asin": "B00B5HZGUG"},
			},
			providers: []metadata.Provider{&lookupOnly{&fixedProvider{slug: "lookup", answers: []*metadata.MetadataResult{ownIDOnly}}}},
			wantMatch: true,
		},
		{
			name: "narrator tag, a record merged after the match lists none: match kept",
			item: enrichmentItemRow{Title: "The Martian", Author: "Andy Weir", Narrators: []string{"R. C. Bray"}},
			providers: []metadata.Provider{
				finder,
				&lookupOnly{&fixedProvider{slug: "establishes", answers: []*metadata.MetadataResult{brayRec}}},
				&lookupOnly{&fixedProvider{slug: "adds", answers: []*metadata.MetadataResult{plainMerged}}},
			},
			wantMatch: true,
		},
		{
			name: "narrator tag, tagged-ASIN lookup fails then the merge's retry answers without narrators: match",
			item: enrichmentItemRow{
				Title: "The Martian", Author: "Andy Weir", Narrators: []string{"R. C. Bray"},
				ProviderIDs: map[string]string{"asin": "B00B5HZGUG"},
			},
			providers: []metadata.Provider{&lookupOnly{&fixedProvider{slug: "lookup", failFirst: true, answers: []*metadata.MetadataResult{ownIDOnly}}}},
			wantMatch: true,
		},
		{
			name:          "narrator tag contradicted by the tagged-ASIN lookup record: match, record's narrators kept",
			item:          authorAsNarrator,
			providers:     []metadata.Provider{brayLookup},
			wantMatch:     true,
			wantID:        "Z0BRAY0001",
			wantNarrators: []string{"R. C. Bray"},
		},
		{
			name:          "narrator tag contradicted by a search result carrying the tagged ASIN: match",
			item:          authorAsNarrator,
			results:       []metadata.SearchResult{bray},
			records:       map[string]*metadata.MetadataResult{"Z0BRAY0001": brayRec2},
			wantMatch:     true,
			wantID:        "Z0BRAY0001",
			wantNarrators: []string{"R. C. Bray"},
		},
		{
			// The merge checks later records against the tagged record's
			// narrators, not the overruled tag.
			name: "narrator tag overruled by the tagged ASIN, a later record with the same narrators: match kept",
			item: authorAsNarrator,
			providers: []metadata.Provider{
				brayLookup,
				&lookupOnly{&fixedProvider{slug: "adds", answers: []*metadata.MetadataResult{sameEdition}}},
			},
			wantMatch: true,
			wantID:    "Z0BRAY0001",
		},
		{
			name: "two tagged-ASIN records with different narrators: the first is kept, the second dropped",
			item: authorAsNarrator,
			providers: []metadata.Provider{
				brayLookup,
				&lookupOnly{&fixedProvider{slug: "other", answers: []*metadata.MetadataResult{otherNarrators}}},
			},
			wantMatch:     true,
			wantID:        "Z0BRAY0001",
			wantNarrators: []string{"R. C. Bray"},
		},
		{
			name: "narrator tag contradicted by a tagged-ASIN record with another title: no match",
			item: enrichmentItemRow{
				Title: "The Martian", Author: "Andy Weir", Narrators: []string{"Andy Weir"},
				ProviderIDs: map[string]string{"asin": "B002V1O7UG"},
			},
			providers: []metadata.Provider{&editionProvider{lookupASIN: true, records: map[string]*metadata.MetadataResult{"B002V1O7UG": cookbookRec}}},
		},
		{
			// A wrong ASIN tag naming another book of the same series: the
			// titles share the series name, but not enough to agree.
			name: "narrator tag contradicted by a tagged-ASIN record for another book in the series: no match",
			item: enrichmentItemRow{
				Title: "Series Name: The Hero", Author: "Jo Smith", Narrators: []string{"Anne Reader"},
				ProviderIDs: map[string]string{"asin": "S2WELL0001"},
			},
			providers: []metadata.Provider{&editionProvider{lookupASIN: true, records: map[string]*metadata.MetadataResult{"S2WELL0001": siblingRec}}},
		},
		{
			name: "no author tag, narrator tag contradicted by a tagged-ASIN record with another title: no match",
			item: enrichmentItemRow{
				Title: "The Martian", Narrators: []string{"Andy Weir"},
				ProviderIDs: map[string]string{"asin": "B002V1O7UG"},
			},
			providers: []metadata.Provider{&editionProvider{lookupASIN: true, records: map[string]*metadata.MetadataResult{"B002V1O7UG": cookbookRec}}},
		},
		{
			name:      "no narrator tag, record lists none: match",
			item:      enrichmentItemRow{Title: "The Martian", Author: "Andy Weir"},
			results:   []metadata.SearchResult{plain},
			records:   map[string]*metadata.MetadataResult{"1492084861": plainRec},
			wantMatch: true,
		},
	}
}

// For a narrator-tagged item, a record whose narrators disagree is rejected
// and the next candidate tried; a record that lists no narrators is no
// contradiction and is accepted as it is. A record reached through the item's
// tagged ASIN outranks the narrator tag, unless its title shares no word with
// the item's.
func TestFindMatchNarratorRules(t *testing.T) {
	for _, tt := range narratorRuleCases() {
		t.Run(tt.name, func(t *testing.T) {
			tt.item.ContentID = "item"
			providers := tt.providers
			if providers == nil {
				providers = []metadata.Provider{&editionProvider{results: tt.results, records: tt.records}}
			}
			m := (&Enricher{}).findMatch(context.Background(), tt.item, providers)
			if m.metadata.HasMetadata != tt.wantMatch {
				t.Fatalf("matched = %v (ids %v), want %v", m.metadata.HasMetadata, m.ids, tt.wantMatch)
			}
			if tt.wantID != "" && m.ids["audiobook-metadata"] != tt.wantID {
				t.Fatalf("matched %v, want %s", m.ids, tt.wantID)
			}
			if tt.wantNarrators != nil {
				var got []string
				for _, p := range m.metadata.People {
					if p.Kind == models.PersonKindNarrator {
						got = append(got, p.Name)
					}
				}
				if !slices.Equal(got, tt.wantNarrators) {
					t.Fatalf("narrators %v, want %v", got, tt.wantNarrators)
				}
			}
		})
	}
}

// Candidate retries and the merge's re-asks go through the provider gate: a
// provider that answers rate limited mid-item is not called again for that
// item, and a narrator rejection whose only provider errors are pauses leaves
// the item to be deferred rather than recorded as a no-match.
func TestFindMatchRetriesGoThroughTheProviderGate(t *testing.T) {
	wrong, wrongRec := edition("The Martian", "A0WHEATON1", "Andy Weir", "Wil Wheaton")
	other, _ := edition("The Martian", "Z0BRAY0001", "Andy Weir", "R. C. Bray")
	p := &throttleSecondFetch{editionProvider: editionProvider{
		results: []metadata.SearchResult{wrong, other},
		records: map[string]*metadata.MetadataResult{"A0WHEATON1": wrongRec},
	}}
	e := &Enricher{}
	providers := e.gate.wrap([]metadata.Provider{p})
	m := e.findMatch(context.Background(), enrichmentItemRow{
		ContentID: "item", Title: "The Martian", Author: "Andy Weir", Narrators: []string{"R. C. Bray"},
	}, providers)

	if p.fetches != 2 {
		t.Fatalf("GetMetadata calls = %d, want 2: no call after the rate-limit answer", p.fetches)
	}
	if m.metadata.HasMetadata {
		t.Fatalf("matched %v, want no match", m.ids)
	}
	var paused *providerPausedError
	if !slices.ContainsFunc(m.providerErrs, func(err error) bool { return errors.As(err, &paused) }) {
		t.Fatalf("errors = %v, want the merge's re-ask held back by the gate", m.providerErrs)
	}

	// A later item meets the paused provider only: it is deferred, not
	// charged a failure or a no-match.
	later := e.findMatch(context.Background(), enrichmentItemRow{
		ContentID: "later", Title: "The Martian", Author: "Andy Weir", Narrators: []string{"R. C. Bray"},
	}, providers)
	if _, ok := pausedOnly(later.providerErrs); !ok || later.metadata.HasMetadata {
		t.Fatalf("later item errors = %v, want paused-only so the item is deferred", later.providerErrs)
	}
}

// throttleSecondFetch answers its second GetMetadata call rate limited.
type throttleSecondFetch struct {
	editionProvider
	fetches int
}

func (p *throttleSecondFetch) GetMetadata(ctx context.Context, req metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	p.fetches++
	if p.fetches == 2 {
		return nil, status.Error(codes.ResourceExhausted, "slow down")
	}
	return p.editionProvider.GetMetadata(ctx, req)
}

// searchOnly hides fixedProvider's GetMetadata, as a search-only provider.
type searchOnly struct{ p *fixedProvider }

func (s *searchOnly) Slug() string       { return s.p.slug }
func (s *searchOnly) Name() string       { return s.p.slug }
func (s *searchOnly) ForTypes() []string { return []string{"audiobook"} }
func (s *searchOnly) Search(ctx context.Context, q metadata.SearchQuery) ([]metadata.SearchResult, error) {
	return s.p.Search(ctx, q)
}

// A tagged-ASIN record whose primary title is a translation sets the
// agreement baseline from the title that matched the item, so a later
// provider's "The Martian" still merges.
func TestFindMatchAgreementBaselineUsesTheMatchingTitle(t *testing.T) {
	spanish := &metadata.MetadataResult{
		HasMetadata: true, Title: "El marciano",
		TitleAliases: []metadata.TitleAlias{{Title: "The Martian"}},
		ProviderIDs:  map[string]string{"asin": "B079YDJ18Z", "lookup": "B079YDJ18Z"},
	}
	english := &metadata.MetadataResult{
		HasMetadata: true, Title: "The Martian", Overview: "from the second provider",
		ProviderIDs: map[string]string{"second": "S1"},
	}
	m := (&Enricher{}).findMatch(context.Background(), enrichmentItemRow{
		ContentID: "item", Title: "The Martian", Author: "Andy Weir",
		ProviderIDs: map[string]string{"asin": "B079YDJ18Z"},
	}, []metadata.Provider{
		&lookupOnly{&fixedProvider{slug: "lookup", answers: []*metadata.MetadataResult{spanish}}},
		&keyedProvider{slug: "second",
			results: []metadata.SearchResult{{Name: "The Martian", ProviderIDs: map[string]string{"second": "S1"}}},
			rec:     english},
	})
	if !m.metadata.HasMetadata || m.metadata.Overview != "from the second provider" || m.ids["second"] != "S1" {
		t.Fatalf("ids %v overview %q, want the second provider merged", m.ids, m.metadata.Overview)
	}
}

// keyedProvider answers GetMetadata only when asked with its own ID, as the
// plugin adapter does.
type keyedProvider struct {
	slug    string
	results []metadata.SearchResult
	rec     *metadata.MetadataResult
}

func (p *keyedProvider) Slug() string       { return p.slug }
func (p *keyedProvider) Name() string       { return p.slug }
func (p *keyedProvider) ForTypes() []string { return []string{"audiobook"} }
func (p *keyedProvider) Search(context.Context, metadata.SearchQuery) ([]metadata.SearchResult, error) {
	return p.results, nil
}

func (p *keyedProvider) GetMetadata(_ context.Context, req metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	if req.ProviderIDs[p.slug] == "" {
		return nil, nil
	}
	return p.rec, nil
}

// failFirstFetch answers its first GetMetadata call with err.
type failFirstFetch struct {
	editionProvider
	err     error
	fetches int
}

func (p *failFirstFetch) GetMetadata(ctx context.Context, req metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	// Like the plugin adapter, a request without the provider's own ID makes
	// no call.
	if req.ProviderIDs["audiobook-metadata"] == "" {
		return nil, nil
	}
	p.fetches++
	if p.fetches == 1 {
		return nil, p.err
	}
	return p.editionProvider.GetMetadata(ctx, req)
}

// A per-item answer such as NotFound moves on to the next candidate; a rate
// limit stops the provider for this item, leaving the next call to the gate.
func TestFindMatchCandidateFetchErrors(t *testing.T) {
	gone, _ := edition("The Martian", "A0GONE0001", "Andy Weir", "R. C. Bray")
	bray, brayRec := edition("The Martian", "Z0BRAY0001", "Andy Weir", "R. C. Bray")
	tests := []struct {
		name        string
		err         error
		wantMatch   bool
		wantFetches int
	}{
		{"NotFound tries the next candidate", status.Error(codes.NotFound, "no such book"), true, 2},
		{"a rate limit stops the provider", status.Error(codes.ResourceExhausted, "slow down"), false, 1},
		{"InvalidArgument tries the next candidate", status.Error(codes.InvalidArgument, "bad id"), true, 2},
		{"Unauthenticated stops the provider", status.Error(codes.Unauthenticated, "no key"), false, 1},
		{"PermissionDenied stops the provider", status.Error(codes.PermissionDenied, "blocked"), false, 1},
		{"Unimplemented stops the provider", status.Error(codes.Unimplemented, "no lookup"), false, 1},
		{"an untyped error stops the provider", errors.New("connection reset"), false, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &failFirstFetch{err: tt.err, editionProvider: editionProvider{
				results: []metadata.SearchResult{gone, bray},
				records: map[string]*metadata.MetadataResult{"Z0BRAY0001": brayRec},
			}}
			e := &Enricher{}
			m := e.findMatch(context.Background(), enrichmentItemRow{
				ContentID: "item", Title: "The Martian", Author: "Andy Weir",
			}, e.gate.wrap([]metadata.Provider{p}))
			if m.metadata.HasMetadata != tt.wantMatch || p.fetches != tt.wantFetches {
				t.Fatalf("matched %v after %d fetches, want %v after %d", m.metadata.HasMetadata, p.fetches, tt.wantMatch, tt.wantFetches)
			}
			if len(m.providerErrs) == 0 {
				t.Fatal("the fetch error was not kept")
			}
		})
	}
}

// searchDownProvider is an editionProvider whose title search fails upstream.
type searchDownProvider struct{ editionProvider }

func (p *searchDownProvider) Search(context.Context, metadata.SearchQuery) ([]metadata.SearchResult, error) {
	return nil, status.Error(codes.Unavailable, "search upstream down")
}

// While a source's title search is failing, a book with a tagged ASIN is
// still looked up and matched, and a book that needs the search is deferred
// once the search is paused instead of charged a failure.
func TestFindMatchLooksUpTaggedASINWhileSearchIsPaused(t *testing.T) {
	_, rec := edition("Night Ferry", "B0NIGHT001", "Ann Author", "Ned Narrator")
	provider := &searchDownProvider{editionProvider{
		lookupASIN: true,
		records:    map[string]*metadata.MetadataResult{"B0NIGHT001": rec},
	}}
	e := &Enricher{}
	chain := e.gate.wrap([]metadata.Provider{provider})
	ctx := context.Background()
	untagged := enrichmentItemRow{ContentID: "untagged", Title: "Low Water", Author: "Ann Author"}
	for range providerOutageStreak {
		e.findMatch(ctx, untagged, chain)
	}

	tagged := enrichmentItemRow{
		ContentID: "tagged", Title: "Night Ferry", Author: "Ann Author",
		ProviderIDs: map[string]string{"asin": "B0NIGHT001"},
	}
	if _, paused := e.gate.allPaused(chain); paused {
		t.Fatal("provider reported paused while its lookups still answer")
	}
	if m := e.findMatch(ctx, tagged, chain); !m.metadata.HasMetadata || m.ids["audiobook-metadata"] != "B0NIGHT001" {
		t.Fatalf("tagged book matched %q (errors %v), want its ASIN record", m.ids["audiobook-metadata"], m.providerErrs)
	}
	m := e.findMatch(ctx, untagged, chain)
	if _, ok := pausedOnly(m.providerErrs); !ok {
		t.Fatalf("untagged book errors = %v, want only the paused search", m.providerErrs)
	}
}
