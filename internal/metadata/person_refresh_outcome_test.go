package metadata

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// erroringPersonProvider fails every detail lookup with err.
type erroringPersonProvider struct {
	slug string
	err  error
}

func (p erroringPersonProvider) Slug() string       { return p.slug }
func (p erroringPersonProvider) Name() string       { return p.slug }
func (p erroringPersonProvider) ForTypes() []string { return []string{"person"} }

func (p erroringPersonProvider) GetPersonDetail(context.Context, PersonDetailRequest) (*PersonDetailResult, error) {
	return nil, p.err
}

// A lookup records what it found, so the sweep can tell a person the provider
// already answered from one it could not reach (#1606).
func TestPersonRefreshRecordsOutcome(t *testing.T) {
	answered := stubPersonProvider{slug: "tmdb", detail: &PersonDetailResult{Name: "Answered"}}
	missing := erroringPersonProvider{slug: "tmdb", err: errors.New("tmdb: HTTP 404: not found")}
	broken := erroringPersonProvider{slug: "tvdb", err: errors.New("tvdb: context deadline exceeded")}
	unsupported := erroringPersonProvider{slug: "anidb", err: ErrPersonDetailUnsupported}

	for _, tc := range []struct {
		name      string
		providers []Provider
		want      catalog.PersonRefreshOutcome
	}{
		{name: "provider returns the person", providers: []Provider{answered}, want: catalog.PersonRefreshAnswered},
		// An empty answer still answers: a person with no bio isn't retried for it.
		{name: "answer with empty fields", providers: []Provider{stubPersonProvider{slug: "tmdb", detail: &PersonDetailResult{}}}, want: catalog.PersonRefreshAnswered},
		{name: "one provider answers, another fails", providers: []Provider{broken, answered}, want: catalog.PersonRefreshAnswered},
		{name: "provider has nothing", providers: []Provider{stubPersonProvider{slug: "tmdb"}}, want: catalog.PersonRefreshNotFound},
		{name: "provider reports 404", providers: []Provider{missing}, want: catalog.PersonRefreshNotFound},
		{name: "provider errors", providers: []Provider{broken}, want: catalog.PersonRefreshFailed},
		{name: "404 and an error", providers: []Provider{missing, broken}, want: catalog.PersonRefreshFailed},
		// No person provider enabled says nothing about the person, so they
		// mustn't count toward giving up on them.
		{name: "no person provider", providers: nil, want: catalog.PersonRefreshFailed},
		// A metadata plugin without person lookup isn't a provider that
		// looked the person up.
		{name: "only unsupported providers", providers: []Provider{unsupported}, want: catalog.PersonRefreshFailed},
		{name: "unsupported and 404", providers: []Provider{unsupported, missing}, want: catalog.PersonRefreshNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakePersonRefreshRepo(models.Person{ID: 7, Name: "Person", TmdbID: "7"})
			service := &PersonRefreshService{repo: repo}
			_, _ = service.refreshPersonWithProviders(context.Background(), 7, tc.providers)
			if !slices.Equal(repo.outcomes, []catalog.PersonRefreshOutcome{tc.want}) {
				t.Fatalf("recorded outcomes = %v, want [%s]", repo.outcomes, tc.want)
			}
		})
	}
}

// An answer that can't be stored records a failure, so the sweep backs off
// instead of retrying it every time the attempt's lease runs out.
func TestPersonRefreshRecordsFailureWhenAnswerCannotBeStored(t *testing.T) {
	for _, tc := range []struct {
		name      string
		detail    *PersonDetailResult
		updateErr error
	}{
		{name: "unparseable birth date", detail: &PersonDetailResult{Name: "Person", BirthDate: "not a date"}},
		{name: "update fails", detail: &PersonDetailResult{Name: "Person"}, updateErr: errors.New("connection reset")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakePersonRefreshRepo(models.Person{ID: 9, Name: "Person", TmdbID: "9"})
			repo.updateErr = tc.updateErr
			service := &PersonRefreshService{repo: repo}
			_, err := service.refreshPersonWithProviders(context.Background(), 9, []Provider{
				stubPersonProvider{slug: "tmdb", detail: tc.detail},
			})
			if err == nil {
				t.Fatal("refresh succeeded, want an error")
			}
			if !slices.Equal(repo.outcomes, []catalog.PersonRefreshOutcome{catalog.PersonRefreshFailed}) {
				t.Fatalf("recorded outcomes = %v, want [failed]", repo.outcomes)
			}
		})
	}
}

