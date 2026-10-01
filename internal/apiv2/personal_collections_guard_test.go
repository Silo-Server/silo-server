package apiv2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type collectionGuardHTTPStore struct {
	userstore.UserStore
	userstore.CollectionItemsPager
	userstore.CollectionMutationStore
	userstore.CollectionFeatureProvider
	beforeUpdate  func()
	liveItemCount *int
}

func (s *collectionGuardHTTPStore) GetCollection(ctx context.Context, id string) (*userstore.Collection, error) {
	c, err := s.UserStore.GetCollection(ctx, id)
	if err == nil && s.liveItemCount != nil {
		c.ItemCount = *s.liveItemCount
	}
	return c, err
}

func (s *collectionGuardHTTPStore) UpdateCollection(ctx context.Context, input userstore.UpdateCollectionInput) error {
	if s.beforeUpdate != nil {
		s.beforeUpdate()
	}
	return s.UserStore.UpdateCollection(ctx, input)
}

type collectionGuardHTTPProvider struct {
	userstore.UserStoreProvider
	stores map[int]userstore.UserStore
}

func (p collectionGuardHTTPProvider) ForUser(_ context.Context, id int) (userstore.UserStore, error) {
	s, ok := p.stores[id]
	if !ok {
		return nil, fmt.Errorf("test account does not exist")
	}
	return s, nil
}
func newCollectionGuardHTTP(t *testing.T) (http.Handler, *collectionGuardHTTPStore) {
	t.Helper()
	provider := collectionGuardHTTPProvider{stores: map[int]userstore.UserStore{}}
	var main *collectionGuardHTTPStore
	for _, uid := range []int{1, 2} {
		db, err := userdb.NewUserDB(filepath.Join(t.TempDir(), "user.db"), uid)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		store := userdb.NewSQLiteUserStore(db.DB)
		wrapped := &collectionGuardHTTPStore{UserStore: store, CollectionItemsPager: store, CollectionMutationStore: store, CollectionFeatureProvider: store}
		provider.stores[uid] = wrapped
		if uid == 1 {
			main = wrapped
		}
	}
	deps := pilotDeps(nil, nil)
	deps.PersonalCollections = handlers.NewCollectionHandler(provider)
	return newTestHandler(t, deps), main
}
func guardHTTPCreate(t *testing.T, h http.Handler, body string) string {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/api/v2/collections", body, viewerHeaders())
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID == "" {
		t.Fatal("empty collection ID")
	}
	return "/api/v2/collections/" + out.ID
}
func guardHTTPGet(t *testing.T, h http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	rec := do(t, h, http.MethodGet, path, "", headers)
	if rec.Code != 200 {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body.String())
	}
	if tag := rec.Header().Get("ETag"); tag == "" || strings.HasPrefix(tag, "W/") {
		t.Fatalf("missing strong editor ETag: %q", tag)
	}
	return rec
}

