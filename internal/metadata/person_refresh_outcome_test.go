package metadata

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
	pluginMissing := erroringPersonProvider{slug: "plugin", err: status.Error(codes.NotFound, "no such person")}

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
		{name: "plugin reports NotFound", providers: []Provider{pluginMissing}, want: catalog.PersonRefreshNotFound},
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

// A person whose lookup already started since the claim or page request,
// such as on another node, isn't looked up again.
func TestRefreshPersonSinceSkipsLookupStartedSince(t *testing.T) {
	claimedAt := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	after := claimedAt.Add(time.Second)
	repo := newFakePersonRefreshRepo(models.Person{ID: 10, Name: "Person", TmdbID: "10", MetadataRefreshAttemptedAt: &after})
	service := &PersonRefreshService{repo: repo}

	answered := stubPersonProvider{slug: "tmdb", detail: &PersonDetailResult{Name: "Answered"}}
	person, err := service.refreshPersonSince(context.Background(), 10, []Provider{answered}, claimedAt)
	if err != nil || person == nil || person.Name != "Person" {
		t.Fatalf("refreshPersonSince = %v, %v; want the stored person", person, err)
	}
	if len(repo.refreshAttempts) != 0 || len(repo.outcomes) != 0 {
		t.Fatalf("looked up again: attempts %v, outcomes %v", repo.refreshAttempts, repo.outcomes)
	}

	// An attempt from before the claim doesn't count: the lookup goes ahead.
	before := claimedAt.Add(-time.Hour)
	repo.persons[10] = models.Person{ID: 10, Name: "Person", TmdbID: "10", MetadataRefreshAttemptedAt: &before}
	person, err = service.refreshPersonSince(context.Background(), 10, []Provider{answered}, claimedAt)
	if err != nil || person == nil || person.Name != "Answered" {
		t.Fatalf("refreshPersonSince = %v, %v; want the looked-up person", person, err)
	}
	if !slices.Equal(repo.outcomes, []catalog.PersonRefreshOutcome{catalog.PersonRefreshAnswered}) {
		t.Fatalf("recorded outcomes = %v, want [answered]", repo.outcomes)
	}
}

// When the gate can't be checked, the lookup doesn't go ahead: another node
// may hold it.
func TestRefreshPersonSinceSkipsLookupWhenGateFails(t *testing.T) {
	repo := newFakePersonRefreshRepo(models.Person{ID: 11, Name: "Person", TmdbID: "11"})
	repo.refreshAttemptErr = errors.New("connection reset")
	service := &PersonRefreshService{repo: repo}
	answered := stubPersonProvider{slug: "tmdb", detail: &PersonDetailResult{Name: "Answered"}}

	if _, err := service.refreshPersonSince(context.Background(), 11, []Provider{answered}, time.Now()); err == nil {
		t.Fatal("refreshPersonSince succeeded, want the gate error")
	}
	if len(repo.outcomes) != 0 {
		t.Fatalf("recorded outcomes = %v, want none", repo.outcomes)
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

// hookPersonProvider answers after running hook, standing in for whatever
// happens while the lookup is out.
type hookPersonProvider struct {
	hook   func()
	detail *PersonDetailResult
}

func (p hookPersonProvider) Slug() string       { return "tmdb" }
func (p hookPersonProvider) Name() string       { return "tmdb" }
func (p hookPersonProvider) ForTypes() []string { return []string{"person"} }

func (p hookPersonProvider) GetPersonDetail(context.Context, PersonDetailRequest) (*PersonDetailResult, error) {
	p.hook()
	return p.detail, nil
}

// An admin's id correction made during the lookup wins: the answer, about the
// old id, is neither stored nor recorded as an outcome.
func TestPersonRefreshKeepsAnIDCorrectionMadeDuringTheLookup(t *testing.T) {
	repo := newFakePersonRefreshRepo(models.Person{ID: 7, Name: "Person", TmdbID: "7"})
	corrected := models.Person{ID: 7, Name: "Person", TmdbID: "70"}
	provider := hookPersonProvider{
		hook:   func() { repo.persons[7] = corrected },
		detail: &PersonDetailResult{Name: "Person", Bio: "About the old id"},
	}
	service := &PersonRefreshService{repo: repo}

	got, err := service.refreshPersonWithProviders(context.Background(), 7, []Provider{provider})
	if err != nil {
		t.Fatal(err)
	}
	if got.TmdbID != "70" || got.Bio != "" {
		t.Fatalf("returned %+v, want the corrected person unchanged", got)
	}
	if repo.persons[7] != corrected {
		t.Fatalf("stored %+v, want the correction kept", repo.persons[7])
	}
	if len(repo.outcomes) != 0 {
		t.Fatalf("recorded outcomes %v, want none", repo.outcomes)
	}
}

// The answered outcome is recorded for the identity the update stored, which
// resolving an id conflict can change from the refreshed person's.
func TestPersonRefreshRecordsTheStoredIdentity(t *testing.T) {
	repo := newFakePersonRefreshRepo(models.Person{ID: 7, Name: "Person", TmdbID: "7"})
	stored := catalog.PersonIdentity{TmdbID: "7", ImdbID: "nm-restored"}
	repo.storedIdentity = &stored
	service := &PersonRefreshService{repo: repo}

	got, err := service.refreshPersonWithProviders(context.Background(), 7, []Provider{
		stubPersonProvider{slug: "tmdb", detail: &PersonDetailResult{Name: "Person"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(repo.identities, []catalog.PersonIdentity{stored}) {
		t.Fatalf("recorded identities %v, want [%v]", repo.identities, stored)
	}
	if catalog.PersonIdentityOf(*got) != stored {
		t.Fatalf("returned identity %v, want %v", catalog.PersonIdentityOf(*got), stored)
	}
}
