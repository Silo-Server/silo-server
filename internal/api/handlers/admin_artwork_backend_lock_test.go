package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

func withURLParam(req *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestArtworkBackendLocksOnceStorageIsRecorded(t *testing.T) {
	put := func(h *AdminHandler, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.HandleUpdateSettings(rec, httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(body)))
		return rec
	}
	putOne := func(h *AdminHandler, key, value string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/admin/settings/"+key, strings.NewReader(`{"value":"`+value+`"}`))
		h.HandleUpdateSetting(rec, withURLParam(req, "key", key))
		return rec
	}

	// Before any artwork is stored the backend is free to change.
	open := &fakeServerSettingsStore{values: map[string]string{"s3.public_endpoint": "https://s3.example", "s3.public_bucket": "artwork"}}
	if rec := put(&AdminHandler{SettingsRepo: open}, `{"values":{"artwork.storage_backend":"s3"}}`); rec.Code != http.StatusOK {
		t.Fatalf("unlocked batch write: %d %s", rec.Code, rec.Body.String())
	}

	locked := &fakeServerSettingsStore{values: map[string]string{
		"artwork.storage_backend":    "local",
		"s3.public_endpoint":         "https://s3.example",
		"s3.public_bucket":           "artwork",
		blobstore.IdentitySettingKey: "local|/var/lib/silo/artwork",
	}}
	h := &AdminHandler{SettingsRepo: locked}
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"batch":  put(h, `{"values":{"artwork.storage_backend":"s3","metadata.cache_images":"true"}}`),
		"single": putOne(h, "artwork.storage_backend", "s3"),
	} {
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "artwork_storage_locked") {
			t.Fatalf("%s write after storage recorded: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if locked.values["artwork.storage_backend"] != "local" || locked.values["metadata.cache_images"] != "" {
		t.Fatalf("locked write mutated settings: %#v", locked.values)
	}
	// Re-saving the current value is a no-op, not a conflict, so forms that
	// submit every key keep working.
	if rec := put(h, `{"values":{"artwork.storage_backend":"local","metadata.cache_images":"true"}}`); rec.Code != http.StatusOK {
		t.Fatalf("same-value write: %d %s", rec.Code, rec.Body.String())
	}
	// The recorded default is auto when the row was never written.
	defaulted := &fakeServerSettingsStore{values: map[string]string{
		"s3.public_endpoint":         "https://s3.example",
		"s3.public_bucket":           "artwork",
		blobstore.IdentitySettingKey: "s3|https://s3.example|artwork|",
	}}
	if rec := putOne(&AdminHandler{SettingsRepo: defaulted}, "artwork.storage_backend", "auto"); rec.Code != http.StatusOK {
		t.Fatalf("auto on an unset locked row: %d %s", rec.Code, rec.Body.String())
	}
	if rec := putOne(&AdminHandler{SettingsRepo: defaulted}, "artwork.storage_backend", "local"); rec.Code != http.StatusConflict {
		t.Fatalf("local on a locked s3 store: %d %s", rec.Code, rec.Body.String())
	}
}

