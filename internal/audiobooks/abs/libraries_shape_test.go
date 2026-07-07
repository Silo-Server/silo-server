package abs

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type libStub struct {
	noopMediaStore
	libs []AudiobookLibrary
}

func (s *libStub) ListAudiobookLibraries(_ context.Context, _ catalog.AccessFilter) ([]AudiobookLibrary, error) {
	return s.libs, nil
}

// TestAudiobookLibraryMap_FullShape asserts the library object matches real
// ABS Library.toOldJSON (12 keys) — a strict client decodes the library model
// and crashes on any missing key.
func TestAudiobookLibraryMap_FullShape(t *testing.T) {
	body, _ := json.Marshal(audiobookLibraryMap(AudiobookLibrary{ID: 18, Name: "Audiobooks"}))
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, k := range []string{
		"id", "name", "folders", "displayOrder", "icon", "mediaType",
		"provider", "settings", "lastScan", "lastScanVersion", "createdAt", "lastUpdate",
	} {
		if _, ok := m[k]; !ok {
			t.Errorf("library object missing key %q", k)
		}
	}
	folders, _ := m["folders"].([]any)
	if len(folders) != 1 {
		t.Fatalf("folders len = %d, want 1", len(folders))
	}
	f0, _ := folders[0].(map[string]any)
	for _, k := range []string{"id", "fullPath", "libraryId", "addedAt"} {
		if _, ok := f0[k]; !ok {
			t.Errorf("folder missing key %q", k)
		}
	}
}

// TestLibraryDetail_UnwrappedWithoutInclude: real ABS findOne returns the
// library object directly (not { library: ... }) when no include=filterdata.
func TestLibraryDetail_UnwrappedWithoutInclude(t *testing.T) {
	media := &libStub{libs: []AudiobookLibrary{{ID: 18, Name: "Audiobooks", Type: "audiobooks"}}}
	h := New(Dependencies{MediaStore: media})

	rec := dispatchABSWithParams(http.MethodGet, "/api/libraries/18", map[string]string{"libraryId": "18"}, nil, "1", "", h.handleLibraryDetail)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, wrapped := got["library"]; wrapped {
		t.Errorf("response must be unwrapped (no 'library' key) without include")
	}
	if got["id"] != "18" {
		t.Errorf("id = %v, want 18 (unwrapped library object)", got["id"])
	}
	if _, ok := got["folders"]; !ok {
		t.Errorf("unwrapped library missing 'folders'")
	}
}

// TestLibraryDetail_WrappedWithInclude: with include=filterdata the response
// wraps in { filterdata, issues, numUserPlaylists, customMetadataProviders, library }.
func TestLibraryDetail_WrappedWithInclude(t *testing.T) {
	media := &libStub{libs: []AudiobookLibrary{{ID: 18, Name: "Audiobooks", Type: "audiobooks"}}}
	h := New(Dependencies{MediaStore: media})

	rec := dispatchABSWithParams(http.MethodGet, "/api/libraries/18?include=filterdata", map[string]string{"libraryId": "18"}, nil, "1", "", h.handleLibraryDetail)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, k := range []string{"filterdata", "issues", "numUserPlaylists", "customMetadataProviders", "library"} {
		if _, ok := got[k]; !ok {
			t.Errorf("include=filterdata response missing key %q", k)
		}
	}
}

// TestLibraries_WrappedEnvelope: GET /libraries returns { libraries: [...] }.
func TestLibraries_WrappedEnvelope(t *testing.T) {
	media := &libStub{libs: []AudiobookLibrary{{ID: 18, Name: "Audiobooks", Type: "audiobooks"}}}
	h := New(Dependencies{MediaStore: media})

	rec := dispatchABSWithParams(http.MethodGet, "/api/libraries", nil, nil, "1", "", h.handleLibraries)
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	libs, ok := got["libraries"].([]any)
	if !ok {
		t.Fatalf("missing 'libraries' array; keys %v", keysOf(got))
	}
	if len(libs) != 1 {
		t.Errorf("libraries len = %d, want 1", len(libs))
	}
}
