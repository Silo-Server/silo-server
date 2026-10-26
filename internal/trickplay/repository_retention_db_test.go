package trickplay

import (
	"testing"
	"time"
)

func TestRevisionProtectionIsMonotonicAndFollowsTheRevisionDB(t *testing.T) {
	f := newFixture(t)
	file := f.file(t, f.library(t, "movies", true), "protected-revision")
	f.reconcile(t)
	revision := f.generate(t, file, "server")
	expiresAt := time.Now().Add(96 * time.Hour).Truncate(time.Microsecond).Add(123 * time.Nanosecond)
	wantExpiry := expiresAt.Truncate(time.Microsecond).Add(time.Microsecond)
	for _, expiry := range []time.Time{expiresAt, expiresAt.Add(-24 * time.Hour)} {
		if ok, err := f.repo.ProtectRevision(t.Context(), file, revision, expiry, testStore); err != nil || !ok {
			t.Fatalf("protect: %v %v", ok, err)
		}
	}
	var savedExpiry time.Time
	if err := f.pool.QueryRow(t.Context(), `SELECT published_expires_at FROM public.media_file_trickplay WHERE media_file_id = $1`, file).Scan(&savedExpiry); err != nil {
		t.Fatal(err)
	}
	if savedExpiry.Before(expiresAt) || !savedExpiry.Equal(wantExpiry) {
		t.Fatalf("saved expiry %v does not cover issued expiry %v at database precision %v", savedExpiry, expiresAt, wantExpiry)
	}
	if _, err := f.repo.Regenerate(t.Context(), []int{file}); err != nil {
		t.Fatal(err)
	}
	f.generate(t, file, "server")
	if ok, err := f.repo.ProtectRevision(t.Context(), file, revision, expiresAt.Add(24*time.Hour), testStore); err != nil || ok {
		t.Fatalf("displaced revision protection accepted: %v %v", ok, err)
	}
	var newExpiry *time.Time
	if err := f.pool.QueryRow(t.Context(), `SELECT published_expires_at FROM public.media_file_trickplay WHERE media_file_id = $1`, file).Scan(&newExpiry); err != nil {
		t.Fatal(err)
	}
	if newExpiry != nil {
		t.Fatalf("replacement inherited old URL expiry: %v", newExpiry)
	}
	var notBefore time.Time
	if err := f.pool.QueryRow(t.Context(), `SELECT not_before FROM public.blob_gc_queue WHERE prefix = $1`, revisionPrefix(file, revision)).Scan(&notBefore); err != nil {
		t.Fatal(err)
	}
	if notBefore.Before(expiresAt) {
		t.Fatalf("revision retires at %v before protected expiry %v", notBefore, expiresAt)
	}
}