// Every setting the recorded identity is built from is locked, not only the
// backend selector: the local root, and for S3 the endpoint, bucket, and key
// prefix. An auto backend that resolved to local also may not gain a bucket,
// because that flips the resolution on restart.
func TestArtworkIdentityFieldsLockOnceStorageIsRecorded(t *testing.T) {
	putOne := func(h *AdminHandler, key, value string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/admin/settings/"+key, strings.NewReader(`{"value":"`+value+`"}`))
		h.HandleUpdateSetting(rec, withURLParam(req, "key", key))
		return rec
	}
	put := func(h *AdminHandler, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.HandleUpdateSettings(rec, httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(body)))
		return rec
	}
	conflict := func(t *testing.T, name string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "artwork_storage_locked") {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	ok := func(t *testing.T, name string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}

	local := func() *fakeServerSettingsStore {
		return &fakeServerSettingsStore{values: map[string]string{
			"artwork.storage_backend":    "local",
			blobstore.IdentitySettingKey: "local|/var/lib/silo/artwork",
		}}
	}
	conflict(t, "local path", putOne(&AdminHandler{SettingsRepo: local()}, "artwork.local_path", "/srv/artwork"))
	ok(t, "same local path", putOne(&AdminHandler{SettingsRepo: local()}, "artwork.local_path", "/var/lib/silo/artwork"))
	// An explicit local backend ignores the public bucket, so adding one is
	// allowed: the artwork location does not move.
	ok(t, "bucket under explicit local", put(&AdminHandler{SettingsRepo: local()},
		`{"values":{"s3.public_endpoint":"https://s3.example","s3.public_bucket":"media"}}`))

	autoLocal := &fakeServerSettingsStore{values: map[string]string{
		blobstore.IdentitySettingKey: "local|/var/lib/silo/artwork",
	}}
	conflict(t, "bucket under auto-local", put(&AdminHandler{SettingsRepo: autoLocal},
		`{"values":{"s3.public_endpoint":"https://s3.example","s3.public_bucket":"media"}}`))
	if autoLocal.values["s3.public_bucket"] != "" {
		t.Fatalf("rejected write persisted: %#v", autoLocal.values)
	}

	s3 := func() *fakeServerSettingsStore {
		return &fakeServerSettingsStore{values: map[string]string{
			"artwork.storage_backend":    "s3",
			"s3.public_endpoint":         "https://s3.example",
			"s3.public_bucket":           "artwork",
			"s3.public_key_prefix":       "cache",
			blobstore.IdentitySettingKey: "s3|https://s3.example|artwork|cache",
		}}
	}
	for key, value := range map[string]string{
		"s3.public_endpoint":   "https://other.example",
		"s3.public_bucket":     "other",
		"s3.public_key_prefix": "elsewhere",
	} {
		conflict(t, key, putOne(&AdminHandler{SettingsRepo: s3()}, key, value))
	}
	// The identity does not include credentials or the read endpoint, so
	// rotating those stays allowed.
	ok(t, "read endpoint", putOne(&AdminHandler{SettingsRepo: s3()}, "s3.public_read_endpoint", "https://cdn.example"))
	ok(t, "same bucket", put(&AdminHandler{SettingsRepo: s3()},
		`{"values":{"s3.public_endpoint":"https://s3.example","s3.public_bucket":"artwork","s3.public_key_prefix":"cache"}}`))
	// Clearing the bucket would resolve the s3 backend to nothing; that is an
	// identity change as well as an invalid configuration.
	if rec := putOne(&AdminHandler{SettingsRepo: s3()}, "s3.public_bucket", ""); rec.Code == http.StatusOK {
		t.Fatalf("clearing the recorded bucket: %d %s", rec.Code, rec.Body.String())
	}
}

func TestArtworkS3BackendRequiresPublicBucket(t *testing.T) {
	putOne := func(h *AdminHandler, key, value string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/admin/settings/"+key, strings.NewReader(`{"value":"`+value+`"}`))
		h.HandleUpdateSetting(rec, withURLParam(req, "key", key))
		return rec
	}
	empty := &fakeServerSettingsStore{values: map[string]string{}}
	if rec := putOne(&AdminHandler{SettingsRepo: empty}, "artwork.storage_backend", "s3"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "s3.public_bucket") {
		t.Fatalf("s3 backend without a bucket: %d %s", rec.Code, rec.Body.String())
	}
	rec := httptest.NewRecorder()
	(&AdminHandler{SettingsRepo: empty}).HandleUpdateSettings(rec, httptest.NewRequest(http.MethodPut, "/admin/settings",
		strings.NewReader(`{"values":{"artwork.storage_backend":"s3"}}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("batch s3 backend without a bucket: %d %s", rec.Code, rec.Body.String())
	}
	if empty.values["artwork.storage_backend"] != "" {
		t.Fatalf("invalid write persisted: %#v", empty.values)
	}
	withBucket := &fakeServerSettingsStore{values: map[string]string{
		"s3.public_endpoint": "https://s3.example", "s3.public_bucket": "artwork",
	}}
	if rec := putOne(&AdminHandler{SettingsRepo: withBucket}, "artwork.storage_backend", "s3"); rec.Code != http.StatusOK {
		t.Fatalf("s3 backend with a bucket: %d %s", rec.Code, rec.Body.String())
	}
	if rec := putOne(&AdminHandler{SettingsRepo: withBucket}, "s3.public_bucket", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("clearing the bucket under an s3 backend: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAdminServerStatusReportsArtworkStorageLock(t *testing.T) {
	read := func(h *AdminHandler) adminArtworkStorageStatus {
		t.Helper()
		rec := httptest.NewRecorder()
		h.HandleGetServerStatus(rec, httptest.NewRequest(http.MethodGet, "/admin/server/status", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var body struct {
			ArtworkStorage adminArtworkStorageStatus `json:"artwork_storage"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body.ArtworkStorage
	}
	fresh := &AdminHandler{RestartStatus: NewServerRestartStatusTracker(), ArtworkBackend: "local", SettingsRepo: &fakeServerSettingsStore{values: map[string]string{}}}
	if got := read(fresh); got.Locked || got.Backend != "local" {
		t.Fatalf("fresh install: %+v", got)
	}
	recorded := &AdminHandler{RestartStatus: NewServerRestartStatusTracker(), ArtworkBackend: "s3", SettingsRepo: &fakeServerSettingsStore{values: map[string]string{blobstore.IdentitySettingKey: "s3|https://s3.example|artwork|"}}}
	if got := read(recorded); !got.Locked || got.Backend != "s3" {
		t.Fatalf("recorded storage: %+v", got)
	}
}
