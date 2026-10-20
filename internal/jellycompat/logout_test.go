package jellycompat

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionStoreDeleteResult(t *testing.T) {
	storeErr := errors.New("inert deletion failure")
	for _, tc := range []struct {
		name      string
		stored    bool
		deleteErr error
		wantErr   error
	}{
		{name: "stored", stored: true},
		{name: "absent", deleteErr: ErrSessionNotFound},
		{name: "wrapped absent", deleteErr: fmt.Errorf("delete: %w", ErrSessionNotFound)},
		{name: "failure", stored: true, deleteErr: storeErr, wantErr: storeErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const token = "inert-session"
			repo := &rowSessionRepo{rows: map[string]Session{}, deleteErr: tc.deleteErr}
			store := NewPersistentSessionStore(time.Hour, fixedNow, repo)
			// Seed only inert state; no lookup or authentication is performed.
			store.sessions[token] = Session{Token: token}
			if tc.stored {
				repo.rows[token] = store.sessions[token]
			}
			repo.beforeDelete = func() {
				if _, ok := store.sessions[token]; ok {
					t.Error("local session still present when persistence was attempted")
				}
			}

			if err := store.Delete(token); err != tc.wantErr { //nolint:errorlint // Delete must return the original repository error unchanged.
				t.Fatalf("Delete = %v, want original error %v", err, tc.wantErr)
			}
			if _, ok := store.sessions[token]; ok {
				t.Error("Delete retained the local session")
			}
			if _, ok := repo.rows[token]; ok != (tc.wantErr != nil) {
				t.Errorf("stored row present = %v, want %v", ok, tc.wantErr != nil)
			}
			if repo.deletes != 1 {
				t.Errorf("persistence attempts = %d, want 1", repo.deletes)
			}
		})
	}
}

func TestSessionStoreDeleteMemoryOnly(t *testing.T) {
	store := NewSessionStore(time.Hour, fixedNow)
	store.sessions["inert-session"] = Session{Token: "inert-session"}
	for range 2 {
		if err := store.Delete("inert-session"); err != nil {
			t.Fatal(err)
		}
		if len(store.sessions) != 0 {
			t.Fatal("Delete retained the in-memory session")
		}
	}
}

func TestHandleLogoutDeletionResult(t *testing.T) {
	storeErr := errors.New("inert repository detail")
	for _, tc := range []struct {
		name             string
		stored           bool
		deleteErr        error
		removedWithError bool
		wantStatus       int
	}{
		{name: "stored", stored: true, wantStatus: http.StatusNoContent},
		{name: "absent", deleteErr: ErrSessionNotFound, wantStatus: http.StatusNoContent},
		{name: "wrapped absent", deleteErr: fmt.Errorf("delete: %w", ErrSessionNotFound), wantStatus: http.StatusNoContent},
		{name: "failure", stored: true, deleteErr: storeErr, wantStatus: http.StatusInternalServerError},
		{name: "uncertain completion", stored: true, deleteErr: storeErr, removedWithError: true, wantStatus: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const token = "inert-session"
			repo := &rowSessionRepo{rows: map[string]Session{}, deleteErr: tc.deleteErr}
			store := NewPersistentSessionStore(time.Hour, fixedNow, repo)
			store.sessions[token] = Session{Token: token}
			if tc.stored {
				repo.rows[token] = store.sessions[token]
			}
			repo.beforeDelete = func() {
				if tc.removedWithError {
					delete(repo.rows, token)
				}
			}
			handler := NewAuthHandler(nil, nil, NewAuthenticator(store, nil))
			req := httptest.NewRequest(http.MethodPost, "/Sessions/Logout", nil)
			req.Header.Set("X-Emby-Token", token)
			rec := httptest.NewRecorder()
			// Invoke only the corrected handler, without an auth request or listener.
			handler.HandleLogout(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if rec.Header().Get("Retry-After") != "" {
				t.Error("logout introduced Retry-After")
			}
			if _, ok := store.sessions[token]; ok {
				t.Error("logout retained the local session")
			}
			wantStored := tc.stored && tc.deleteErr != nil && !tc.removedWithError
			if _, ok := repo.rows[token]; ok != wantStored {
				t.Errorf("stored row present = %v, want %v", ok, wantStored)
			}
			if repo.deletes != 1 {
				t.Errorf("persistence attempts = %d, want 1", repo.deletes)
			}
			if rec.Code == http.StatusNoContent {
				if rec.Body.Len() != 0 {
					t.Error("successful logout returned a body")
				}
				return
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 2 || body["Error"] != "ServerError" || body["Message"] != "Could not sign out" {
				t.Errorf("unexpected public error response: %v", body)
			}
		})
	}
}

func TestHandleLogoutDeletionRetry(t *testing.T) {
	const token = "inert-session"
	repo := &rowSessionRepo{
		rows:      map[string]Session{token: {Token: token}},
		deleteErr: errors.New("inert deletion failure"),
	}
	store := NewPersistentSessionStore(time.Hour, fixedNow, repo)
	store.sessions[token] = repo.rows[token]
	handler := NewAuthHandler(nil, nil, NewAuthenticator(store, nil))
	// A finite direct-handler retry sequence measures deletion state only.
	// Router authentication and real client retry behavior are outside this test.
	for _, wantStatus := range []int{http.StatusInternalServerError, http.StatusNoContent, http.StatusNoContent} {
		req := httptest.NewRequest(http.MethodPost, "/Sessions/Logout", nil)
		req.Header.Set("X-Emby-Token", token)
		rec := httptest.NewRecorder()
		handler.HandleLogout(rec, req)
		if rec.Code != wantStatus {
			t.Fatalf("status = %d, want %d", rec.Code, wantStatus)
		}
		if _, ok := repo.rows[token]; ok != (wantStatus == http.StatusInternalServerError) {
			t.Fatalf("stored row present = %v after status %d", ok, rec.Code)
		}
		repo.deleteErr = nil
	}
	if repo.deletes != 3 {
		t.Errorf("persistence attempts = %d, want 3", repo.deletes)
	}
}
