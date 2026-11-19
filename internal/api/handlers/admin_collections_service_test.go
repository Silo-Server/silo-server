package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// TestAdminCollectionUpdateNameRuleDB: the editor sends its whole draft on
// Save, so an update checks the title only when it changes it or adds a
// library. A collection that already shares its title (made by an import)
// still saves its other fields; a rename into a taken name, or adding a
// library that has the name, is refused at title.
func TestAdminCollectionUpdateNameRuleDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	repo := catalog.NewLibraryCollectionRepository(f.pool)
	h := NewLibraryCollectionHandler(repo, nil, catalog.NewItemRepository(f.pool), nil)
	title := fmt.Sprintf("Staff Picks %d", f.account)
	create := func(name string, library int, unique bool) string {
		t.Helper()
		var id string
		if unique {
			created, err := h.CreateAdminCollection(t.Context(), AdminCollectionCreate{LibraryIDs: []int{library}, Title: name, CollectionType: "manual"})
			if err != nil {
				t.Fatalf("create %q: %v", name, err)
			}
			id = created.ID
		} else {
			created, err := repo.Create(t.Context(), catalog.CreateLibraryCollectionInput{LibraryIDs: []int{library}, Slug: "staff-picks", Title: name})
			if err != nil {
				t.Fatalf("import %q: %v", name, err)
			}
			id = created.ID
		}
		t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM library_collections WHERE id = $1`, id) })
		return id
	}
	create(title, f.library, true)
	imported := create(title, f.library, false)
	other := create(title+" too", f.library, true)
	create(title, f.hidden, true)

	for _, tc := range []struct {
		name      string
		id        string
		update    AdminCollectionUpdate
		wantTaken bool
	}{
		{name: "full draft of an existing duplicate", id: imported, update: AdminCollectionUpdate{Title: new(title), Description: new("Edited"), LibraryIDs: &[]int{f.library}}},
		{name: "case and spaces only", id: imported, update: AdminCollectionUpdate{Title: new("  " + strings.ToUpper(title) + " ")}},
		{name: "rename into a taken name", id: other, update: AdminCollectionUpdate{Title: new(title)}, wantTaken: true},
		{name: "add a library that has the name", id: imported, update: AdminCollectionUpdate{Title: new(title), LibraryIDs: &[]int{f.library, f.hidden}}, wantTaken: true},
	} {
		_, err := h.UpdateAdminCollection(t.Context(), tc.id, tc.update)
		apiErr, isField := errors.AsType[*APIError](err)
		taken := isField && apiErr.Field == "title"
		if taken != tc.wantTaken || (!tc.wantTaken && err != nil) {
			t.Fatalf("%s: err = %v, want title taken %v", tc.name, err, tc.wantTaken)
		}
	}

	// Without a library the collection would leave every library's list.
	_, err := h.UpdateAdminCollection(t.Context(), other, AdminCollectionUpdate{LibraryIDs: &[]int{}})
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Field != "library_ids" {
		t.Fatalf("update to no libraries: err = %v, want a field error at library_ids", err)
	}
}

// TestAdminCollectionUpdateNameRuleReadsTheLockedRowDB: an If-Match: * update
// skips the revision check, so whether it renames is decided from the row its
// transaction locks, not the read the handler took first. Here another
// request renames the collection and moves it next to a same-named one while
// the update, which echoes the old name, waits on the row; the echo is then a
// rename into a taken name.
func TestAdminCollectionUpdateNameRuleReadsTheLockedRowDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	repo := catalog.NewLibraryCollectionRepository(f.pool)
	h := NewLibraryCollectionHandler(repo, nil, catalog.NewItemRepository(f.pool), nil)
	title := fmt.Sprintf("Staff Picks %d", f.account)
	var id string
	for _, library := range []int{f.hidden, f.library} {
		created, err := h.CreateAdminCollection(t.Context(), AdminCollectionCreate{LibraryIDs: []int{library}, Title: title, CollectionType: "manual"})
		if err != nil {
			t.Fatalf("create in %d: %v", library, err)
		}
		id = created.ID
		t.Cleanup(func() {
			_, _ = f.pool.Exec(context.Background(), `DELETE FROM library_collections WHERE id = $1`, created.ID)
		})
	}

	other, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Rollback(context.Background()) }()
	var otherPID int
	if err := other.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&otherPID); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(t.Context(), `UPDATE library_collections SET title = title || ' too', slug = slug || '-moved', library_id = $2 WHERE id = $1`, id, f.hidden); err != nil {
		t.Fatalf("rename and move: %v", err)
	}
	if _, err := other.Exec(t.Context(), `UPDATE library_collection_libraries SET library_id = $2 WHERE collection_id = $1`, id, f.hidden); err != nil {
		t.Fatalf("move scope: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := h.UpdateAdminCollection(WithAdminCollectionExpectedRevision(t.Context(), -1), id, AdminCollectionUpdate{Title: new(title), Description: new("Edited")})
		done <- err
	}()
	// Commit only once the update waits on the row the other request holds.
	for blocked := false; !blocked; {
		select {
		case err := <-done:
			t.Fatalf("update finished before the other request committed: %v", err)
		default:
		}
		if err := f.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, otherPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if !blocked {
			time.Sleep(10 * time.Millisecond) //nolint:forbidigo // the poll interval; the wait is on pg_blocking_pids, an observable lock wait
		}
	}
	if err := other.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	err = <-done
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Field != "title" {
		t.Fatalf("echoed old name after a rename and move: err = %v, want a field error at title", err)
	}
}

// A variant that fails to delete must not leave an admin collection pointing
// at partly deleted artwork: the reference is cleared first and the failed
// cleanup only leaves orphans (issue #2419).
func TestDeleteAdminCollectionArtworkClearsReferenceWhenVariantDeleteFailsDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	repo := catalog.NewLibraryCollectionRepository(f.pool)
	h := NewLibraryCollectionHandler(repo, nil, catalog.NewItemRepository(f.pool), nil)
	created, err := h.CreateAdminCollection(t.Context(), AdminCollectionCreate{LibraryID: f.library, Title: "Broken artwork", CollectionType: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM library_collections WHERE id = $1`, created.ID)
	})
	for _, kind := range []string{collectionImagePoster, adminCollectionBackdrop} {
		t.Run(kind, func(t *testing.T) {
			dir := collectionImageDir(adminCollectionImagePrefix, created.ID, kind)
			path := dir + "/original.0000000000000001.webp"
			input := catalog.UpdateLibraryCollectionInput{ID: created.ID}
			if kind == collectionImagePoster {
				input.PosterURL = new(path)
			} else {
				input.BackdropURL = new(path)
			}
			if err := repo.Update(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			artwork := &partialDeleteStore{okKeys: 1, stubListDeleteStore: stubListDeleteStore{keys: []string{path, dir + "/w300.0000000000000001.webp"}}}
			h.ArtworkStore = artwork

			if err := h.DeleteAdminCollectionArtwork(t.Context(), created.ID, kind); err != nil {
				t.Fatalf("DeleteAdminCollectionArtwork: %v", err)
			}
			got, err := h.adminCollectionImagePath(t.Context(), created.ID, kind)
			if err != nil {
				t.Fatal(err)
			}
			if got != "" {
				t.Fatalf("%s reference = %q, want cleared", kind, got)
			}
			if len(artwork.deleted) != 1 {
				t.Fatalf("cleanup did not run after the clear: deleted %#v", artwork.deleted)
			}
		})
	}
}
