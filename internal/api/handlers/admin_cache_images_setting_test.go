package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// metadata.cache_images copies provider artwork into the public bucket, so both
// write paths reject enabling it when no public bucket exists anywhere. A saved
// but not-yet-active bucket counts: the setup wizard configures the bucket and
// enables caching in one batch, and the UI badges the pending restart.

func cacheImagesHandler(settings *fakeServerSettingsStore, storage bool) *AdminHandler {
	h := &AdminHandler{SettingsRepo: settings}
	if storage {
		h.PublicStorageConfigured = func() bool { return true }
	}
	return h
}

func updateCacheImagesBatch(h *AdminHandler, values string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.HandleUpdateSettings(rec, httptest.NewRequest(
		http.MethodPut,
		"/admin/settings",
		strings.NewReader(`{"values":{`+values+`}}`),
	))
	return rec
}

func updateSingleSetting(h *AdminHandler, key, value string) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	router.Put("/admin/settings/{key}", h.HandleUpdateSetting)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPut,
		"/admin/settings/"+key,
		strings.NewReader(`{"value":"`+value+`"}`),
	))
	return rec
}

func updateCacheImagesSingle(h *AdminHandler, value string) *httptest.ResponseRecorder {
	return updateSingleSetting(h, "metadata.cache_images", value)
}

func assertStorageUnavailable(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	var body errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "storage_unavailable" {
		t.Fatalf("error code = %q, want storage_unavailable; body=%#v", body.Error, body)
	}
	if !strings.Contains(body.Message, "S3 image caching requires a configured public storage bucket") {
		t.Fatalf("error message = %q", body.Message)
	}
}

