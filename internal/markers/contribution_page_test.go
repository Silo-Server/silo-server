package markers

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestContributionStorePageTiesAndFileIsolation(t *testing.T) {
	f := newContributionStoreFixture(t)
	ids := []string{}
	for i := range 4 {
		fileID := f.fileIDs[0]
		if i == 3 {
			fileID = f.fileIDs[1]
		}
		claim, ok, err := f.store.Claim(t.Context(), f.row(fileID, fmt.Sprintf("page-%d", i)), contributionClaimLease)
		if err != nil || !ok {
			t.Fatalf("seed %v %v", ok, err)
		}
		if i < 3 {
			ids = append(ids, claim.ID)
		}
	}
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
	if _, err := f.pool.Exec(t.Context(), `UPDATE marker_contributions SET updated_at=$1 WHERE provider=$2`, stamp, f.provider); err != nil {
		t.Fatal(err)
	}
	slices.Sort(ids)
	slices.Reverse(ids)
	first, more, err := f.store.ListByFilePage(t.Context(), f.fileIDs[0], 2, ContributionPagePosition{})
	if err != nil || !more || len(first) != 2 {
		t.Fatalf("first %v %v %v", first, more, err)
	}
	second, more, err := f.store.ListByFilePage(t.Context(), f.fileIDs[0], 2, ContributionPagePosition{UpdatedAt: first[1].UpdatedAt, ID: first[1].ID})
	if err != nil || more || len(second) != 1 {
		t.Fatalf("second %v %v %v", second, more, err)
	}
	got := []string{first[0].ID, first[1].ID, second[0].ID}
	if !reflect.DeepEqual(got, ids) {
		t.Fatalf("order %v want %v", got, ids)
	}
}
