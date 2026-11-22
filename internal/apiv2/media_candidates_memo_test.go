package apiv2

import (
	"context"
	"net/http"
	"testing"
	"time"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

// TestDetailPreplayDiscoveryCoalescesRepeatedWatchReads proves the player's
// inventory poll cannot re-list the provider and rewrite candidates on every
// watch read, and that switching the selected version does not either: the
// listing and its persistence are item-wide, while the selected row's own
// preparation is scheduled separately.
func TestDetailPreplayDiscoveryCoalescesRepeatedWatchReads(t *testing.T) {
	deps, base := catalogDeps(t)
	fake := &fakePreplayCatalog{
		fakeCatalog: base,
		detail: catalogpkg.ItemDetail{
			ContentID: "movie:heat-1995",
			Type:      "movie",
			Versions:  []catalogpkg.FileVersion{{FileID: 120, FilePath: "virtual://movie/tt0113277", AddedAt: fixedTime()}},
		},
		hasVirtual: true,
	}
	deps.CatalogItems = fake
	deps.Watch = &fakePreplayWatch{fakeWatch: &fakeWatch{}, catalog: fake}
	deps.VirtualCandidatesDiscovery = fake
	deps.PreplayDiscoveryEnabled = func(context.Context) bool { return true }
	handler := newTestHandler(t, deps)

	for attempt := 0; attempt < 3; attempt++ {
		rec := do(t, handler, http.MethodGet, "/api/v2/watch/movie:heat-1995?library_id=2&file_id=120", "", viewerHeaders())
		if rec.Code != http.StatusOK {
			t.Fatalf("attempt %d: %s", attempt, rec.Body.String())
		}
	}
	if fake.discoveries != 1 {
		t.Fatalf("discoveries after three identical watch reads = %d, want 1", fake.discoveries)
	}

	rec := do(t, handler, http.MethodGet, "/api/v2/watch/movie:heat-1995?library_id=2&file_id=121", "", viewerHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("selected-file read: %s", rec.Body.String())
	}
	if fake.discoveries != 1 {
		t.Fatalf("discoveries after selecting another file = %d, want the 1 item-wide listing already fresh", fake.discoveries)
	}
}

// TestCatalogDetailPreplayDiscoveryIsItemScopedAcrossMounts proves a catalog
// page remount with a different selected row does not re-enumerate the item's
// provider listing. The catalog leaf read is item-wide and the client picks a
// version locally, so the selected file must not create a new memo entry; the
// watch read above shares that same item-scoped entry.
func TestCatalogDetailPreplayDiscoveryIsItemScopedAcrossMounts(t *testing.T) {
	deps, base := catalogDeps(t)
	fake := &fakePreplayCatalog{
		fakeCatalog: base,
		detail: catalogpkg.ItemDetail{
			ContentID: "movie:heat-1995",
			Type:      "movie",
			Versions:  []catalogpkg.FileVersion{{FileID: 120, FilePath: "virtual://movie/tt0113277", AddedAt: fixedTime()}},
		},
		hasVirtual: true,
	}
	deps.CatalogItems = fake
	deps.VirtualCandidatesDiscovery = fake
	deps.PreplayDiscoveryEnabled = func(context.Context) bool { return true }
	handler := newTestHandler(t, deps)

	answer := func(fileID string) {
		t.Helper()
		rec := do(t, handler, http.MethodGet, "/api/v2/catalog/items/movie:heat-1995?library_id=2&file_id="+fileID, "", viewerHeaders())
		if rec.Code != http.StatusOK {
			t.Fatalf("file_id %s: %s", fileID, rec.Body.String())
		}
	}
	answer("120")
	answer("121")

	deadline := time.Now().Add(5 * time.Second)
	for fake.discoveryDoneCount() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("catalog discovery never completed")
		}
		time.Sleep(5 * time.Millisecond) //nolint:forbidigo // Polls the detached run's completion counter.
	}

	// A third mount with another selection inside the memo window must reuse the
	// item's listing instead of starting a provider round-trip.
	answer("122")
	if snapshot := fake.snapshot(); snapshot.discoveries != 1 {
		t.Fatalf("discoveries after catalog remounts = %d, want 1 item-scoped listing", snapshot.discoveries)
	}
}
