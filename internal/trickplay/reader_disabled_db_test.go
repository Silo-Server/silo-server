package trickplay

import (
	"testing"
	"time"
)

func TestReaderSuppressesDisabledLibraryAvailabilityDB(t *testing.T) {
	f := newFixture(t)
	for _, state := range []string{stateReady, stateRunning} {
		t.Run(state, func(t *testing.T) {
			folder := f.library(t, "movies", true)
			fileID := f.file(t, folder, state)
			f.reconcile(t)
			f.generate(t, fileID, "server-a")
			if state == stateRunning {
				if _, err := f.repo.Regenerate(t.Context(), []int{fileID}); err != nil {
					t.Fatal(err)
				}
				if job, err := f.repo.ClaimFile(t.Context(), fileID, "server-b", time.Minute); err != nil || job == nil {
					t.Fatalf("claim: %+v %v", job, err)
				}
			}
			reader := NewReader(f.pool, identityStore(testStore), fakeURLs{})
			if grids, err := reader.TrickplayGrids(t.Context(), []int{fileID}); err != nil || len(grids) != 1 {
				t.Fatalf("enabled availability: %+v %v", grids, err)
			}
			f.exec(t, `UPDATE public.media_folders SET enabled=false WHERE id=$1`, folder)
			if grids, err := reader.TrickplayGrids(t.Context(), []int{fileID}); err != nil || len(grids) != 0 {
				t.Errorf("disabled availability before reconcile: %+v %v", grids, err)
			}
			if _, ok, err := reader.SignedManifest(t.Context(), fileID); err != nil || ok {
				t.Errorf("disabled signed manifest: %v %v", ok, err)
			}
		})
	}
}
