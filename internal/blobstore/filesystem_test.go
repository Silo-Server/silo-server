package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFilesystemHidesTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	s, err := NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Put(context.Background(), "poster.jpg", []byte("image")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, ".probe-ready"), []byte("probe"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(root, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "nested", ".tmp-writing"), []byte("tmp"), 0644); err != nil {
		t.Fatal(err)
	}
	objects, next, err := s.List(context.Background(), "", "", 1)
	if err != nil || next != "" || len(objects) != 1 || objects[0].Key != "poster.jpg" {
		t.Fatalf("list: %v %q %v", objects, next, err)
	}
}
func TestFilesystemRejectsSymlinkParents(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	s, err := NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(outside, "image.jpg"), []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = s.Put(ctx, "linked/image.jpg", []byte("replace")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("put: %v", err)
	}
	if _, _, err = s.Get(ctx, "linked/image.jpg"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get: %v", err)
	}
	if _, err = s.DeletePrefix(ctx, "linked"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(outside, "image.jpg"))
	if err != nil || string(data) != "outside" {
		t.Fatalf("outside changed: %q %v", data, err)
	}
}
func TestFilesystemOpenReaderSurvivesReplacement(t *testing.T) {
	s, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = s.Put(ctx, "image.jpg", []byte("old")); err != nil {
		t.Fatal(err)
	}
	r, info, err := s.Get(ctx, "image.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if err = s.Put(ctx, "image.jpg", []byte("replacement")); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r)
	if err != nil || string(b) != "old" || info.Size != int64(len(b)) {
		t.Fatalf("reader and metadata differ: %q %+v %v", b, info, err)
	}
}

func TestFilesystemPaginationUsesFullKeyOrder(t *testing.T) {
	s, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	want := []string{"a.jpg", "a/z.jpg", "a0.jpg", "b/z.jpg"}
	for _, key := range want {
		if err := s.Put(ctx, key, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	cursor := ""
	for i, key := range want {
		page, next, err := s.List(ctx, "", cursor, 1)
		if err != nil || len(page) != 1 || page[0].Key != key {
			t.Fatalf("page %d: %v %q %v", i, page, next, err)
		}
		cursor = next
	}
	if cursor != "" {
		t.Fatal(cursor)
	}
}

func TestFilesystemCleansOnlyAbandonedTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	active, name, err := temporary(root, ".", ".tmp-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = active.Close() }()
	old := time.Now().Add(-48 * time.Hour)
	for _, key := range []string{name, ".tmp-orphan", ".probe-new"} {
		if key != name {
			if err := os.WriteFile(filepath.Join(dir, key), nil, 0644); err != nil {
				t.Fatal(err)
			}
		}
		if key != ".probe-new" {
			if err := os.Chtimes(filepath.Join(dir, key), old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, _, err := s.List(context.Background(), "", "", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".tmp-orphan")); !os.IsNotExist(err) {
		t.Fatalf("orphan: %v", err)
	}
	for _, key := range []string{name, ".probe-new"} {
		if _, err := os.Stat(filepath.Join(dir, key)); err != nil {
			t.Fatal(err)
		}
	}
	if err := active.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.List(context.Background(), "", "", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		t.Fatalf("released orphan: %v", err)
	}
}

func TestFilesystemRootValidationAndRecovery(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFilesystem(filepath.Join(file, "art")); err == nil {
		t.Fatal("file parent accepted")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFilesystem(link); err == nil {
		t.Fatal("symlink root accepted")
	}
	aliasStore, err := NewFilesystem(filepath.Join(link, "art"))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(filepath.Join(link, "art"))
	if err != nil || aliasStore.root != canonical {
		t.Fatalf("canonical root: %q %q %v", aliasStore.root, canonical, err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := aliasStore.Put(context.Background(), "stable.jpg", []byte("ok")); err != nil {
		t.Fatal(err)
	}
	s, err := NewFilesystem(filepath.Join(dir, "art"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.root); err != nil {
		t.Fatal(err)
	}
	if err := s.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(context.Background(), "recovered.jpg", []byte("ok")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := s.List(ctx, "", "", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled list: %v", err)
	}
}

func TestFilesystemListSkipsCompletedSubtrees(t *testing.T) {
	s, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a/first.jpg", "z/last.jpg"} {
		if err := s.Put(context.Background(), key, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	blocked := filepath.Join(s.root, "a")
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(blocked, 0755) }()
	page, _, err := s.List(context.Background(), "", "b", 1)
	if err != nil || len(page) != 1 || page[0].Key != "z/last.jpg" {
		t.Fatalf("page=%v err=%v", page, err)
	}
}

func TestOpenLocalDegradedStartupRecovers(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires ordinary filesystem permissions")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(parent, 0755) }()
	stores, backend, err := Open(context.Background(), Options{Backend: BackendLocal, LocalPath: filepath.Join(parent, "art")})
	if err != nil || backend != BackendLocal {
		t.Fatalf("open: %s %v", backend, err)
	}
	store := stores.Assets
	if err := store.Probe(context.Background()); err == nil {
		t.Fatal("unwritable storage reported ready")
	}
	if err := os.Chmod(parent, 0755); err != nil {
		t.Fatal(err)
	}
	if err := store.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), "ready.jpg", []byte("ok")); err != nil {
		t.Fatal(err)
	}
}

func TestProbeReclaimsRootOrphansPastFreshFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf(".probe-fresh-%02d", i)), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	var orphans []string
	for _, name := range []string{".probe-z-orphan", ".tmp-z-orphan"} {
		orphan := filepath.Join(dir, name)
		if err := os.WriteFile(orphan, nil, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(orphan, old, old); err != nil {
			t.Fatal(err)
		}
		orphans = append(orphans, orphan)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(outside, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, ".probe-link")); err != nil {
		t.Fatal(err)
	}
	if err := s.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, orphan := range orphans {
		if _, err := os.Stat(orphan); !os.IsNotExist(err) {
			t.Fatalf("orphan remains: %v", err)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".probe-link")); err != nil {
		t.Fatal(err)
	}
}

func TestPutTracksCreatedAncestors(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(fs.root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	created, err := mkdirAllTracked(root, "a/b/c")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a/b/c", "a/b", "a"}; !slices.Equal(created, want) {
		t.Fatalf("created = %v, want %v", created, want)
	}
	created, err = mkdirAllTracked(root, "a/b/d")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a/b/d"}; !slices.Equal(created, want) {
		t.Fatalf("created = %v, want %v", created, want)
	}
	if created, err = mkdirAllTracked(root, "a/b/d"); err != nil || len(created) != 0 {
		t.Fatalf("existing dir reported as created: %v, %v", created, err)
	}
	if err := fs.Put(t.Context(), "x/y/z/file.webp", []byte("ok")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fs.root, "x/y/z/file.webp")); err != nil {
		t.Fatal(err)
	}
}

func TestFilesystemIdentityIsAbsoluteRoot(t *testing.T) {
	root := t.TempDir()
	a, err := NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewFilesystem(root + string(filepath.Separator))
	if err != nil {
		t.Fatal(err)
	}
	if a.Identity() != b.Identity() || !strings.HasPrefix(a.Identity(), BackendLocal+"|"+string(filepath.Separator)) {
		t.Fatalf("identities %q and %q", a.Identity(), b.Identity())
	}
	other, err := NewFilesystem(filepath.Join(root, "other"))
	if err != nil {
		t.Fatal(err)
	}
	if other.Identity() == a.Identity() {
		t.Fatal("different roots share an identity")
	}
}
