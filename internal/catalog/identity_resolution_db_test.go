package catalog

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestResolveIdentitiesVisibilityAndPagingDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()
	repo := NewItemRepository(pool)
	suffix := fmt.Sprintf("identity-batch-%d", time.Now().UnixNano())
	var visible, denied, disabled int
	for i, target := range []*int{&visible, &denied, &disabled} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,$2) RETURNING id`, fmt.Sprintf("%s-%d", suffix, i), i != 2).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	ids := []string{}
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id=ANY($1)`, ids)
		batchEquivExec(t, pool, `DELETE FROM media_folders WHERE id=ANY($1)`, []int{visible, denied, disabled})
	})
	add := func(name, kind, tmdb, imdb string, library int) string {
		id := suffix + "-" + name
		ids = append(ids, id)
		batchEquivExec(t, pool, `INSERT INTO media_items(content_id,type,title,tmdb_id,imdb_id) VALUES($1,$2,'Synthetic identity',$3,$4)`, id, kind, tmdb, imdb)
		batchEquivExec(t, pool, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, library)
		return id
	}
	// The first candidate conflicts and must consume the first page budget.
	add("a-conflict", "movie", "101", "tt-other", visible)
	a := add("b-copy", "movie", "101", "tt101", visible)
	b := add("c-copy", "movie", "101", "", visible)
	add("0-denied", "movie", "101", "tt101", denied)
	add("1-disabled", "movie", "101", "tt101", disabled)
	add("f-other-type", "series", "101", "tt101", visible)
	active := add("g-provider", "movie", "", "tt101", visible)
	batchEquivExec(t, pool, `INSERT INTO media_item_provider_ids(content_id,provider,provider_id,item_type) VALUES($1,'tmdb','101','movie')`, active)
	stale := add("h-rematched", "movie", "999", "", visible)
	batchEquivExec(t, pool, `INSERT INTO stale_media_ids(content_id,provider,provider_id) VALUES($1,'tmdb','101')`, stale)
	// A current column overrides a superseded active row, even if that row matches.
	rematched := add("i-old-provider", "movie", "999", "tt101", visible)
	batchEquivExec(t, pool, `INSERT INTO media_item_provider_ids(content_id,provider,provider_id,item_type) VALUES($1,'imdb','tt-old','movie')`, rematched)
	req := IdentityRequest{Seeds: []IdentitySeed{{Type: "movie", TmdbID: "101", ImdbID: "tt101"}}, Limit: 1}
	access := AccessFilter{AllowedLibraryIDs: []int{visible}}
	page, err := repo.ResolveIdentities(ctx, req, access)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Matches) != 0 || !page.HasMore || page.Next.ContentID != suffix+"-a-conflict" {
		t.Fatalf("conflicting first page = %+v", page)
	}
	matches := []IdentityMatch{}
	for attempts := 0; ; attempts++ {
		if attempts > 10 {
			t.Fatal("continuation did not terminate")
		}
		req.After = page.Next
		page, err = repo.ResolveIdentities(ctx, req, access)
		if err != nil {
			t.Fatal(err)
		}
		matches = append(matches, page.Matches...)
		if !page.HasMore {
			break
		}
	}
	want := []IdentityMatch{{Seed: 0, ContentID: a}, {Seed: 0, ContentID: b}, {Seed: 0, ContentID: active}}
	if !reflect.DeepEqual(matches, want) {
		t.Fatalf("matches = %+v, want %+v", matches, want)
	}
	req.After = IdentityPosition{}
	req.Limit = 100
	req.Seeds = append(req.Seeds, IdentitySeed{Type: "series", TmdbID: "101"})
	page, err = repo.ResolveIdentities(ctx, req, access)
	if err != nil || len(page.Matches) != 4 || page.Matches[3].Seed != 1 {
		t.Fatalf("typed seeds = %+v, %v", page, err)
	}
	page, err = repo.ResolveIdentities(ctx, req, AccessFilter{AllowedLibraryIDs: []int{}})
	if err != nil || len(page.Matches) != 0 || page.HasMore {
		t.Fatalf("empty scope = %+v, %v", page, err)
	}
	superseded := add("0-superseded-provider", "movie", "999", "", visible)
	batchEquivExec(t, pool, `INSERT INTO media_item_provider_ids(content_id,provider,provider_id,item_type) VALUES($1,'tmdb','303','movie')`, superseded)
	valid := add("z-current-provider", "movie", "303", "", visible)
	page, err = repo.ResolveIdentities(ctx, IdentityRequest{Seeds: []IdentitySeed{{Type: "movie", TmdbID: "303"}}, Limit: 1}, access)
	if err != nil || page.HasMore || len(page.Matches) != 1 || page.Matches[0].ContentID != valid {
		t.Fatalf("superseded provider consumed page: %+v, %v", page, err)
	}

}
