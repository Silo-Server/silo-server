package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

// countingDestination counts reads of restore-set.tar.
type countingDestination struct {
	Destination
	opens atomic.Int32
}

func (d *countingDestination) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	if strings.HasSuffix(key, "/"+RestoreSetName) {
		d.opens.Add(1)
	}
	return d.Destination.Open(ctx, key)
}

// TestPreviewsShareOneValidatedRestoreSet: validating a restore set reads
// and hashes the whole archive, so previews of one backup, concurrent or
// repeated, read it once; another archive is read on its own.
func TestPreviewsShareOneValidatedRestoreSet(t *testing.T) {
	ctx := context.Background()
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dest := &countingDestination{Destination: NewStoreDestination(store)}
	svc := New(Options{Destination: dest, KeyCheck: testKeyCheck, ManifestKey: testManifestKey,
		ServerID: func(context.Context) (string, error) { return "server", nil }})
	put := func(id string) {
		index, entries := testRestoreSet()
		index.ServerName = id // each backup's archive differs
		data := buildTar(t, index, entries)
		sum := sha256.Sum256(data)
		m := validTestManifest()
		m.ID = id
		m.Objects = append(m.Objects, ObjectEntry{Name: RestoreSetName, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
		m.RestoreSet = &RestoreSetSummary{FormatVersion: restoreSetVersion, Sections: map[string]int{SectionSettings: 1}}
		manifest, _ := json.Marshal(m)
		for name, body := range map[string][]byte{RestoreSetName: data, ManifestName: manifest} {
			if err := store.Put(ctx, objectKey("server", id, name), body); err != nil {
				t.Fatal(err)
			}
		}
	}
	const first, second = "20261031T040000Z-nightly", "20261030T040000Z-nightly"
	put(first)
	put(second)

	var wg sync.WaitGroup
	sets := make([]*RestoreSet, 8)
	for i := range sets {
		wg.Go(func() {
			rs, release, _, err := svc.previewSet(ctx, restoreSource{backupID: first}, true)
			if err != nil {
				t.Error(err)
				return
			}
			release()
			sets[i] = rs
		})
	}
	wg.Wait()
	if _, _, _, err := svc.previewSet(ctx, restoreSource{backupID: first}, true); err != nil {
		t.Fatal(err)
	}
	if n := dest.opens.Load(); n != 1 {
		t.Fatalf("previews of one backup read its restore set %d times, want 1", n)
	}
	for _, rs := range sets {
		if rs != sets[0] || len(rs.Libraries.Libraries) != 1 {
			t.Fatal("previews did not share one validated restore set")
		}
	}
	if _, _, _, err := svc.previewSet(ctx, restoreSource{backupID: second}, true); err != nil {
		t.Fatal(err)
	}
	if n := dest.opens.Load(); n != 2 {
		t.Fatalf("another backup's restore set reads = %d, want 2", n)
	}
}

// TestRestoreSetReadStopsWithItsLastPreview: one preview leaving does not
// fail another waiting on the same read, and the read stops once no preview
// waits on it.
func TestRestoreSetReadStopsWithItsLastPreview(t *testing.T) {
	var c restoreSetCache
	started, stopped := make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	load := func(ctx context.Context) (*RestoreSet, error) {
		close(started)
		select {
		case <-release:
			return &RestoreSet{}, nil
		case <-ctx.Done():
			close(stopped)
			return nil, ctx.Err()
		}
	}
	first, cancelFirst := context.WithCancel(t.Context())
	second, cancelSecond := context.WithCancel(t.Context())
	defer cancelSecond()
	results := make(chan error, 2)
	go func() { _, _, err := c.get(first, "d", load); results <- err }()
	await(t, started, "the first preview to start reading")
	go func() { _, _, err := c.get(second, "d", load); results <- err }()
	for deadline := time.Now().Add(10 * time.Second); ; {
		c.mu.Lock()
		joined := c.reading["d"] != nil && c.reading["d"].waiters == 2
		c.mu.Unlock()
		if joined {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the second preview never joined the read")
		}
		runtime.Gosched()
	}
	cancelFirst()
	if err := await(t, results, "the preview that left"); !errors.Is(err, context.Canceled) {
		t.Fatalf("the preview that left = %v, want canceled", err)
	}
	select {
	case <-stopped:
		t.Fatal("the read stopped while another preview waited on it")
	default:
	}
	cancelSecond()
	if err := await(t, results, "the last preview"); !errors.Is(err, context.Canceled) {
		t.Fatalf("the last preview = %v, want canceled", err)
	}
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("the read went on after its last preview left")
	}
}

// TestCachedSetIsChargedWhatItHolds: a set is charged what it holds once
// read, its decoded documents included, so a set of short item ids that
// decode to far more than their bytes cannot fill memory past the cache's
// bound; and the cache keeps one set of an unverifiable backup at a time.
func TestCachedSetIsChargedWhatItHolds(t *testing.T) {
	index, entries := testRestoreSet()
	ids := make([]string, 100_000)
	for i := range ids {
		ids[i] = strconv.Itoa(i % 100)
	}
	doc := CollectionsDocument{ItemArtwork: []ItemArtworkEntry{}, Branding: []BrandingEntry{}}
	for i := range 7 {
		doc.Collections = append(doc.Collections, CollectionEntry{ID: fmt.Sprintf("c%d", i), LibraryID: 3, Libraries: []CollectionLib{{LibraryID: 3}},
			Title: "Heavy", Items: ids})
	}
	setDocument(&index, entries, homeSectionsDocumentName, mustJSON(HomeSectionsDocument{Sections: []HomeSectionEntry{}}), 0)
	for i := range index.Sections {
		if index.Sections[i].ID == SectionCollections {
			index.Sections[i].Blobs = []string{}
		}
	}
	entries = slices.DeleteFunc(entries, func(e testEntry) bool { return strings.HasPrefix(e.name, "blobs/collection-images/") })
	setDocument(&index, entries, collectionsDocumentName, mustJSON(doc), len(doc.Collections))
	data := buildTar(t, index, entries)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	rs, err := ReadRestoreSet(context.Background(), bytes.NewReader(data), ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	held := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if rs.metadataBytes < held {
		t.Fatalf("charged %d bytes for a set that holds %d", rs.metadataBytes, held)
	}
	runtime.KeepAlive(rs)

	var c restoreSetCache
	c.mu.Lock()
	c.storeLocked("a", &RestoreSet{fromUnverified: true, metadataBytes: 1})
	c.storeLocked("b", &RestoreSet{metadataBytes: 1})
	c.storeLocked("c", &RestoreSet{fromUnverified: true, metadataBytes: 1})
	c.mu.Unlock()
	if _, ok := c.entries["a"]; ok || len(c.entries) != 2 || c.bytes != 2 {
		t.Fatalf("cache %v (%d bytes), want the newer unverified set and the verified one", slices.Collect(maps.Keys(c.entries)), c.bytes)
	}
}

// TestEvictedSetKeepsItsFilesWhileHeld: a set the cache drops keeps its
// account documents until the last preview holding it releases it, and one
// no one holds loses them at once, as do idle sets dropped for disk space.
func TestEvictedSetKeepsItsFilesWhileHeld(t *testing.T) {
	index, entries := testRestoreSet()
	data := buildTar(t, index, entries)
	var c restoreSetCache
	read := func(digest string) (*RestoreSet, func()) {
		rs, release, err := c.get(t.Context(), digest, func(ctx context.Context) (*RestoreSet, error) {
			return ReadRestoreSet(ctx, bytes.NewReader(data), ReadOptions{})
		})
		if err != nil {
			t.Fatal(err)
		}
		return rs, release
	}
	held, release := read("held")
	if len(held.Users) == 0 {
		t.Fatal("the test set has no accounts")
	}
	idle, releaseIdle := read("idle")
	releaseIdle()
	for i := range maxCachedRestoreSets {
		_, r := read(fmt.Sprintf("other-%d", i))
		r()
	}
	if _, err := os.Stat(idle.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an evicted set no one held kept its files: %v", err)
	}
	if _, err := held.loadUser(held.Users[0]); err != nil {
		t.Fatalf("an evicted set lost its files while a preview held it: %v", err)
	}
	// Short of disk, the cache drops what no preview holds and nothing else.
	cachedHeld, releaseCached := read("cached-held")
	cachedIdle, releaseCachedIdle := read("cached-idle")
	releaseCachedIdle()
	if !c.evictIdle() {
		t.Fatal("evictIdle dropped nothing")
	}
	if _, err := os.Stat(cachedIdle.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an idle cached set kept its files: %v", err)
	}
	if _, err := cachedHeld.loadUser(cachedHeld.Users[0]); err != nil {
		t.Fatalf("a held cached set lost its files: %v", err)
	}
	releaseCached()
	release()
	release() // a second release is a no-op
	if _, err := os.Stat(held.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an evicted set kept its files after its last release: %v", err)
	}
}
