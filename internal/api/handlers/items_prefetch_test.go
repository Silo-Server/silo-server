package handlers

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// fakePrefetcher mirrors PrefetchVirtualPlayback's real admission gate: no
// user ID, no work.
type fakePrefetcher struct{ calls chan struct{} }

func (f *fakePrefetcher) PrefetchVirtualPlayback(ctx context.Context, _ []*models.MediaFile, profileID string) {
	if apimw.GetUserID(ctx) == 0 || profileID == "" {
		return
	}
	select {
	case f.calls <- struct{}{}:
	default:
	}
}

type fakeVirtualFiles struct {
	files map[int]*models.MediaFile
}

func (f *fakeVirtualFiles) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	return f.files[id], nil
}

// The catalog prefetch must carry request identity into the background task:
// the real PrefetchVirtualPlayback drops work with no user ID, so a prefetch
// that loses auth silently warms nothing after doing the row reads.
func TestCatalogPrefetchKeepsIdentity(t *testing.T) {
	prefetcher := &fakePrefetcher{calls: make(chan struct{}, 1)}
	h := &ItemsHandler{}
	h.SetVirtualPrefetcher(prefetcher, &fakeVirtualFiles{files: map[int]*models.MediaFile{
		7: {ID: 7, FilePath: "virtual://movie/tt1?result=a"},
	}})
	ctx := apimw.SetClaims(context.Background(), &auth.Claims{UserID: 42})
	h.prefetchVirtualFileIDs(ctx, []int{7}, "profile-1")
	select {
	case <-prefetcher.calls:
	case <-time.After(2 * time.Second):
		t.Fatal("prefetch task never admitted: background context lost auth identity")
	}
}

// Repeated catalog reads for the same rows must collapse before any row load:
// the file-level admission gate fires before the goroutine and the GetByID
// calls, so a second identical trigger never touches the database.
func TestCatalogPrefetchCollapsesRepeatReads(t *testing.T) {
	loader := &countingVirtualFiles{files: map[int]*models.MediaFile{
		7: {ID: 7, FilePath: "virtual://movie/tt1?result=a"},
	}}
	prefetcher := &fakePrefetcher{calls: make(chan struct{}, 8)}
	h := &ItemsHandler{}
	h.SetVirtualPrefetcher(prefetcher, loader)
	ctx := apimw.SetClaims(context.Background(), &auth.Claims{UserID: 42})
	h.prefetchVirtualFileIDs(ctx, []int{7}, "profile-1")
	h.prefetchVirtualFileIDs(ctx, []int{7}, "profile-1")
	select {
	case <-prefetcher.calls:
	case <-time.After(2 * time.Second):
		t.Fatal("first prefetch never admitted")
	}
	// Give the duplicate a chance to (incorrectly) load, then assert the
	// loader saw exactly one row read.
	time.Sleep(100 * time.Millisecond)
	if n := loader.loads(); n != 1 {
		t.Fatalf("row loads = %d, want 1 (repeat read must collapse before GetByID)", n)
	}
}

type countingVirtualFiles struct {
	files map[int]*models.MediaFile
	n     int32
}

func (f *countingVirtualFiles) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	atomic.AddInt32(&f.n, 1)
	return f.files[id], nil
}

func (f *countingVirtualFiles) loads() int {
	return int(atomic.LoadInt32(&f.n))
}
