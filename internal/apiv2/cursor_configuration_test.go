package apiv2

import (
	"bytes"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

func TestCursorSignersShareConfiguredSecret(t *testing.T) {
	secret := []byte("synthetic-cluster-cursor-secret")
	first := NewCursors(secret)
	second := NewCursors(bytes.Clone(secret))
	// A caller retaining its configuration slice cannot mutate an existing
	// signer's key. Independent server processes can resume the same page.
	secret[0] ^= 0xff
	scope := CursorScope{OperationID: "listItems", Security: "profile:7", Sort: "title", Tiebreaker: "id"}
	cursor, err := first.Encode(scope, "last-item")
	if err != nil {
		t.Fatal(err)
	}
	var position string
	if problem := second.Decode(scope, cursor, &position); problem != nil {
		t.Fatal(problem)
	}
	if position != "last-item" {
		t.Fatalf("position = %q", position)
	}
}

func TestCursorMissingSecretFailsClosedThroughHTTP(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.CursorSecret = nil
	images := &fakeAdminImages{rows: []handlers.AdminImageEntryView{{OriginalURL: "one"}, {OriginalURL: "two"}}}
	deps.AdminCatalogImages = images
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/items/item/images?limit=1"
	requireProblem(t, do(t, h, "GET", path, "", nil), TypeAuthenticationRequired)
	if images.calls != 0 {
		t.Fatal("unauthenticated request reached service")
	}
	// Encoding a continuation must retain its dependency error through the
	// handler's service error projection; no process-local key is substituted.
	requireProblem(t, do(t, h, "GET", path, "", bearer(adminToken)), TypeDependencyUnavailable)
	if images.calls != 1 {
		t.Fatalf("first page calls = %d", images.calls)
	}
	// A continuation fails before querying the domain when no shared key can
	// verify its authority, even when its encoding is malformed too.
	requireProblem(t, do(t, h, "GET", path+"&cursor=unverifiable", "", bearer(adminToken)), TypeDependencyUnavailable)
	if images.calls != 1 {
		t.Fatal("unverifiable cursor reached service")
	}
}
