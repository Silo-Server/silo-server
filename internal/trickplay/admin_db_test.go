package trickplay

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestAdminDB(t *testing.T) {
	f := newFixture(t)
	on, off := f.library(t, "movies", true), f.library(t, "movies", false)
	ready, pending := f.file(t, on, "ready"), f.file(t, on, "pending")
	offFile := f.file(t, off, "off")
	contentID := fmt.Sprintf("movie:trickplay-admin-%d", ready)
	f.exec(t, `UPDATE public.media_files SET content_id = $1 WHERE id = ANY($2)`, contentID, []int{ready, pending})
	f.exec(t, `UPDATE public.media_files SET content_id = $1 WHERE id = $2`, contentID+"-off", offFile)
	f.reconcile(t)
	f.generate(t, ready, "server-a")

	kicks := 0
	admin := NewAdmin(f.pool, identityStore(testStore), func() { kicks++ })
	files, err := admin.ItemStatus(t.Context(), contentID)
	if err != nil || len(files) != 2 {
		t.Fatalf("status %+v %v", files, err)
	}
	if files[0].FileID != ready || files[0].State != stateReady || !files[0].Servable || files[0].ThumbnailCount != 360 || files[0].SheetBytes == 0 {
		t.Fatalf("ready file %+v", files[0])
	}
	if files[1].State != statePending || files[1].Servable {
		t.Fatalf("pending file %+v", files[1])
	}
	if offStatus, err := admin.ItemStatus(t.Context(), contentID+"-off"); err != nil || offStatus[0].State != "off" {
		t.Fatalf("off file %+v %v", offStatus, err)
	}

	requeued, err := admin.Regenerate(t.Context(), contentID)
	if err != nil || requeued != 2 || kicks != 1 {
		t.Fatalf("regenerate %d %v kicks %d", requeued, err, kicks)
	}
	if _, err := admin.Regenerate(t.Context(), contentID+"-off"); !errors.Is(err, ErrNotOptedIn) {
		t.Fatalf("off library: %v", err)
	}
	if _, err := admin.Regenerate(t.Context(), "movie:nothing-here"); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("unknown item: %v", err)
	}

	libraries, err := admin.LibraryStatuses(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var found *LibraryStatus
	for i := range libraries {
		if libraries[i].LibraryID == on {
			found = &libraries[i]
		}
		if libraries[i].LibraryID == off {
			t.Fatal("a library that does not generate previews is listed")
		}
	}
	if found == nil || found.Pending != 2 || found.SheetBytes == 0 {
		t.Fatalf("library %+v", found)
	}
}

func TestAdminRetryKeepsPublishedPreviewsDB(t *testing.T) {
	f := newFixture(t)
	for _, retry := range []struct {
		name    string
		outcome Outcome
		state   string
	}{{"failed", Failed, statePending}, {"unusable", Unusable, stateUnusable}} {
		t.Run(retry.name, func(t *testing.T) {
			folder := f.library(t, "movies", true)
			fileID := f.file(t, folder, retry.name)
			contentID := fmt.Sprintf("movie:admin-retry-%d", fileID)
			f.exec(t, `UPDATE public.media_files SET content_id=$1 WHERE id=$2`, contentID, fileID)
			f.reconcile(t)
			revision := f.generate(t, fileID, "server-a")
			if _, err := f.repo.Regenerate(t.Context(), []int{fileID}); err != nil {
				t.Fatal(err)
			}
			job, err := f.repo.ClaimFile(t.Context(), fileID, "server-b", time.Minute)
			if err != nil || job == nil {
				t.Fatalf("claim: %+v %v", job, err)
			}
			if finished, err := f.repo.Finish(t.Context(), fileID, job.LeaseToken, retry.outcome, "ffmpeg sampling failed (no_stream)", 0); err != nil || !finished {
				t.Fatalf("finish: %v %v", finished, err)
			}
			reader := NewReader(f.pool, identityStore(testStore), fakeURLs{})
			manifest, ok, err := reader.SignedManifest(t.Context(), fileID)
			if err != nil || !ok || manifest.Revision != revision {
				t.Fatalf("retained manifest: %+v %v %v", manifest, ok, err)
			}
			status, err := NewAdmin(f.pool, identityStore(testStore), nil).ItemStatus(t.Context(), contentID)
			if err != nil || len(status) != 1 || status[0].State != retry.state || !status[0].Servable || status[0].Failures != 1 || status[0].LastError == "" {
				t.Fatalf("retained publication status: %+v %v", status, err)
			}
		})
	}
}

