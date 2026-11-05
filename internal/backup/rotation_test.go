package backup

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

func TestPlanRotation(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return now.Add(-time.Duration(n) * 24 * time.Hour) }
	clean := &Manifest{}
	warned := &Manifest{MissingBlobs: []MissingBlob{{Key: "gone"}}}
	backup := func(kind Kind, age int, m *Manifest) folder {
		return folder{id: FolderName(day(age), kind), kind: kind, at: day(age), manifest: m, verified: true}
	}
	unverified := func(kind Kind, age int, m *Manifest) folder {
		f := backup(kind, age, m)
		f.verified = false
		return f
	}
	id := func(kind Kind, age int) string { return FolderName(day(age), kind) }

	tests := []struct {
		name    string
		folders []folder
		keep    int
		pinned  map[string]bool
		want    []string
	}{
		{
			name:    "nightly and manual share keep, oldest go first",
			folders: []folder{backup(KindNightly, 0, clean), backup(KindManual, 1, clean), backup(KindNightly, 2, clean), backup(KindNightly, 3, clean)},
			keep:    2,
			want:    []string{id(KindNightly, 2), id(KindNightly, 3)},
		},
		{
			name: "an incomplete folder is never rotated, only swept",
			folders: []folder{backup(KindNightly, 0, clean), {id: id(KindNightly, 1), kind: KindNightly, at: day(1)},
				backup(KindNightly, 2, clean)},
			keep: 1,
			want: []string{id(KindNightly, 2)},
		},
		{
			name: "the newest backup without warnings survives beyond keep",
			folders: []folder{backup(KindNightly, 0, warned), backup(KindNightly, 1, warned), backup(KindNightly, 2, clean),
				backup(KindNightly, 3, clean)},
			keep: 2,
			want: []string{id(KindNightly, 3)},
		},
		{
			name: "an unverified clean copy does not stand in for a verified one",
			folders: []folder{unverified(KindNightly, 0, clean), backup(KindNightly, 1, warned), backup(KindNightly, 2, clean),
				backup(KindNightly, 3, clean)},
			keep: 1,
			want: []string{id(KindNightly, 1), id(KindNightly, 3)},
		},
		{
			name:    "an unverified warned copy is rotated like any other",
			folders: []folder{backup(KindNightly, 0, clean), unverified(KindNightly, 1, warned), unverified(KindNightly, 2, clean)},
			keep:    1,
			want:    []string{id(KindNightly, 1), id(KindNightly, 2)},
		},
		{
			name:    "with only unverified clean copies, keep applies as is",
			folders: []folder{backup(KindNightly, 0, warned), unverified(KindNightly, 1, clean), unverified(KindNightly, 2, clean)},
			keep:    1,
			want:    []string{id(KindNightly, 1), id(KindNightly, 2)},
		},
		{
			name: "an unreadable folder is neither rotated nor counted toward keep",
			folders: []folder{{id: id(KindNightly, 0), kind: KindNightly, at: day(0), unreadable: true}, backup(KindNightly, 1, clean),
				backup(KindNightly, 2, clean), {id: id(KindNightly, 9), kind: KindNightly, at: day(9), unreadable: true}},
			keep: 1,
			want: []string{id(KindNightly, 2)},
		},
		{
			name:    "with every copy warned, keep applies as is",
			folders: []folder{backup(KindNightly, 0, warned), backup(KindNightly, 1, warned), backup(KindNightly, 2, warned)},
			keep:    1,
			want:    []string{id(KindNightly, 1), id(KindNightly, 2)},
		},
		{
			name: "before-restore copies last seven days outside keep",
			folders: []folder{backup(KindNightly, 0, clean), backup(KindBeforeRestore, 6, clean), backup(KindBeforeRestore, 8, clean),
				backup(KindNightly, 1, clean)},
			keep: 1,
			want: []string{id(KindNightly, 1), id(KindBeforeRestore, 8)},
		},
		{
			name:    "an active restore source is never deleted",
			folders: []folder{backup(KindNightly, 0, clean), backup(KindNightly, 1, clean), backup(KindBeforeRestore, 9, clean)},
			keep:    1,
			pinned:  map[string]bool{id(KindNightly, 1): true, id(KindBeforeRestore, 9): true},
			want:    nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planRotation(tt.folders, tt.keep, now, tt.pinned)
			slices.Sort(tt.want)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("planRotation = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPlanSweepDeletesOnlyFoldersSeenWithoutAManifest: the sweep deletes a
// folder only when a complete listing showed no manifest object in it and
// it is over a day old. A folder whose manifest could not be read, or did
// not verify, may be a complete backup and is kept.
func TestPlanSweepDeletesOnlyFoldersSeenWithoutAManifest(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	old, recent := now.Add(-25*time.Hour), now.Add(-time.Hour)
	for _, tt := range []struct {
		name   string
		folder folder
		swept  bool
	}{
		{"an old folder with no manifest", folder{at: old, newest: old, noManifest: true}, true},
		{"an old folder written to recently", folder{at: old, newest: recent, noManifest: true}, false},
		{"a recent folder with no manifest", folder{at: recent, newest: recent, noManifest: true}, false},
		{"a complete backup", folder{at: old, newest: old, manifest: &Manifest{}}, false},
		{"a manifest that could not be read", folder{at: old, newest: old, unreadable: true}, false},
		{"a manifest that did not verify", folder{at: old, newest: old, tampered: true}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.folder.id = "folder"
			if got := planSweep([]folder{tt.folder}, now); (len(got) == 1) != tt.swept {
				t.Fatalf("planSweep = %v, want swept %v", got, tt.swept)
			}
		})
	}
}

// TestBackupIDsNeverReuseAFolder: a backup's id is reserved under the lock
// from the folders at the destination, complete or not and of any kind, so
// two backups started in the same instant, or after the clock went back,
// never write into one folder. Ids written to the second still parse.
func TestBackupIDsNeverReuseAFolder(t *testing.T) {
	ctx := t.Context()
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 31, 4, 0, 0, 0, time.UTC)
	svc := New(Options{Destination: NewStoreDestination(store), Now: func() time.Time { return now },
		ServerID: func(context.Context) (string, error) { return "server", nil }})
	reserve := func(kind Kind) string {
		t.Helper()
		prep, err := svc.prepareBackup(ctx, kind, "", now)
		if err != nil {
			t.Fatal(err)
		}
		// The backup writes its first object; the folder is now taken.
		if err := store.Put(ctx, objectKey("server", prep.id, DumpName), []byte("dump")); err != nil {
			t.Fatal(err)
		}
		return prep.id
	}

	// A folder an earlier build named to the second holds this instant.
	legacy := "20261031T040000Z-manual"
	if at, kind, ok := ParseFolderName(legacy); !ok || !at.Equal(now) || kind != KindManual {
		t.Fatalf("ParseFolderName(%q) = %v, %v, %v", legacy, at, kind, ok)
	}
	if err := store.Put(ctx, objectKey("server", legacy, DumpName), []byte("dump")); err != nil {
		t.Fatal(err)
	}
	first, second, third := reserve(KindManual), reserve(KindManual), reserve(KindNightly)
	want := []string{"20261031T040000.001Z-manual", "20261031T040000.002Z-manual", "20261031T040000.003Z-nightly"}
	if got := []string{first, second, third}; !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	// A run handed a reserved id keeps it while it is free, and moves on
	// when a folder took it meanwhile.
	if prep, err := svc.prepareBackup(ctx, KindManual, "20261031T050000.000Z-manual", now); err != nil || prep.id != "20261031T050000.000Z-manual" {
		t.Fatalf("free reserved id = %q, %v", prep.id, err)
	}
	if prep, err := svc.prepareBackup(ctx, KindManual, first, now); err != nil || prep.id != "20261031T040000.004Z-manual" {
		t.Fatalf("taken reserved id = %q, %v", prep.id, err)
	}
	for _, bad := range []string{"20261031T040000.1Z-manual", "20261031T040000.000Z-overwritten-blobs"} {
		if _, _, ok := ParseFolderName(bad); ok {
			t.Errorf("ParseFolderName(%q) parsed", bad)
		}
	}
}
