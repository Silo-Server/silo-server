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
	storeErr := errors.New("delete failed")
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
			const token = "logout-token"
			repo := &rowSessionRepo{rows: map[string]Session{}, deleteErr: tc.deleteErr}
			store := NewPersistentSessionStore(time.Hour, fixedNow, repo)
			store.sessions[token] = Session{Token: token}
			if tc.stored {
				repo.rows[token] = store.sessions[token]
			}

			if err := store.Delete(token); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Delete = %v, want %v", err, tc.wantErr)
			}
			if _, ok := store.sessions[token]; ok {
				t.Error("Delete kept the cached session")
			}
			if _, ok := repo.rows[token]; ok != (tc.wantErr != nil) {
				t.Errorf("stored row present = %v, want %v", ok, tc.wantErr != nil)
			}
		})
	}
}

func TestHandleLogoutDeletionResult(t *testing.T) {
	for _, tc := range []struct {
		name       string
		deleteErr  error
		wantStatus int
	}{
		{name: "deleted", wantStatus: http.StatusNoContent},
		{name: "already absent", deleteErr: ErrSessionNotFound, wantStatus: http.StatusNoContent},
		{name: "failure", deleteErr: errors.New("connection reset"), wantStatus: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const token = "logout-token"
			repo := &rowSessionRepo{rows: map[string]Session{token: {Token: token}}, deleteErr: tc.deleteErr}
			store := NewPersistentSessionStore(time.Hour, fixedNow, repo)
			handler := NewAuthHandler(nil, nil, NewAuthenticator(store, nil))
			req := httptest.NewRequest(http.MethodPost, "/Sessions/Logout", nil)
			req.Header.Set("X-Emby-Token", token)
			rec := httptest.NewRecorder()
			handler.HandleLogout(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if rec.Code == http.StatusNoContent {
				if rec.Body.Len() != 0 {
					t.Errorf("204 carried a body: %q", rec.Body.String())
				}
				return
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			// The repository's error text must not reach the client.
			if len(body) != 2 || body["Error"] != "ServerError" || body["Message"] != "Could not sign out" {
				t.Errorf("error body = %v", body)
			}
		})
	}
}
