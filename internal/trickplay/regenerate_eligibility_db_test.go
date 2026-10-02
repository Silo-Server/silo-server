package trickplay

import "testing"

func TestRegenerateQueuesUntrackedEligibleFilesAndSkipsOptedOutDB(t *testing.T) {
	f := newFixture(t)
	on, off := f.library(t, "movies", true), f.library(t, "movies", false)
	untracked, disabled := f.file(t, on, "untracked"), f.file(t, off, "disabled")
	f.exec(t, `INSERT INTO public.media_file_trickplay (media_file_id, recipe_version, state) VALUES ($1, $2, 'unusable')`, disabled, AlgorithmVersion)
	requeued, err := f.repo.Regenerate(t.Context(), []int{untracked, disabled})
	if err != nil || requeued != 1 {
		t.Fatalf("regenerate count=%d error=%v", requeued, err)
	}
	row, exists := f.row(t, untracked)
	if !exists || row.state != statePending || !row.due {
		t.Fatalf("eligible file not queued: %+v %v", row, exists)
	}
	row, _ = f.row(t, disabled)
	if row.state != stateUnusable {
		t.Fatalf("disabled file was requeued: %+v", row)
	}
}
