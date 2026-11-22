package backup

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Silo-Server/silo-server/internal/sections"
)

// TestRowOfCollectionsIsRestoredDB: a deleted admin row of collections is
// created again listing the collections it listed, mapped to the ones here,
// in order, and a second preview finds it the same. A row whose listed
// collection is deleted after the backup restores with that collection
// left out of its list.
func TestRowOfCollectionsIsRestoredDB(t *testing.T) {
	f := newSelectiveFixture(t)
	ctx, pool := t.Context(), f.pool
	wirePageTargets(f, new(bool), &[]string{})
	s := seedPages(t, f)
	listed := fmt.Sprintf(`{"collections":[{"library_collection_id":%q},{"library_collection_id":%q}]}`, s.plain, s.managedCollection)
	row, err := sections.NewRepository(pool).Create(ctx, &sections.PageSection{Scope: "library", LibraryID: &s.library, Position: 901,
		SectionType: sections.SectionCollectionPosters, Title: "Franchises " + s.suffix, ItemLimit: 20, Config: json.RawMessage(listed), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	source, err := backupUnderLock(t, f.svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	id := homeSectionItemID(row.ID)
	restore := func(want string) {
		t.Helper()
		preview, err := f.svc.Preview(ctx, source.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		if got := previewItem(t, preview, SectionHomeSections, id); !got.Selectable {
			t.Fatalf("deleted row of collections = %+v", got)
		}
		report := f.restoreRequest(t, RestoreRequest{BackupID: source.ID, Digest: preview.Digest, Selection: map[string][]string{SectionHomeSections: {id}}})
		if got := reportItem(t, report, SectionHomeSections, id); got.Outcome != OutcomeRestored {
			t.Fatalf("restoring it = %+v", got)
		}
		var config string
		if err := pool.QueryRow(ctx, `SELECT config::text FROM page_sections WHERE id = $1`, row.ID).Scan(&config); err != nil {
			t.Fatal(err)
		}
		if !jsonEqual(json.RawMessage(config), json.RawMessage(want)) {
			t.Fatalf("restored row lists %s, want %s", config, want)
		}
		again, err := f.svc.Preview(ctx, source.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		if got := previewItem(t, again, SectionHomeSections, id); got.Status != ItemOnThisServer || got.Reason != ReasonSame {
			t.Fatalf("after the restore = %+v %+v", got, got.HomeSection)
		}
	}

	dbExec(t, pool, `DELETE FROM page_sections WHERE id = $1`, row.ID)
	restore(listed)

	// The managed collection is not in the backup's collections, so once
	// it is gone the row comes back without it.
	dbExec(t, pool, `DELETE FROM page_sections WHERE id = ANY($1)`, []string{row.ID, s.managed})
	dbExec(t, pool, `DELETE FROM library_collections WHERE id = $1`, s.managedCollection)
	restore(fmt.Sprintf(`{"collections":[{"library_collection_id":%q}]}`, s.plain))
}
