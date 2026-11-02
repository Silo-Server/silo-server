package metadata

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// stubPersonRepo resolves every credit to ids, or fails with err.
type stubPersonRepo struct {
	ids []int64
	err error
}

func (r stubPersonRepo) BatchFindOrCreate(_ context.Context, people []models.Person) ([]int64, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.ids[:len(people)], nil
}

func (stubPersonRepo) PhotoStates(context.Context, []int64) (map[int64]catalog.PersonPhotoState, error) {
	return nil, nil
}

func (stubPersonRepo) ClaimPhotoSources(context.Context, []catalog.PersonPhotoSourceClaim) ([]int64, error) {
	return nil, nil
}

// A refresh whose people cannot be resolved keeps the stored cast instead of
// replacing it with the credits that already had IDs (none, for fresh
// provider credits).
func TestMergeAndPersistKeepsPeopleWhenResolutionFails(t *testing.T) {
	for _, tc := range []struct {
		name        string
		repo        stubPersonRepo
		wantReplace bool
	}{
		{"resolved", stubPersonRepo{ids: []int64{42}}, true},
		{"resolution failed", stubPersonRepo{err: errors.New("person lookup cut short")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHarness()
			h.service.personRepo = tc.repo
			ctx := t.Context()
			if err := h.itemRepo.Upsert(ctx, &models.MediaItem{ContentID: "movie-cast", Type: "movie", Title: "Film", Year: 2020}); err != nil {
				t.Fatal(err)
			}
			result := &MetadataResult{
				HasMetadata: true, Title: "Film", Year: 2020, ProviderIDs: map[string]string{},
				People: []models.ItemPerson{{Person: models.Person{Name: "Lead Actor"}, Kind: models.PersonKindActor, Character: "Hero"}},
			}
			if _, err := h.service.mergeAndPersist(ctx, ProcessRequest{ContentID: "movie-cast", Mode: ModeScheduledRefresh, Language: "en"},
				result, nil, nil, nil, "movie"); err != nil {
				t.Fatalf("mergeAndPersist: %v", err)
			}
			got, replaced := h.itemRepo.replacedPeople["movie-cast"]
			if replaced != tc.wantReplace {
				t.Fatalf("ReplacePeople called = %v, want %v (people %+v)", replaced, tc.wantReplace, got)
			}
			if tc.wantReplace && (len(got) != 1 || got[0].ID != 42) {
				t.Fatalf("replaced people = %+v, want the one credit with ID 42", got)
			}
		})
	}
}
