package trickplay

import (
	"testing"
	"time"
)

func TestLibraryTypeChangeSuppressesAndRetiresTrickplayDB(t *testing.T) {
	for _, kind := range []string{"audiobooks", "ebooks", "podcasts", "manga", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			folder := f.library(t, "movies", true)
			file := f.file(t, folder, kind)
			f.reconcile(t)
			revision := f.generate(t, file, "server")
			expiry := time.Now().Add(96 * time.Hour).Truncate(time.Microsecond)
			if ok, err := f.repo.ProtectRevision(t.Context(), file, revision, expiry, testStore); err != nil || !ok {
				t.Fatalf("protect eligible publication: %v %v", ok, err)
			}
			f.exec(t, `UPDATE media_folders SET type=$2 WHERE id=$1`, folder, kind)
			reader := NewReader(f.pool, identityStore(testStore), fakeURLs{})
			if grids, err := reader.TrickplayGrids(t.Context(), []int{file}); err != nil || len(grids) != 0 {
				t.Errorf("availability after type change: %+v %v", grids, err)
			}
			if signed, ok, err := reader.SignedManifest(t.Context(), file); err != nil || ok {
				t.Errorf("manifest after type change: %+v %v %v", signed, ok, err)
			}
			for _, requested := range []time.Time{expiry.Add(-time.Hour), expiry.Add(time.Hour)} {
				if ok, err := f.repo.ProtectRevision(t.Context(), file, revision, requested, testStore); err != nil || ok {
					t.Errorf("URL protection after type change at %v: %v %v", requested, ok, err)
				}
			}
			f.reconcile(t)
			if row, ok := f.row(t, file); ok {
				t.Errorf("unsupported library publication retained: %+v", row)
			}
			var deadline time.Time
			if err := f.pool.QueryRow(t.Context(), `SELECT not_before FROM blob_gc_queue WHERE prefix=$1`, revisionPrefix(file, revision)).Scan(&deadline); err != nil {
				t.Errorf("retired revision deadline: %v", err)
			} else if deadline.Before(expiry) {
				t.Errorf("revision retires at %v before issued URL expiry %v", deadline, expiry)
			}
		})
	}
}

func TestNormalizedVideoLibraryTypesRemainEligibleDB(t *testing.T) {
	for _, kind := range []string{" Movie ", " MOVIES ", " Series ", " TV ", " Show ", " TVShows ", " MIXED "} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			file := f.file(t, f.library(t, kind, true), "alias")
			f.reconcile(t)
			revision := f.generate(t, file, "server")
			manifests, err := f.repo.Manifests(t.Context(), []int{file}, testStore)
			if err != nil || len(manifests) != 1 {
				t.Fatalf("normalized video type unavailable: %+v %v", manifests, err)
			}
			if ok, err := f.repo.ProtectRevision(t.Context(), file, revision, time.Now().Add(time.Hour), testStore); err != nil || !ok {
				t.Fatalf("normalized video type unprotected: %v %v", ok, err)
			}
			f.reconcile(t)
			if row, ok := f.row(t, file); !ok || row.state != stateReady {
				t.Fatalf("normalized video type retired: %+v %v", row, ok)
			}
		})
	}
}

func TestCurrentFileEligibilitySuppressesTrickplayDB(t *testing.T) {
	for _, change := range []struct{ name, sql string }{
		{"missing", `UPDATE media_files SET missing_since=now() WHERE id=$1`},
		{"unprobed", `UPDATE media_files SET probe_updated_at=NULL WHERE id=$1`},
		{"audio-only", `UPDATE media_files SET video_tracks='[]' WHERE id=$1`},
		{"zero-duration", `UPDATE media_files SET duration=0 WHERE id=$1`},
	} {
		t.Run(change.name, func(t *testing.T) {
			f := newFixture(t)
			file := f.file(t, f.library(t, "movies", true), change.name)
			// A short publication isolates eligibility from the existing two-second
			// source-duration tolerance when the duration is cleared to zero.
			f.exec(t, `UPDATE media_files SET duration=1 WHERE id=$1`, file)
			f.reconcile(t)
			revision := f.generate(t, file, "server")
			expiry := time.Now().Add(96 * time.Hour)
			if ok, err := f.repo.ProtectRevision(t.Context(), file, revision, expiry, testStore); err != nil || !ok {
				t.Fatalf("protect eligible file: %v %v", ok, err)
			}
			f.exec(t, change.sql, file)
			if manifests, err := f.repo.Manifests(t.Context(), []int{file}, testStore); err != nil || len(manifests) != 0 {
				t.Errorf("ineligible file manifests: %+v %v", manifests, err)
			}
			for _, requested := range []time.Time{expiry.Add(-time.Hour), expiry.Add(time.Hour)} {
				if ok, err := f.repo.ProtectRevision(t.Context(), file, revision, requested, testStore); err != nil || ok {
					t.Errorf("ineligible file URL protection: %v %v", ok, err)
				}
			}
			if row, ok := f.row(t, file); !ok || row.revision == nil || *row.revision != revision {
				t.Errorf("temporary file ineligibility displaced retained storage: %+v %v", row, ok)
			}
		})
	}
}
