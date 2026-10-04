package downloads

import (
	"testing"
	"time"
)

func TestPreparationProgress(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	if p, r := preparationProgress(nil, f(100), f(2)); p != nil || r != nil {
		t.Fatal("an encode without a reported position has no progress")
	}
	if p, r := preparationProgress(f(10), f(0), f(2)); p != nil || r != nil {
		t.Fatal("an unknown duration has no progress")
	}
	if p, r := preparationProgress(f(25), f(100), nil); p == nil || *p != 0.25 || r != nil {
		t.Fatalf("no speed: %v %v", p, r)
	}
	if p, r := preparationProgress(f(25), f(100), f(3)); p == nil || *p != 0.25 || r == nil || *r != 25 {
		t.Fatalf("at 3x: %v %v", p, r)
	}
	if p, r := preparationProgress(f(120), f(100), f(3)); *p != 1 || *r != 0 {
		t.Fatalf("overshoot: %v %v", *p, *r)
	}
}

func TestAttachPreparationsPostgres(t *testing.T) {
	repo := statusEventTestRepo(t)
	if _, err := repo.pool.Exec(t.Context(), `CREATE TABLE download_artifacts(
 id text PRIMARY KEY, status text NOT NULL, created_at timestamptz NOT NULL, completed_at timestamptz, next_retry_at timestamptz,
 progress_encoded_seconds double precision, progress_duration_seconds double precision, progress_speed double precision)`); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	for i, a := range []struct {
		id, status string
		retry      *time.Time
		encoded    *float64
	}{
		{id: "running", status: "tracks_v1_running", encoded: new(300.0)},
		{id: "other-user-first", status: "tracks_v1_queued"},
		{id: "backing-off", status: "tracks_v1_queued", retry: new(time.Now().Add(time.Hour))},
		{id: "mine-second", status: "queued"},
		{id: "done", status: "tracks_v1_ready"},
	} {
		if _, err := repo.pool.Exec(t.Context(), `INSERT INTO download_artifacts(id,status,created_at,next_retry_at,progress_encoded_seconds,progress_duration_seconds,progress_speed) VALUES($1,$2,$3,$4,$5,1200,6)`,
			a.id, a.status, base.Add(time.Duration(i)*time.Minute), a.retry, a.encoded); err != nil {
			t.Fatal(err)
		}
	}
	rows := []*Download{
		{ID: "a", Status: StatusPreparing, ArtifactID: "running"},
		{ID: "b", Status: StatusPreparing, ArtifactID: "mine-second"},
		{ID: "c", Status: StatusPreparing, ArtifactID: "backing-off"},
		{ID: "d", Status: StatusPreparing, ArtifactID: "done"},
		{ID: "e", Status: StatusReady, ArtifactID: "running"},
		{ID: "f", Status: StatusPreparing},
	}
	if err := repo.attachPreparations(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
	if p := rows[0].Preparation; p == nil || p.State != PreparationRunning || p.Progress == nil || *p.Progress != 0.25 || p.RemainingSeconds == nil || *p.RemainingSeconds != 150 || p.QueuePosition != 0 {
		t.Fatalf("running %+v", p)
	}
	// Second in the whole server's queue: another account's job is ahead,
	// and a job waiting out a retry backoff is not queued.
	if p := rows[1].Preparation; p == nil || p.State != PreparationQueued || p.QueuePosition != 2 || p.Progress != nil {
		t.Fatalf("queued %+v", p)
	}
	if p := rows[2].Preparation; p == nil || p.State != PreparationRetrying || p.QueuePosition != 0 {
		t.Fatalf("retrying %+v", p)
	}
	for _, row := range rows[3:] {
		if row.Preparation != nil {
			t.Fatalf("%s: %+v", row.ID, row.Preparation)
		}
	}
}
