package apiv2

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Runs in the real policy transport fixture's isolated database, after its
// persistence checks. Page size two forces several continuations through SQL.
func testAdminPolicyPages(t *testing.T, pool *pgxpool.Pool, h http.Handler, document ID) {
	t.Helper()
	ctx := t.Context()
	for i := range 5 {
		var id int64
		if err := pool.QueryRow(ctx, `INSERT INTO policy_documents(domain,name,enabled) VALUES('action',$1,false) RETURNING id`, fmt.Sprintf("Policy pagination fixture %d", i)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		defer func() { _, _ = pool.Exec(ctx, `DELETE FROM policy_documents WHERE id=$1`, id) }()
	}
	if _, err := pool.Exec(ctx, `INSERT INTO policy_document_versions(document_id,version_number,rego_source,source_sha256,compiled_ok) SELECT $1,n,'fixture','fixture',false FROM generate_series(3,7) AS n`, string(document)); err != nil {
		t.Fatal(err)
	}
	base := Prefix + "/admin/policy/documents"
	for _, tc := range []struct {
		path     string
		count    int
		versions bool
	}{{base, 6, false}, {base + "/" + string(document) + "/versions", 7, true}} {
		first := do(t, h, "GET", tc.path+"?limit=2", "", bearer(adminToken))
		if first.Code != 200 {
			t.Fatalf("first page %d %s", first.Code, first.Body)
		}
		type row struct {
			ID            ID      `json:"id"`
			VersionNumber int     `json:"version_number"`
			Source        *string `json:"source"`
		}
		var page Collection[row]
		if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 2 || page.Page == nil || !page.Page.HasMore || page.Page.NextCursor == "" {
			t.Fatalf("not paginated: %s", first.Body)
		}
		cursor := page.Page.NextCursor
		repeated := do(t, h, "GET", tc.path+"?limit=2&cursor="+cursor, "", bearer(adminToken))
		repeatedAgain := do(t, h, "GET", tc.path+"?limit=2&cursor="+cursor, "", bearer(adminToken))
		if repeated.Code != 200 || repeatedAgain.Body.String() != repeated.Body.String() {
			t.Fatal("same continuation did not repeat the same page")
		}
		requireProblem(t, do(t, h, "GET", tc.path+"?cursor="+cursor, "", bearer(otherAdminToken)), TypeInvalidCursor)
		requireProblem(t, do(t, h, "GET", tc.path+"?cursor="+cursor, "", with(bearer(adminToken), "X-Profile-Id", "p-primary")), TypeInvalidCursor)
		requireProblem(t, do(t, h, "GET", tc.path+"?cursor="+cursor+"x", "", bearer(adminToken)), TypeInvalidCursor)
		mismatch := base + "/" + string(document) + "/versions"
		if tc.versions {
			mismatch = base + "/999999/versions"
		}
		requireProblem(t, do(t, h, "GET", mismatch+"?cursor="+cursor, "", bearer(adminToken)), TypeInvalidCursor)
		requireProblem(t, do(t, h, "GET", tc.path+"?limit=201", "", bearer(adminToken)), TypeValidationFailed)
		seen := map[ID]bool{}
		lastVersion := 8
		var lastDocumentID int64
		for {
			for _, item := range page.Items {
				if seen[item.ID] {
					t.Fatalf("duplicate id %s", item.ID)
				}
				seen[item.ID] = true
				if !tc.versions {
					id, p := adminPolicyID(item.ID)
					if p != nil || id <= lastDocumentID {
						t.Fatal("document identity order incorrect")
					}
					lastDocumentID = id
				}
				if tc.versions {
					if item.VersionNumber >= lastVersion || item.Source != nil {
						t.Fatal("version order or metadata projection incorrect")
					}
					lastVersion = item.VersionNumber
				}
			}
			if !page.Page.HasMore {
				if page.Page.NextCursor != "" {
					t.Fatal("terminal page retained cursor")
				}
				break
			}
			next := do(t, h, "GET", tc.path+"?limit=2&cursor="+page.Page.NextCursor, "", bearer(adminToken))
			if next.Code != 200 {
				t.Fatalf("next page %d %s", next.Code, next.Body)
			}
			page = Collection[row]{}
			if err := json.Unmarshal(next.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if len(page.Items) > 2 || page.Page == nil {
				t.Fatal("page size or metadata missing")
			}
			if len(seen) > tc.count {
				t.Fatal("pagination did not terminate")
			}
		}
		if len(seen) != tc.count {
			t.Fatalf("got %d rows, want %d", len(seen), tc.count)
		}
	}
	store := policy.NewPolicyStore(pool)
	id, p := adminPolicyID(document)
	if p != nil {
		t.Fatal(p)
	}
	docs, err := store.ListDocumentsPage(ctx, 0, 2)
	if err != nil || len(docs) != 2 {
		t.Fatalf("bounded repository documents: %d %v", len(docs), err)
	}
	versions, err := store.ListVersionsPage(ctx, id, 0, 2)
	if err != nil || len(versions) != 2 {
		t.Fatalf("bounded repository versions: %d %v", len(versions), err)
	}
	if _, err := store.ListDocumentsPage(ctx, 0, 202); err == nil {
		t.Fatal("unbounded document request accepted")
	}
	if _, err := store.ListVersionsPage(ctx, id, 0, 202); err == nil {
		t.Fatal("unbounded version request accepted")
	}
}
