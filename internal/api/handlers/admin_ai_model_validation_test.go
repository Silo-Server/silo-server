package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/ai/chatgpt"
)

type aiSelectionTestStore struct {
	*fakeServerSettingsStore
	beforeCommit func()
}

func (s *aiSelectionTestStore) GetMany(_ context.Context, keys ...string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		values[key] = s.values[key]
	}
	return values, nil
}

func (s *aiSelectionTestStore) UpdateValueAtomic(_ context.Context, key string, update func(string) (string, error)) error {
	value, err := update(s.values[key])
	if err == nil {
		s.values[key] = value
	}
	return err
}

func (s *aiSelectionTestStore) UpdateValueAndSettingsAtomic(ctx context.Context, _ string, update func(map[string]string) (map[string]string, error)) error {
	return s.fakeServerSettingsStore.UpdateAtomic(ctx, update)
}

func (s *aiSelectionTestStore) UpdateAtomic(ctx context.Context, update func(map[string]string) (map[string]string, error)) error {
	if s.beforeCommit != nil {
		before := s.beforeCommit
		s.beforeCommit = nil
		before()
	}
	return s.fakeServerSettingsStore.UpdateAtomic(ctx, update)
}

type aiModelTestTransport func(*http.Request) (*http.Response, error)

func (f aiModelTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAdminModelWritesValidateCatalogAndConcurrentAccount(t *testing.T) {
	previousTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	http.DefaultTransport = aiModelTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.openai.com/v1/models" || r.Header.Get("Authorization") != "Bearer a-token" {
			t.Fatalf("unexpected catalog request %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"a-model","visibility":"list"}]}`))}, nil
	})
	for _, test := range []struct {
		name, model      string
		single, switched bool
		wantError        bool
	}{
		{name: "batch unavailable model", model: "other-model", wantError: true},
		{name: "single unavailable model", model: "other-model", single: true, wantError: true},
		{name: "batch supported model", model: "a-model"},
		{name: "activation committed during catalog fetch", model: "a-model", switched: true, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			credentialValues := map[string]any{"active": "a", "accounts": []map[string]any{{"client_id": "a", "access_token": "a-token", "refresh_token": "a-refresh", "expires_at": time.Now().Add(time.Hour)}, {"client_id": "b", "access_token": "b-token", "refresh_token": "b-refresh", "expires_at": time.Now().Add(time.Hour)}}}
			encode := func() string {
				data, err := json.Marshal(credentialValues)
				if err != nil {
					t.Fatal(err)
				}
				return string(data)
			}
			store := &aiSelectionTestStore{fakeServerSettingsStore: &fakeServerSettingsStore{values: map[string]string{"ai.auth_mode": "chatgpt", "ai.chat_model": "a-model", chatgpt.CredentialsKey: encode()}}}
			if test.switched {
				store.beforeCommit = func() {
					credentialValues["active"] = "b"
					store.values[chatgpt.CredentialsKey] = encode()
					store.values["ai.chat_model"] = "b-model"
				}
			}
			before := maps.Clone(store.values)
			h := &AdminHandler{SettingsRepo: store, ChatGPT: chatgpt.NewService(store)}
			var err error
			if test.single {
				_, err = h.UpdateAdminSetting(t.Context(), "ai.chat_model", test.model, nil)
			} else {
				_, err = h.UpdateAdminSettings(t.Context(), map[string]string{"ai.chat_model": test.model}, nil)
			}
			if (err != nil) != test.wantError {
				t.Fatalf("write error=%v, want error=%t", err, test.wantError)
			}
			if test.wantError {
				apiErr, _ := errors.AsType[*APIError](err)
				if apiErr == nil || apiErr.Code != adminInvalidSettingsCode {
					t.Fatalf("validation diagnostic=%v", err)
				}
			}
			if test.switched {
				if store.values["ai.chat_model"] != "b-model" {
					t.Fatal("stale model write replaced concurrent activation")
				}
			} else if test.wantError && !maps.Equal(before, store.values) {
				t.Fatal("failed model validation changed settings")
			}
		})
	}
}
