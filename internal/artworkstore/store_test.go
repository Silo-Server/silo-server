package artworkstore

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystemContract(t *testing.T) {
	store, err := NewFilesystem(filepath.Join(t.TempDir(), "artwork"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.Put(ctx, "tmdb/movie/poster.webp", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "tmdb/movie/backdrop.webp", []byte("two")); err != nil {
		t.Fatal(err)
	}
	reader, info, err := store.Get(ctx, "tmdb/movie/poster.webp")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(reader)
	_ = reader.Close()
	if string(data) != "one" || info.ETag == "" {
		t.Fatalf("get = %q, %#v", data, info)
	}
	items, cursor, err := store.List(ctx, "tmdb", "", 1)
	if err != nil || len(items) != 1 || cursor == "" {
		t.Fatalf("list = %#v %q %v", items, cursor, err)
	}
	items, cursor, err = store.List(ctx, "tmdb", cursor, 1)
	if err != nil || len(items) != 1 || cursor != "" {
		t.Fatalf("list page 2 = %#v %q %v", items, cursor, err)
	}
	if n, err := store.Delete(ctx, []string{"tmdb/movie/poster.webp", "tmdb/missing.webp"}); err != nil || n != 2 {
		t.Fatalf("delete = %d, %v", n, err)
	}
	if n, err := store.DeletePrefix(ctx, "tmdb/movie"); err != nil || n != 1 {
		t.Fatalf("delete prefix = %d, %v", n, err)
	}
	if _, err := store.Stat(ctx, "tmdb/movie/backdrop.webp"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stat after delete = %v", err)
	}
}

func TestFilesystemRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	store, err := NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.webp")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.webp")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(context.Background(), "link.webp"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("symlink get = %v", err)
	}
}

func TestStoreRejectsInvalidKeysWithoutWrites(t *testing.T) {
	stores := []Store{}
	fs, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stores = append(stores, fs)
	stores = append(stores, fakeArtworkS3(t))
	keys := []string{"", "../x", "/abs", "a//b", "a\\b", "a\x00b", string(make([]byte, 1025))}
	for _, s := range stores {
		for _, key := range keys {
			if err := s.Put(context.Background(), key, []byte("x")); !errors.Is(err, ErrInvalidKey) {
				t.Fatalf("%T key %q: %v", s, key, err)
			}
		}
	}
}