func TestSQLiteCollectionHTTPPreconditionsAndConcurrentCAS(t *testing.T) {
	h, store := newCollectionGuardHTTP(t)
	path := guardHTTPCreate(t, h, `{"name":"Initial","collection_type":"manual"}`)
	initial := guardHTTPGet(t, h, path, viewerHeaders())
	tag := initial.Header().Get("ETag")
	same := guardHTTPGet(t, h, path, viewerHeaders())
	if same.Header().Get("ETag") != tag || same.Body.String() != initial.Body.String() {
		t.Fatal("identical canonical version changed bytes or tag")
	}
	requireProblem(t, do(t, h, http.MethodPatch, path, `{"name":"Missing guard"}`, viewerHeaders()), TypePreconditionRequired)
	afterMissing := guardHTTPGet(t, h, path, viewerHeaders())
	if afterMissing.Body.String() != initial.Body.String() || afterMissing.Header().Get("ETag") != tag {
		t.Fatal("missing guard changed state")
	}
	changed := do(t, h, http.MethodPatch, path, `{"name":"Fresh"}`, with(viewerHeaders(), "If-Match", tag))
	if changed.Code != 200 {
		t.Fatalf("fresh patch: %d %s", changed.Code, changed.Body.String())
	}
	freshTag := changed.Header().Get("ETag")
	if freshTag == "" || freshTag == tag {
		t.Fatal("fresh patch did not advance ETag")
	}
	stale := do(t, h, http.MethodPatch, path, `{"name":"Stale"}`, with(viewerHeaders(), "If-Match", tag))
	requireProblem(t, stale, TypePreconditionFailed)
	if stale.Header().Get("ETag") != freshTag {
		t.Fatalf("stale response did not return current tag: %q", stale.Header().Get("ETag"))
	}
	unchanged := guardHTTPGet(t, h, path, viewerHeaders())
	if unchanged.Body.String() != changed.Body.String() || unchanged.Header().Get("ETag") != freshTag {
		t.Fatal("stale patch changed canonical state")
	}
	// Force both requests past their HTTP read/If-Match evaluation before either
	// reaches SQLite's transaction. Only a real storage CAS can select one winner.
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	store.beforeUpdate = func() { arrived <- struct{}{}; <-release }
	responses := make(chan *httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for _, name := range []string{"Race A", "Race B"} {
		wg.Go(func() {
			responses <- do(t, h, http.MethodPatch, path, fmt.Sprintf(`{"name":%q}`, name), with(viewerHeaders(), "If-Match", freshTag))
		})
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for range 2 {
		select {
		case <-arrived:
		case <-timer.C:
			close(release)
			wg.Wait()
			t.Fatal("both requests did not reach the storage CAS barrier")
		}
	}
	close(release)
	wg.Wait()
	close(responses)
	store.beforeUpdate = nil
	var winner, loser *httptest.ResponseRecorder
	for rec := range responses {
		switch rec.Code {
		case 200:
			if winner != nil {
				t.Fatal("two successful conditional writes")
			}
			winner = rec
		case 412:
			loser = rec
		default:
			t.Fatalf("parallel patch: %d %s", rec.Code, rec.Body.String())
		}
	}
	if winner == nil || loser == nil {
		t.Fatal("expected one winner and one precondition failure")
	}
	current := guardHTTPGet(t, h, path, viewerHeaders())
	if current.Body.String() != winner.Body.String() || current.Header().Get("ETag") != winner.Header().Get("ETag") || loser.Header().Get("ETag") != current.Header().Get("ETag") {
		t.Fatal("parallel result disagrees with committed canonical representation")
	}
	requireProblem(t, do(t, h, http.MethodDelete, path, "", viewerHeaders()), TypePreconditionRequired)
	deleted := do(t, h, http.MethodDelete, path, "", with(viewerHeaders(), "If-Match", current.Header().Get("ETag")))
	if deleted.Code != 204 {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, path, "", viewerHeaders()), TypeNotFound)
}

func TestSQLiteCollectionHTTPProfileAccessAndCapabilities(t *testing.T) {
	h, _ := newCollectionGuardHTTP(t)
	shared := guardHTTPCreate(t, h, `{"name":"Shared","is_shared":true,"allowed_profile_ids":["p-primary"]}`)
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-primary")
	read := guardHTTPGet(t, h, shared, viewer)
	requireProblem(t, do(t, h, http.MethodPatch, shared, `{"name":"Forbidden"}`, with(viewer, "If-Match", read.Header().Get("ETag"))), TypePermissionDenied)
	private := guardHTTPCreate(t, h, `{"name":"Private"}`)
	requireProblem(t, do(t, h, http.MethodGet, private, "", viewer), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPatch, private, `{"name":"Hidden"}`, with(viewer, "If-Match", "*")), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, private, "", with(bearer(adminToken), "X-Profile-Id", "p-owner")), TypeNotFound)
	caps := do(t, h, http.MethodGet, "/api/v2/collections/capabilities", "", viewerHeaders())
	if caps.Code != 200 {
		t.Fatalf("capabilities: %d %s", caps.Code, caps.Body.String())
	}
	var features struct {
		Groups, Imports, Artwork bool
		ItemReorder              bool `json:"item_reorder"`
	}
	if err := json.Unmarshal(caps.Body.Bytes(), &features); err != nil {
		t.Fatal(err)
	}
	if features.Groups || features.Imports || features.Artwork || features.ItemReorder {
		t.Fatalf("SQLite advertised unavailable features: %+v", features)
	}
	unsupported := do(t, h, http.MethodPost, "/api/v2/collections/groups", `{"name":"Unavailable"}`, viewerHeaders())
	if unsupported.Code != 501 {
		t.Fatalf("unsupported groups: %d %s", unsupported.Code, unsupported.Body.String())
	}
}

// Catalog and watch-state changes can alter the displayed count without
// changing the stored collection revision. Exercise that through the real
// editor, guard and mutation paths without needing a database in the HTTP suite.
func TestCollectionHTTPPreconditionsTrackLiveItemCount(t *testing.T) {
	h, store := newCollectionGuardHTTP(t)
	count := 1
	store.liveItemCount = &count
	path := guardHTTPCreate(t, h, `{"name":"Live count"}`)
	initial := guardHTTPGet(t, h, path, viewerHeaders())
	id := strings.TrimPrefix(path, "/api/v2/collections/")
	revision, err := store.CollectionRevision(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	same := guardHTTPGet(t, h, path, viewerHeaders())
	if same.Body.String() != initial.Body.String() || same.Header().Get("ETag") != initial.Header().Get("ETag") {
		t.Fatal("unchanged count and revision changed the response or tag")
	}

	count = 2
	current := guardHTTPGet(t, h, path, viewerHeaders())
	var view PersonalCollection
	if err := json.Unmarshal(current.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ItemCount != count || current.Header().Get("ETag") == initial.Header().Get("ETag") {
		t.Fatal("live count change did not change the body and strong ETag")
	}
	afterRevision, err := store.CollectionRevision(t.Context(), id)
	if err != nil || afterRevision != revision {
		t.Fatalf("count change must leave the stored revision alone: %d -> %d, %v", revision, afterRevision, err)
	}
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		body := ""
		if method == http.MethodPatch {
			body = `{"name":"Stale edit"}`
		}
		stale := do(t, h, method, path, body, with(viewerHeaders(), "If-Match", initial.Header().Get("ETag")))
		requireProblem(t, stale, TypePreconditionFailed)
		if stale.Header().Get("ETag") != current.Header().Get("ETag") {
			t.Fatal("stale count response omitted the current validator")
		}
	}
	unchanged := guardHTTPGet(t, h, path, viewerHeaders())
	if unchanged.Body.String() != current.Body.String() || unchanged.Header().Get("ETag") != current.Header().Get("ETag") {
		t.Fatal("a stale request changed the collection")
	}
	fresh := do(t, h, http.MethodPatch, path, `{"name":"Fresh edit"}`, with(viewerHeaders(), "If-Match", current.Header().Get("ETag")))
	if fresh.Code != http.StatusOK || fresh.Header().Get("ETag") == current.Header().Get("ETag") {
		t.Fatalf("fresh edit did not succeed and advance the tag: %d %s", fresh.Code, fresh.Body.String())
	}
	count = 3
	wildcard := do(t, h, http.MethodDelete, path, "", with(viewerHeaders(), "If-Match", "*"))
	if wildcard.Code != http.StatusNoContent {
		t.Fatalf("explicit overwrite failed after another count change: %d %s", wildcard.Code, wildcard.Body.String())
	}
}