func TestAdminFollowsCurrentLibrarySettingDB(t *testing.T) {
	f := newFixture(t)
	folder := f.library(t, "movies", true)
	fileID := f.file(t, folder, "before-reconcile")
	contentID := fmt.Sprintf("movie:admin-current-%d", fileID)
	f.exec(t, `UPDATE public.media_files SET content_id=$1 WHERE id=$2`, contentID, fileID)
	admin := NewAdmin(f.pool, identityStore(testStore), nil)
	status, err := admin.ItemStatus(t.Context(), contentID)
	if err != nil || len(status) != 1 || status[0].State != statePending {
		t.Fatalf("newly opted-in file: %+v %v", status, err)
	}
	if requeued, err := admin.Regenerate(t.Context(), contentID); err != nil || requeued != 1 {
		t.Fatalf("regenerate before reconcile: %d %v", requeued, err)
	}
	f.generate(t, fileID, "server-a")
	if _, err := f.repo.Regenerate(t.Context(), []int{fileID}); err != nil {
		t.Fatal(err)
	}
	if job, err := f.repo.ClaimFile(t.Context(), fileID, "server-b", time.Hour); err != nil || job == nil {
		t.Fatalf("claim: %+v %v", job, err)
	}
	f.exec(t, `UPDATE public.media_folders SET trickplay_enabled=false WHERE id=$1`, folder)
	if _, err := admin.Regenerate(t.Context(), contentID); !errors.Is(err, ErrNotOptedIn) {
		t.Fatalf("disabled running file: %v", err)
	}
	status, err = admin.ItemStatus(t.Context(), contentID)
	if err != nil || len(status) != 1 || status[0].State != "off" || status[0].Servable {
		t.Fatalf("off status: %+v %v", status, err)
	}
}

