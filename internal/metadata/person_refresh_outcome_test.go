package metadata

import (
	"context"
	"errors"
	"slices"
	"testing"

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
