package trickplay

import "testing"

func TestRegenerateFencesOlderWorkersDB(t *testing.T) {
	f := newFixture(t)
	folder := f.library(t, "movies", true)
	legacy := f.file(t, folder, "legacy-recipe")
	future := f.file(t, folder, "future-recipe")
	f.exec(t, `INSERT INTO public.media_file_trickplay (media_file_id, recipe_version, state) VALUES ($1, $2, 'unusable'), ($3, $4, 'unusable')`, legacy, AlgorithmVersion-1, future, AlgorithmVersion+1)
	if changed, err := f.repo.Regenerate(t.Context(), []int{legacy, future}); err != nil || changed != 1 {
		t.Fatalf("regenerate changed=%d error=%v", changed, err)
	}
	row, ok := f.row(t, legacy)
	if !ok || row.state != statePending || row.version != AlgorithmVersion {
		t.Errorf("regenerated legacy row=%+v found=%v", row, ok)
	}
	var olderWorkerCanClaim bool
	if err := f.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM public.media_file_trickplay WHERE media_file_id=$1 AND state='pending' AND available_at<=now() AND recipe_version<=$2)`, legacy, AlgorithmVersion-1).Scan(&olderWorkerCanClaim); err != nil {
		t.Fatal(err)
	}
	if olderWorkerCanClaim {
		t.Error("regeneration left the row claimable by an older worker")
	}
	row, ok = f.row(t, future)
	if !ok || row.state != stateUnusable || row.version != AlgorithmVersion+1 {
		t.Fatalf("regeneration changed a newer worker's row=%+v found=%v", row, ok)
	}
}
