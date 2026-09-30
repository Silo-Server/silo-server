package trickplay

import (
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
