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
