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
