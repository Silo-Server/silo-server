package abs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// inProgressStubMediaStore backs the /me/items-in-progress shape tests.
type inProgressStubMediaStore struct {
	noopMediaStore
	libs []AudiobookLibrary
	byID map[string]*models.MediaItem
}

func (s *inProgressStubMediaStore) ListAudiobookLibraries(context.Context, catalog.AccessFilter) ([]AudiobookLibrary, error) {
	return s.libs, nil
}

func (s *inProgressStubMediaStore) GetAudiobooksByIDs(_ context.Context, ids []string, _ catalog.AccessFilter) (map[string]*models.MediaItem, error) {
	out := make(map[string]*models.MediaItem, len(ids))
	for _, id := range ids {
		if it, ok := s.byID[id]; ok {
			out[id] = it
		}
	}
	return out, nil
}

// TestItemsInProgress_Unauthenticated_401 covers the auth guard: no
// ctxAuth in the request context (bearerAuth middleware never ran).
func TestItemsInProgress_Unauthenticated_401(t *testing.T) {
	h := New(Dependencies{MediaStore: &inProgressStubMediaStore{}})
	req := httptest.NewRequest(http.MethodGet, "/api/me/items-in-progress", nil)
	rec := httptest.NewRecorder()
	h.handleItemsInProgress(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// listedProgressStore returns fixed rows from ListProgressForAudiobooks and
// looks single rows up among them.
type listedProgressStore struct {
	fakeProgressStore
	rows []ProgressRow
	err  error
}

func (s *listedProgressStore) ListProgressForAudiobooks(context.Context, string, string, int) ([]ProgressRow, error) {
	return s.rows, s.err
}

func (s *listedProgressStore) GetProgress(_ context.Context, _, _, contentID string) (*ProgressRow, error) {
	if s.err != nil {
		return nil, s.err
	}
	for i := range s.rows {
		if s.rows[i].ContentID == contentID {
			return &s.rows[i], nil
		}
	}
	return nil, nil
}

// progressHandler serves one visible book ("seen") with saved progress, plus
// a progress row for a book the caller cannot see ("hidden").
func progressHandler() *Handler {
	seen := &models.MediaItem{ContentID: "seen", Type: "audiobook", Title: "Seen"}
	return New(Dependencies{
		MediaStore: &inProgressStubMediaStore{byID: map[string]*models.MediaItem{"seen": seen}},
		ProgressStore: &listedProgressStore{rows: []ProgressRow{
			{UserID: "1", ContentID: "seen", CurrentSeconds: 123.5, DurationSeconds: 600, ProgressPct: 0.2, HideFromContinue: true, UpdatedAt: time.Unix(100, 0)},
			{UserID: "1", ContentID: "hidden", CurrentSeconds: 50, DurationSeconds: 600, ProgressPct: 0.1, UpdatedAt: time.Unix(90, 0)},
		}},
	})
}

func progressItemIDs(t *testing.T, raw any) []string {
	t.Helper()
	rows, ok := raw.([]any)
	if !ok {
		t.Fatalf("mediaProgress = %#v, want an array", raw)
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.(map[string]any)["libraryItemId"].(string))
	}
	return ids
}

// The user object carries the caller's progress rows, like real ABS
// User.toOldJSONForBrowser, so clients that seed resume positions from login
// or /me see them; rows for books outside the caller's access are dropped.
func TestUserObjectCarriesAccessibleMediaProgress(t *testing.T) {
	h := progressHandler()

	rec := dispatchABSWithParams(http.MethodGet, "/api/me", nil, nil, "1", "p1", h.handleMe)
	if rec.Code != http.StatusOK {
		t.Fatalf("/me status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var me map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatalf("decode /me: %v", err)
	}
	if got := progressItemIDs(t, me["mediaProgress"]); !slices.Equal(got, []string{"seen"}) {
		t.Errorf("/me mediaProgress items = %v, want [seen]", got)
	} else if row := me["mediaProgress"].([]any)[0].(map[string]any); row["hideFromContinueListening"] != true || row["mediaItemType"] != "book" {
		t.Errorf("/me mediaProgress row = %v, want the stored hide flag and mediaItemType book", row)
	}

	env := h.loginEnvelope(t.Context(), time.Unix(100, 0), ctxAuth{UserID: "1", ProfileID: "p1"}, "Reader", "a", "r", false)
	user := env["user"].(map[string]any)
	b, _ := json.Marshal(user["mediaProgress"])
	var loginProgress any
	_ = json.Unmarshal(b, &loginProgress)
	if got := progressItemIDs(t, loginProgress); !slices.Equal(got, []string{"seen"}) {
		t.Errorf("login user.mediaProgress items = %v, want [seen]", got)
	}
}

// A failed progress read leaves the user object's mediaProgress empty
// instead of failing sign-in.
func TestUserObjectProgressReadFailureKeepsLogin(t *testing.T) {
	h := New(Dependencies{
		MediaStore:    &inProgressStubMediaStore{},
		ProgressStore: &listedProgressStore{err: errors.New("db down")},
	})
	env := h.loginEnvelope(t.Context(), time.Unix(100, 0), ctxAuth{UserID: "1", ProfileID: "p1"}, "Reader", "a", "r", false)
	b, _ := json.Marshal(env["user"].(map[string]any)["mediaProgress"])
	if string(b) != "[]" {
		t.Errorf("login user.mediaProgress = %s, want []", b)
	}
}

// Item detail with include=progress carries userMediaProgress (null when the
// caller has none), as real ABS LibraryItemController.findOne does.
func TestItemDetailIncludeProgress(t *testing.T) {
	saved := []ProgressRow{{UserID: "1", ContentID: "seen", CurrentSeconds: 123.5, DurationSeconds: 600, ProgressPct: 0.2, UpdatedAt: time.Unix(100, 0)}}
	for _, tc := range []struct {
		name, query string
		rows        []ProgressRow
		err         error
		wantKey     bool
		wantTime    any // nil means userMediaProgress is null
	}{
		{"saved progress", "?expanded=1&include=progress", saved, nil, true, 123.5},
		{"no saved progress", "?expanded=1&include=progress", nil, nil, true, nil},
		{"no include", "?expanded=1", saved, nil, false, nil},
		{"progress read fails", "?expanded=1&include=progress", nil, errors.New("db down"), true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := New(Dependencies{
				MediaStore: &accessMediaStore{
					item:  &models.MediaItem{ContentID: "seen", Type: "audiobook", Title: "Seen"},
					files: []*models.MediaFile{{MediaFolderID: 1}},
				},
				ProgressStore: &listedProgressStore{rows: tc.rows, err: tc.err},
			})
			rec := dispatchABSWithParams(http.MethodGet, "/api/items/seen"+tc.query, map[string]string{"id": "seen"}, nil, "1", "p1", h.handleItem)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
			}
			var got map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			raw, has := got["userMediaProgress"]
			if has != tc.wantKey {
				t.Fatalf("userMediaProgress present = %v, want %v", has, tc.wantKey)
			}
			if tc.wantTime == nil {
				if raw != nil {
					t.Errorf("userMediaProgress = %#v, want null", raw)
				}
				return
			}
			p, _ := raw.(map[string]any)
			if p == nil || p["currentTime"] != tc.wantTime || p["libraryItemId"] != "seen" {
				t.Errorf("userMediaProgress = %#v, want currentTime %v for seen", raw, tc.wantTime)
			}
		})
	}
}