func TestAdminCountsUnreconciledEligibleFilesDB(t *testing.T) {
	f := newFixture(t)
	folder := f.library(t, "movies", true)
	f.file(t, folder, "pending-one")
	f.file(t, folder, "pending-two")
	unprobed := f.file(t, folder, "unprobed")
	f.exec(t, `UPDATE public.media_files SET probe_updated_at=NULL WHERE id=$1`, unprobed)
	missing := f.file(t, folder, "missing")
	f.exec(t, `UPDATE public.media_files SET missing_since=now() WHERE id=$1`, missing)
	noVideo := f.file(t, folder, "no-video")
	f.exec(t, `UPDATE public.media_files SET video_tracks='[]'::jsonb WHERE id=$1`, noVideo)
	libraries, err := NewAdmin(f.pool, identityStore(testStore), nil).LibraryStatuses(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, library := range libraries {
		if library.LibraryID == folder {
			if library.Pending != 2 || library.Ready != 0 || library.Running != 0 || library.Unusable != 0 {
				t.Fatalf("unreconciled library: %+v", library)
			}
			return
		}
	}
	t.Fatal("opted-in library absent")
}

func TestAdminReclassifiesTrackedIneligibleFilesDB(t *testing.T) {
	for _, change := range []struct {
		name string
		sql  string
	}{
		{"missing", "missing_since=now()"},
		{"unprobed", "probe_updated_at=NULL"},
		{"zero-duration", "duration=0"},
		{"audio-only", "video_tracks='[]'::jsonb"},
	} {
		for _, state := range []string{stateReady, statePending, stateRunning, stateUnusable} {
			t.Run(change.name+"/"+state, func(t *testing.T) {
				f := newFixture(t)
				folder := f.library(t, "movies", true)
				file := f.file(t, folder, "tracked")
				content := fmt.Sprintf("movie:tracked-ineligible-%d", file)
				f.exec(t, `UPDATE public.media_files SET content_id=$1 WHERE id=$2`, content, file)
				f.reconcile(t)
				revision := f.generate(t, file, "server-a")
				if state != stateReady {
					if count, err := f.repo.Regenerate(t.Context(), []int{file}); err != nil || count != 1 {
						t.Fatalf("requeue publication: %d %v", count, err)
					}
					if state != statePending {
						job, err := f.repo.ClaimFile(t.Context(), file, "server-b", time.Minute)
						if err != nil || job == nil {
							t.Fatalf("claim regeneration: %+v %v", job, err)
						}
						if state == stateUnusable {
							if finished, err := f.repo.Finish(t.Context(), file, job.LeaseToken, Unusable, "no video stream", 0); err != nil || !finished {
								t.Fatalf("finish regeneration: %v %v", finished, err)
							}
						}
					}
				}

				// An ineligible file that was never tracked remains outside the
				// aggregate, unlike a tracked file whose eligibility changes.
				untracked := f.file(t, folder, "untracked")
				f.exec(t, `UPDATE public.media_files SET `+change.sql+` WHERE id=$1`, untracked)
				admin := NewAdmin(f.pool, identityStore(testStore), nil)
				readLibrary := func() LibraryStatus {
					t.Helper()
					libraries, err := admin.LibraryStatuses(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					for _, library := range libraries {
						if library.LibraryID == folder {
							return library
						}
					}
					t.Fatal("opted-in library absent")
					return LibraryStatus{}
				}
				before := readLibrary()
				if before.Pending+before.Running+before.Ready+before.Unusable != 1 || before.SheetBytes != 620_000 {
					t.Fatalf("eligible tracked publication: %+v", before)
				}
				if state == stateReady && before.Ready != 1 {
					t.Fatalf("ready publication: %+v", before)
				}
				f.exec(t, `UPDATE public.media_files SET `+change.sql+` WHERE id=$1`, file)
				assertCurrentStatus := func() {
					t.Helper()
					files, err := admin.ItemStatus(t.Context(), content)
					if err != nil || len(files) != 1 || files[0].State != stateUnusable {
						t.Fatalf("current ineligible item status: %+v %v", files, err)
					}
					library := readLibrary()
					if library.Pending != 0 || library.Running != 0 || library.Ready != 0 || library.Unusable != 1 || library.SheetBytes != before.SheetBytes {
						t.Fatalf("current ineligible library status: %+v; item status: %+v", library, files[0])
					}
				}
				assertCurrentStatus()
				if row, ok := f.row(t, file); !ok || row.state != state || row.revision == nil || *row.revision != revision {
					t.Fatalf("status read altered persisted generation state: %+v %v", row, ok)
				}
				f.reconcile(t)
				assertCurrentStatus()
				if _, ok := f.row(t, untracked); ok {
					t.Fatal("never-tracked ineligible file entered the queue")
				}
			})
		}
	}
}

func TestAdminRejectsUnsupportedLibraryTypesDB(t *testing.T) {
	for _, libraryType := range []string{"audiobooks", " ebooks ", "podcasts", "unknown"} {
		t.Run(libraryType, func(t *testing.T) {
			f := newFixture(t)
			folder := f.library(t, "movies", true)
			file := f.file(t, folder, "changed-library-type")
			content := fmt.Sprintf("movie:admin-library-type-%d", file)
			f.exec(t, `UPDATE public.media_files SET content_id=$1 WHERE id=$2`, content, file)
			f.reconcile(t)
			f.generate(t, file, "server-a")
			f.exec(t, `UPDATE public.media_folders SET type=$1 WHERE id=$2`, libraryType, folder)
			admin := NewAdmin(f.pool, identityStore(testStore), nil)
			files, err := admin.ItemStatus(t.Context(), content)
			if err != nil || len(files) != 1 || files[0].State != "off" {
				t.Errorf("unsupported library item status: %+v %v", files, err)
			}
			if _, err := admin.Regenerate(t.Context(), content); !errors.Is(err, ErrNotOptedIn) {
				t.Errorf("unsupported library regenerate: %v", err)
			}
			libraries, err := admin.LibraryStatuses(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, library := range libraries {
				if library.LibraryID == folder {
					t.Errorf("unsupported library listed: %+v", library)
				}
			}
		})
	}
}

func TestAdminResolvesMultiEpisodeFileDB(t *testing.T) {
	f := newFixture(t)
	folder := f.library(t, "tv", true)
	fileID := f.file(t, folder, "multi-episode")
	seriesID := fmt.Sprintf("series:admin-range-%d", fileID)
	f.exec(t, `INSERT INTO public.media_items(content_id,type,title,genres) VALUES($1,'series','Range test','{}')`, seriesID)
	first, second, outside := seriesID+":e1", seriesID+":e2", seriesID+":e3"
	for i, episode := range []string{first, second, outside} {
		f.exec(t, `INSERT INTO public.episodes(content_id,series_id,season_number,episode_number,title) VALUES($1,$2,1,$3,'Episode')`, episode, seriesID, i+1)
	}
	t.Cleanup(func() {
		if _, err := f.pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM public.media_items WHERE content_id=$1`, seriesID); err != nil {
			t.Error(err)
		}
	})
	f.exec(t, `UPDATE public.media_files SET content_id=$1,episode_id=$2,multi_episode_start=1,multi_episode_end=2 WHERE id=$3`, seriesID, first, fileID)
	admin := NewAdmin(f.pool, identityStore(testStore), nil)
	status, err := admin.ItemStatus(t.Context(), second)
	if err != nil || len(status) != 1 || status[0].FileID != fileID {
		t.Fatalf("covered episode: %+v %v", status, err)
	}
	if count, err := admin.Regenerate(t.Context(), second); err != nil || count != 1 {
		t.Fatalf("covered episode regenerate: %d %v", count, err)
	}
	if _, err := admin.ItemStatus(t.Context(), outside); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("outside range: %v", err)
	}
	f.exec(t, `UPDATE public.media_files SET missing_since=now() WHERE id=$1`, fileID)
	for _, episode := range []string{first, second} {
		status, err := admin.ItemStatus(t.Context(), episode)
		if err != nil || len(status) != 1 || status[0].FileID != fileID || status[0].State != stateUnusable || status[0].Servable {
			t.Errorf("missing covered episode %s: %+v %v", episode, status, err)
		}
		if count, err := admin.Regenerate(t.Context(), episode); err != nil || count != 0 {
			t.Errorf("missing covered episode regenerate %s: %d %v", episode, count, err)
		}
	}
	if _, err := admin.ItemStatus(t.Context(), outside); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("outside missing range: %v", err)
	}
}

func TestAdminTreatsDisabledLibraryAsOffDB(t *testing.T) {
	f := newFixture(t)
	folder := f.library(t, "movies", true)
	fileID := f.file(t, folder, "disabled")
	contentID := fmt.Sprintf("movie:admin-disabled-%d", fileID)
	f.exec(t, `UPDATE public.media_files SET content_id=$1 WHERE id=$2`, contentID, fileID)
	f.reconcile(t)
	f.generate(t, fileID, "server-a")
	f.exec(t, `UPDATE public.media_folders SET enabled=false WHERE id=$1`, folder)
	admin := NewAdmin(f.pool, identityStore(testStore), nil)
	status, err := admin.ItemStatus(t.Context(), contentID)
	if err != nil || len(status) != 1 || status[0].State != "off" || status[0].Servable {
		t.Errorf("disabled library status: %+v %v", status, err)
	}
	if _, err := admin.Regenerate(t.Context(), contentID); !errors.Is(err, ErrNotOptedIn) {
		t.Errorf("disabled library regenerate: %v", err)
	}
	libraries, err := admin.LibraryStatuses(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, library := range libraries {
		if library.LibraryID == folder {
			t.Errorf("disabled library listed: %+v", library)
		}
	}
}

func TestAdminClassifiesIneligibleFilesDB(t *testing.T) {
	f := newFixture(t)
	for _, change := range []string{"missing_since=now()", "probe_updated_at=NULL", "duration=0", "video_tracks='[]'::jsonb"} {
		folder := f.library(t, "movies", true)
		file := f.file(t, folder, change)
		content := fmt.Sprintf("movie:ineligible-%d", file)
		f.exec(t, `UPDATE media_files SET content_id=$1, `+change+` WHERE id=$2`, content, file)
		status, err := NewAdmin(f.pool, identityStore(testStore), nil).ItemStatus(t.Context(), content)
		if err != nil || len(status) != 1 || status[0].State != stateUnusable {
			t.Fatalf("%s: %+v %v", change, status, err)
		}
	}
}
