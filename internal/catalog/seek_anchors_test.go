package catalog

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSeekAnchorsNeedAnIndexBoundedSort(t *testing.T) {
	e := &QueryExecutor{SeekAnchors: newSeekAnchorCache()}
	for _, tc := range []struct {
		field    string
		relation string
		anchored bool
	}{
		{"title", "", true},
		// Added order leads with a nullable term; walking from an anchor
		// would scan from the first row.
		{"added_at", "", false},
		// Episode titles may be NULL, so the episode relation isn't bounded.
		{"title", episodeCatalogBaseRelation, false},
	} {
		e.BaseRelationSQL = tc.relation
		plan, err := e.buildPreviewPagePlan(QueryDefinition{Sort: QuerySort{Field: tc.field, Order: "asc"}}, AccessFilter{}, 1, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := e.canAnchorSeek(plan, seekAnchorStride); got != tc.anchored {
			t.Fatalf("%s on %q: canAnchorSeek = %v", tc.field, tc.relation, got)
		}
		// A seek before the first anchor is a short OFFSET already.
		if e.canAnchorSeek(plan, seekAnchorStride-1) {
			t.Fatalf("%s on %q: anchored a seek before the first anchor", tc.field, tc.relation)
		}
	}

	// A failed anchor pass (here: no pool) hands the seek back to OFFSET.
	e.BaseRelationSQL = ""
	plan, err := e.buildPreviewPagePlan(QueryDefinition{Sort: QuerySort{Field: "title", Order: "asc"}}, AccessFilter{}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, anchored, err := e.anchoredSeek(t.Context(), plan, seekAnchorStride); anchored || err != nil {
		t.Fatalf("failed anchor pass: anchored=%v err=%v, want an OFFSET fallback", anchored, err)
	}

	plan, err = e.buildPreviewPagePlan(QueryDefinition{Sort: QuerySort{Field: "title", Order: "desc"}}, AccessFilter{}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	sql, _ := plan.seekAnchorSQL()
	if !strings.Contains(sql, "ROW_NUMBER() OVER ("+plan.orderBy+")") || !strings.Contains(sql, fmt.Sprintf("seek_position %% %d = 0", seekAnchorStride)) {
		t.Fatalf("anchor SQL = %s", sql)
	}
}

func TestSeekAnchorsPostgresMatchOffsetSeek(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	prefix := fmt.Sprintf("anchor-%d-", time.Now().UnixNano())
	const count = 2*seekAnchorStride + 90
	// Titles repeat so ties fall back to content_id; some sort titles override
	// the title, as "The X" sorts under X.
	if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,sort_title,genres,status)
		SELECT $1 || lpad(i::text, 4, '0'), 'movie', 'Title ' || (i % 97), CASE WHEN i % 5 = 0 THEN 'A ' || i ELSE '' END, '{}', 'released'
		FROM generate_series(0, $2 - 1) AS i`, prefix, count); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%") }()
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s%04d", prefix, i)
	}
	access := AccessFilter{AllowedContentIDs: ids}
	snapshot := time.Now()
	plain := &QueryExecutor{Pool: pool, SnapshotAt: &snapshot}
	anchored := &QueryExecutor{Pool: pool, SnapshotAt: &snapshot, SeekAnchors: newSeekAnchorCache()}
	for _, order := range []string{"asc", "desc"} {
		def := QueryDefinition{Sort: QuerySort{Field: "title", Order: order}}
		for _, index := range []int{1, 2, seekAnchorStride, seekAnchorStride + 1, seekAnchorStride + 2, 2 * seekAnchorStride, 2*seekAnchorStride + 1, count - 1, count} {
			want, err := plain.SeekCursor(ctx, def, access, index)
			if err != nil {
				t.Fatalf("%s offset seek %d: %v", order, index, err)
			}
			got, err := anchored.SeekCursor(ctx, def, access, index)
			if err != nil {
				t.Fatalf("%s anchored seek %d: %v", order, index, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s seek %d: anchored %v, offset %v", order, index, cursorKeyStrings(got), cursorKeyStrings(want))
			}
		}
		if _, err := anchored.SeekCursor(ctx, def, access, count+1); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("%s seek past the end: %v", order, err)
		}
	}
	if got := len(anchored.SeekAnchors.entries); got != 2 {
		t.Fatalf("cached anchor sets = %d, want one per order", got)
	}
}

func cursorKeyStrings(c *QueryCursor) []string {
	if c == nil {
		return nil
	}
	out := []string{fmt.Sprint(c.Consumed)}
	for _, key := range c.Keys {
		if key.Value == nil {
			out = append(out, "<nil>")
		} else {
			out = append(out, *key.Value)
		}
	}
	return out
}
