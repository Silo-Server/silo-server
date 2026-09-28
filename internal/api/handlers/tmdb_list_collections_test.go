package handlers

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestBuildTMDBListSourceConfig(t *testing.T) {
	limit := 30
	raw, err := buildTMDBListSourceConfig("https://www.themoviedb.org/list/310", &limit)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw), `{"mode":"tmdb_list","url":"https://www.themoviedb.org/list/310","limit":30}`; got != want {
		t.Fatalf("source config = %s, want %s", got, want)
	}
}

func TestAdminTMDBListImportRejectsNonListURL(t *testing.T) {
	_, err := (&LibraryCollectionHandler{}).ImportAdminTMDBList(t.Context(), AdminCollectionImportTMDBList{
		LibraryIDs: []int{1}, Title: "List", URL: "https://www.themoviedb.org/collection/10",
	})
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != 400 || apiErr.Code != "bad_request" {
		t.Fatalf("error = %#v, want 400 bad_request", err)
	}
}

func TestGenericAdminCollectionRejectsInvalidTMDBListSource(t *testing.T) {
	_, err := (&LibraryCollectionHandler{}).CreateAdminCollection(t.Context(), AdminCollectionCreate{
		LibraryIDs: []int{1}, Title: "List", CollectionType: "tmdb",
		SourceConfig: json.RawMessage(`{"mode":"tmdb_list","url":"https://example.com/list/310"}`),
	})
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != 400 {
		t.Fatalf("error = %#v, want 400", err)
	}
}

func TestAdminCollectionSourceUpdateValidatesTMDBListURL(t *testing.T) {
	existing := &models.LibraryCollection{
		CollectionType: "tmdb",
		SourceURL:      "https://www.themoviedb.org/list/310",
		SourceConfig:   json.RawMessage(`{"mode":"tmdb_list","url":"https://www.themoviedb.org/list/310"}`),
	}
	good := json.RawMessage(`{"mode":"tmdb_list","url":"https://www.themoviedb.org/list/8649937-marvel"}`)
	if err := validateAdminCollectionSourceUpdate(existing, AdminCollectionUpdate{SourceConfig: good}); err != nil {
		t.Fatalf("valid list URL rejected: %v", err)
	}
	bad := json.RawMessage(`{"mode":"tmdb_list","url":"https://www.themoviedb.org/movie/550"}`)
	err := validateAdminCollectionSourceUpdate(existing, AdminCollectionUpdate{SourceConfig: bad})
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Status != 400 {
		t.Fatalf("error = %#v, want 400", err)
	}
	// Other TMDB modes are not list-validated.
	preset := json.RawMessage(`{"mode":"tmdb_preset","preset":"trending","media_type":"all","time_window":"day"}`)
	if err := validateAdminCollectionSourceUpdate(existing, AdminCollectionUpdate{SourceConfig: preset}); err != nil {
		t.Fatalf("preset config rejected: %v", err)
	}
}

func TestPersonalTMDBListImportRejectsNonListURL(t *testing.T) {
	_, err := (&UserCollectionImportHandler{}).ImportTMDBList(t.Context(), 1, "p1", UserImportTMDBListRequest{
		UserImportSharedFields: UserImportSharedFields{Title: "List"},
		URL:                    "https://mdblist.com/lists/user/slug",
	})
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Field != "url" {
		t.Fatalf("error = %#v, want a url field error", err)
	}
}

func TestPersonalTMDBListSourceURLCanBeEdited(t *testing.T) {
	store := &lifecycleStore{collection: userstore.Collection{
		ID: "c", CreatorProfileID: "owner", AllowedProfileIDs: []string{"owner"},
		CollectionType: "tmdb", SourceURL: "https://www.themoviedb.org/list/310",
		SourceConfig: `{"mode":"tmdb_list","url":"https://www.themoviedb.org/list/310"}`,
	}}
	h := NewCollectionHandler(lifecycleProvider{store: store})
	next := "https://www.themoviedb.org/list/8649937-marvel?language=en"
	if _, err := h.UpdatePersonalCollection(t.Context(), PersonalCollectionUpdateCommand{
		UserID: 1, ProfileID: "owner", CollectionID: "c",
		Request: PersonalCollectionUpdateRequest{SourceURL: &next},
	}); err != nil {
		t.Fatal(err)
	}
	const want = "https://www.themoviedb.org/list/8649937"
	if store.update.SourceURL == nil || *store.update.SourceURL != want {
		t.Fatalf("source_url = %v, want %s", store.update.SourceURL, want)
	}
	var patch map[string]any
	if err := json.Unmarshal([]byte(*store.update.SourceConfigPatch), &patch); err != nil {
		t.Fatal(err)
	}
	if patch["url"] != want {
		t.Fatalf("source config patch = %#v, want url %s", patch, want)
	}

	bad := "https://www.themoviedb.org/movie/550"
	_, err := h.UpdatePersonalCollection(t.Context(), PersonalCollectionUpdateCommand{
		UserID: 1, ProfileID: "owner", CollectionID: "c",
		Request: PersonalCollectionUpdateRequest{SourceURL: &bad},
	})
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Field != "source_url" {
		t.Fatalf("error = %#v, want a source_url field error", err)
	}
}

func TestPersonalTMDBPresetSourceURLCannotBeEdited(t *testing.T) {
	store := &lifecycleStore{collection: userstore.Collection{
		ID: "c", CreatorProfileID: "owner", AllowedProfileIDs: []string{"owner"},
		CollectionType: "tmdb", SourceConfig: `{"mode":"tmdb_preset","preset":"trending"}`,
	}}
	h := NewCollectionHandler(lifecycleProvider{store: store})
	next := "https://www.themoviedb.org/list/310"
	_, err := h.UpdatePersonalCollection(t.Context(), PersonalCollectionUpdateCommand{
		UserID: 1, ProfileID: "owner", CollectionID: "c",
		Request: PersonalCollectionUpdateRequest{SourceURL: &next},
	})
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Field != "source_url" {
		t.Fatalf("error = %#v, want a source_url field error", err)
	}
	if store.mutations != 0 {
		t.Fatalf("rejected edit caused %d mutations", store.mutations)
	}
}