func TestCacheImagesEnableRequiresPublicBucket(t *testing.T) {
	const cacheImagesTrue = `"metadata.cache_images":"true"`

	t.Run("batch with active storage", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{}}
		rec := updateCacheImagesBatch(cacheImagesHandler(settings, true), cacheImagesTrue)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if settings.values["metadata.cache_images"] != "true" {
			t.Fatalf("stored values = %#v", settings.values)
		}
	})

	t.Run("batch with saved bucket but inactive store", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{"s3.public_bucket": "silo-public"}}
		rec := updateCacheImagesBatch(cacheImagesHandler(settings, false), cacheImagesTrue)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if settings.values["metadata.cache_images"] != "true" {
			t.Fatalf("stored values = %#v", settings.values)
		}
	})

	// The setup wizard writes both in one request, before any restart.
	t.Run("batch configuring the bucket in the same request", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{}}
		rec := updateCacheImagesBatch(cacheImagesHandler(settings, false),
			cacheImagesTrue+`,"s3.public_endpoint":"https://s3.example.com","s3.public_bucket":"silo-public"`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if settings.values["metadata.cache_images"] != "true" ||
			settings.values["s3.public_bucket"] != "silo-public" {
			t.Fatalf("stored values = %#v", settings.values)
		}
	})

	// The batch is validated inside the settings transaction, so it reads the
	// stored values but must not write any of them.
	t.Run("batch with no bucket anywhere", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{}}
		rec := updateCacheImagesBatch(cacheImagesHandler(settings, false), cacheImagesTrue)
		assertStorageUnavailable(t, rec)
		if settings.setManyCalls != 0 || settings.setCalls != 0 {
			t.Fatalf("write attempted: setMany=%d set=%d", settings.setManyCalls, settings.setCalls)
		}
		if _, stored := settings.values["metadata.cache_images"]; stored {
			t.Fatalf("stored values = %#v", settings.values)
		}
	})

	// Clearing the bucket in the same batch must not count as configuring one.
	t.Run("batch clearing the bucket while enabling", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{}}
		rec := updateCacheImagesBatch(cacheImagesHandler(settings, false),
			cacheImagesTrue+`,"s3.public_bucket":""`)
		assertStorageUnavailable(t, rec)
	})

	// The batch persists atomically, so a bucket it clears is gone even though
	// the store — and the process still running on it — have one right now.
	t.Run("batch clearing a stored bucket while enabling", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{"s3.public_bucket": "silo-public"}}
		rec := updateCacheImagesBatch(cacheImagesHandler(settings, true),
			cacheImagesTrue+`,"s3.public_bucket":""`)
		assertStorageUnavailable(t, rec)
		if settings.setManyCalls != 0 || settings.setCalls != 0 {
			t.Fatalf("write attempted: setMany=%d set=%d", settings.setManyCalls, settings.setCalls)
		}
		if settings.values["s3.public_bucket"] != "silo-public" {
			t.Fatalf("stored values = %#v", settings.values)
		}
	})

	// The dual: caching is already on and the batch never resubmits it, so only
	// the complete prospective state catches the storage disappearing.
	t.Run("batch clearing the bucket while caching stays on", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{
			"metadata.cache_images": "true",
			"s3.public_bucket":      "silo-public",
		}}
		rec := updateCacheImagesBatch(cacheImagesHandler(settings, true), `"s3.public_bucket":""`)
		assertStorageUnavailable(t, rec)
		if settings.values["s3.public_bucket"] != "silo-public" {
			t.Fatalf("stored values = %#v", settings.values)
		}
	})

	t.Run("batch clearing the public endpoint and bucket while caching stays on", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{
			"metadata.cache_images": "true",
			"s3.public_endpoint":    "https://s3.example.com",
			"s3.public_bucket":      "silo-public",
		}}
		rec := updateCacheImagesBatch(cacheImagesHandler(settings, true),
			`"s3.public_endpoint":"","s3.public_bucket":""`)
		assertStorageUnavailable(t, rec)
		if settings.values["s3.public_bucket"] != "silo-public" {
			t.Fatalf("stored values = %#v", settings.values)
		}
	})

	// Turning caching off in the same batch is the documented way out.
	t.Run("batch disabling caching while clearing the bucket", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{
			"metadata.cache_images": "true",
			"s3.public_endpoint":    "https://s3.example.com",
			"s3.public_bucket":      "silo-public",
		}}
		rec := updateCacheImagesBatch(cacheImagesHandler(settings, true),
			`"metadata.cache_images":"false","s3.public_endpoint":"","s3.public_bucket":""`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if settings.values["metadata.cache_images"] != "false" ||
			settings.values["s3.public_bucket"] != "" {
			t.Fatalf("stored values = %#v", settings.values)
		}
	})

	// LoadFromDB falls back to the pre-rename bucket key, so clearing only the
	// canonical one still leaves the cacher a bucket.
	t.Run("batch clearing the bucket while the legacy bucket remains", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{
			"metadata.cache_images": "true",
			"s3.public_endpoint":    "https://s3.example.com",
			"s3.public_bucket":      "silo-public",
			"s3.operational_bucket": "silo-legacy",
		}}
		rec := updateCacheImagesBatch(cacheImagesHandler(settings, true), `"s3.public_bucket":""`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if settings.values["s3.public_bucket"] != "" {
			t.Fatalf("stored values = %#v", settings.values)
		}
	})

	t.Run("batch disable with no bucket", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{"metadata.cache_images": "true"}}
		rec := updateCacheImagesBatch(cacheImagesHandler(settings, false), `"metadata.cache_images":"false"`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if settings.values["metadata.cache_images"] != "false" {
			t.Fatalf("stored values = %#v", settings.values)
		}
	})

	t.Run("single with active storage", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{}}
		rec := updateCacheImagesSingle(cacheImagesHandler(settings, true), "true")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if settings.values["metadata.cache_images"] != "true" {
			t.Fatalf("stored values = %#v", settings.values)
		}
	})

	t.Run("single with saved bucket but inactive store", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{"s3.public_bucket": "silo-public"}}
		rec := updateCacheImagesSingle(cacheImagesHandler(settings, false), "true")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if settings.values["metadata.cache_images"] != "true" {
			t.Fatalf("stored values = %#v", settings.values)
		}
	})

	t.Run("single with an environment-supplied bucket", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{}}
		h := cacheImagesHandler(settings, false)
		h.BootstrapSensitiveConfigured = map[string]bool{"s3.public_bucket": true}
		h.BootstrapSensitiveValues = map[string]string{"s3.public_bucket": "silo-public"}
		rec := updateCacheImagesSingle(h, "true")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if settings.values["metadata.cache_images"] != "true" {
			t.Fatalf("stored values = %#v", settings.values)
		}
	})

	t.Run("single with no bucket anywhere", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{}}
		rec := updateCacheImagesSingle(cacheImagesHandler(settings, false), "true")
		assertStorageUnavailable(t, rec)
		if settings.setCalls != 0 {
			t.Fatalf("Set calls = %d, want 0", settings.setCalls)
		}
		if _, stored := settings.values["metadata.cache_images"]; stored {
			t.Fatalf("stored values = %#v", settings.values)
		}
	})

	t.Run("single disable with no bucket", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{"metadata.cache_images": "true"}}
		rec := updateCacheImagesSingle(cacheImagesHandler(settings, false), "false")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if settings.values["metadata.cache_images"] != "false" {
			t.Fatalf("stored values = %#v", settings.values)
		}
	})

	// The legacy single-key route must hold the same line as the batch:
	// clearing the bucket while caching is stored on strands the cacher.
	t.Run("single bucket clear while caching is on", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{
			"metadata.cache_images": "true",
			"s3.public_bucket":      "silo-public",
		}}
		rec := updateSingleSetting(cacheImagesHandler(settings, true), "s3.public_bucket", "")
		assertStorageUnavailable(t, rec)
		if settings.values["s3.public_bucket"] != "silo-public" {
			t.Fatalf("stored values = %#v", settings.values)
		}
	})

	t.Run("single bucket clear while caching is off", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{
			"s3.public_bucket": "silo-public",
		}}
		rec := updateSingleSetting(cacheImagesHandler(settings, false), "s3.public_bucket", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if settings.values["s3.public_bucket"] != "" {
			t.Fatalf("stored values = %#v", settings.values)
		}
	})

	t.Run("single bucket change to a new value while caching is on", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{
			"metadata.cache_images": "true",
			"s3.public_bucket":      "silo-public",
		}}
		rec := updateSingleSetting(cacheImagesHandler(settings, false), "s3.public_bucket", "silo-art")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if settings.values["s3.public_bucket"] != "silo-art" {
			t.Fatalf("stored values = %#v", settings.values)
		}
	})
}
