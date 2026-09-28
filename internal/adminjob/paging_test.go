package adminjob

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestAdminJobPageTimestampTies(t *testing.T) {
	repo := lifecycleRepo(t)
	kind := fmt.Sprintf("paging_%d", time.Now().UnixNano())
	at := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	var expected []string
	for range 5 {
		job, err := repo.Create(t.Context(), CreateJobInput{JobType: kind, CreatedByUserID: 1, RequestPayload: map[string]string{}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.pool.Exec(t.Context(), `UPDATE admin_jobs SET status='completed',requested_at=$2 WHERE id=$1`, job.ID, at); err != nil {
			t.Fatal(err)
		}
		expected = append(expected, job.ID)
	}
	t.Cleanup(func() { _, _ = repo.pool.Exec(context.Background(), `DELETE FROM admin_jobs WHERE job_type=$1`, kind) })
	slices.Sort(expected)
	slices.Reverse(expected)
	var before time.Time
	var beforeID string
	var seen []string
	for {
		rows, err := repo.ListPage(t.Context(), kind, before, beforeID, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			break
		}
		for _, job := range rows {
			seen = append(seen, job.ID)
		}
		last := rows[len(rows)-1]
		beforeID = last.ID
		before = last.RequestedAt
	}
	if !slices.Equal(seen, expected) {
		t.Fatalf("page traversal=%v want %v", seen, expected)
	}
	if _, err := repo.ListPage(t.Context(), kind, time.Time{}, "", 202); err == nil {
		t.Fatal("unbounded limit accepted")
	}
}