// A claimed person whose lookup already started since the claim, such as from
// a person page on another node, isn't looked up again.
func TestRefreshClaimedPersonSkipsLookupStartedSinceClaim(t *testing.T) {
	claimedAt := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	after := claimedAt.Add(time.Second)
	repo := newFakePersonRefreshRepo(models.Person{ID: 10, Name: "Person", TmdbID: "10", MetadataRefreshAttemptedAt: &after})
	service := &PersonRefreshService{repo: repo}

	person, err := service.RefreshClaimedPerson(context.Background(), 10, claimedAt)
	if err != nil || person == nil || person.ID != 10 {
		t.Fatalf("RefreshClaimedPerson = %v, %v; want the stored person", person, err)
	}
	if len(repo.refreshAttempts) != 0 || len(repo.outcomes) != 0 {
		t.Fatalf("looked up again: attempts %v, outcomes %v", repo.refreshAttempts, repo.outcomes)
	}

	// An attempt from before the claim doesn't count: the lookup goes ahead
	// (and fails here, with no providers configured).
	before := claimedAt.Add(-time.Hour)
	repo.persons[10] = models.Person{ID: 10, Name: "Person", TmdbID: "10", MetadataRefreshAttemptedAt: &before}
	if _, err := service.RefreshClaimedPerson(context.Background(), 10, claimedAt); err == nil {
		t.Fatal("RefreshClaimedPerson skipped a lookup that was due")
	}
}

// A refresh that ran out of time still records its failure, so the sweep backs
// off instead of treating the person as never looked up.
func TestPersonRefreshRecordsOutcomeAfterTimeout(t *testing.T) {
	repo := newFakePersonRefreshRepo(models.Person{ID: 8, Name: "Slow", TmdbID: "8"})
	service := &PersonRefreshService{repo: repo}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = service.refreshPersonWithProviders(ctx, 8, []Provider{
		erroringPersonProvider{slug: "tmdb", err: context.Canceled},
	})
	if !slices.Equal(repo.outcomes, []catalog.PersonRefreshOutcome{catalog.PersonRefreshFailed}) {
		t.Fatalf("recorded outcomes = %v, want [failed]", repo.outcomes)
	}
}

// A lookup no provider answered because one was rate limiting says so, so the
// worker can back off; it still reads as ErrPersonMetadataNotFound (#1606).
func TestPersonRefreshReportsRateLimit(t *testing.T) {
	limited := erroringPersonProvider{slug: "tmdb", err: errors.New("tmdb: HTTP 429: too many requests")}

	repo := newFakePersonRefreshRepo(models.Person{ID: 11, Name: "Limited", TmdbID: "11"})
	service := &PersonRefreshService{repo: repo}
	_, err := service.refreshPersonWithProviders(context.Background(), 11, []Provider{limited})
	var rateLimited *PersonLookupRateLimitedError
	if !errors.As(err, &rateLimited) || !errors.Is(err, ErrPersonMetadataNotFound) {
		t.Fatalf("error = %v, want a PersonLookupRateLimitedError that is ErrPersonMetadataNotFound", err)
	}
	if !slices.Equal(repo.outcomes, []catalog.PersonRefreshOutcome{catalog.PersonRefreshFailed}) {
		t.Fatalf("recorded outcomes = %v, want [failed]", repo.outcomes)
	}

	// Another provider's answer still makes the lookup a success.
	repo = newFakePersonRefreshRepo(models.Person{ID: 12, Name: "Answered", TmdbID: "12"})
	service = &PersonRefreshService{repo: repo}
	answered := stubPersonProvider{slug: "tvdb", detail: &PersonDetailResult{Name: "Answered"}}
	if _, err := service.refreshPersonWithProviders(context.Background(), 12, []Provider{limited, answered}); err != nil {
		t.Fatalf("error = %v, want nil when another provider answered", err)
	}
}
