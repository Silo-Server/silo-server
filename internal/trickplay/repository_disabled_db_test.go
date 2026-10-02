package trickplay

import "testing"

func TestDisabledLibraryStopsServingAndRemovesPublishedWorkDB(t *testing.T) {
	f := newFixture(t)
	for _, state := range []string{"ready", "running"} {
		t.Run(state, func(t *testing.T) {
			folder := f.library(t, "movies", true)
			file := f.file(t, folder, state)
			f.reconcile(t)
			revision := f.generate(t, file, "server")
			if state == "running" {
				if _, err := f.repo.Regenerate(t.Context(), []int{file}); err != nil {
					t.Fatal(err)
				}
				if job, err := f.repo.ClaimFile(t.Context(), file, "server", leaseDuration); err != nil || job == nil {
					t.Fatalf("claim: %+v %v", job, err)
				}
			}
			reader := NewReader(f.pool, identityStore(testStore), fakeURLs{})
			if _, ok, err := reader.SignedManifest(t.Context(), file); err != nil || !ok {
				t.Fatalf("enabled manifest: %v %v", ok, err)
			}
			f.exec(t, `UPDATE public.media_folders SET enabled = false WHERE id = $1`, folder)
			if manifests, err := f.repo.Manifests(t.Context(), []int{file}, testStore); err != nil || len(manifests) != 0 {
				t.Fatalf("disabled manifests: %+v %v", manifests, err)
			}
			if grids, err := reader.TrickplayGrids(t.Context(), []int{file}); err != nil || len(grids) != 0 {
				t.Fatalf("disabled grids: %+v %v", grids, err)
			}
			if _, ok, err := reader.SignedManifest(t.Context(), file); err != nil || ok {
				t.Fatalf("disabled signed manifest: %v %v", ok, err)
			}
			if body, _, ok, err := reader.OpenSheet(t.Context(), file, testRecipe.Width, 0); err != nil || ok {
				if body != nil {
					_ = body.Close()
				}
				t.Fatalf("disabled sheet: %v %v", ok, err)
			}
			if state == "running" {
				f.exec(t, `UPDATE public.media_file_trickplay SET lease_expires_at = now() - interval '1 second' WHERE media_file_id = $1`, file)
			}
			f.reconcile(t)
			if _, exists := f.row(t, file); exists {
				t.Fatal("disabled library retained finished work")
			}
			queued := f.queued(t, file)
			if len(queued) != 1 || queued[0] != revisionPrefix(file, revision) {
				t.Fatalf("disabled published revision was not queued: %v", queued)
			}
		})
	}
}
