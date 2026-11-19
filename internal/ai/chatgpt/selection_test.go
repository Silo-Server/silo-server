package chatgpt

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"testing"
	"time"
)

func (m *memoryStore) GetMany(_ context.Context, keys ...string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := m.values[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

func (m *memoryStore) UpdateValueAndSettingsAtomic(ctx context.Context, _ string, update func(map[string]string) (map[string]string, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failNext {
		m.failNext = false
		return errors.New("simulated interrupted commit")
	}
	writes, err := update(maps.Clone(m.values))
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	maps.Copy(m.values, writes)
	return nil
}

func selectionFixture(t *testing.T, values map[string]string) (*Service, *memoryStore) {
	t.Helper()
	store := &memoryStore{values: maps.Clone(values)}
	save(t, store, credentials{Active: "a", Accounts: []account{
		{ClientID: "a", AccessToken: "a-token", RefreshToken: "a-refresh", ExpiresAt: time.Now().Add(time.Hour)},
		{ClientID: "b", AccessToken: "b-token", RefreshToken: "b-refresh", ExpiresAt: time.Now().Add(time.Hour)},
	}})
	return NewService(store), store
}

func TestSelectionFailurePreservesAccountAndModel(t *testing.T) {
	for _, status := range []int{200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s, store := selectionFixture(t, map[string]string{"ai.auth_mode": "chatgpt", "ai.chat_model": "a-model", "ai.reasoning_effort": "high"})
			before := maps.Clone(store.values)
			s.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
				return response(status, map[string]any{"models": []Model{}}), nil
			})
			if err := s.SelectAccount(t.Context(), "b"); err == nil {
				t.Fatal("unusable catalog accepted")
			}
			if !maps.Equal(before, store.values) {
				t.Fatal("failed selection changed persisted settings")
			}
		})
	}
}

func TestModelValidationRejectsConcurrentActivationAndDisconnect(t *testing.T) {
	s, store := selectionFixture(t, map[string]string{"ai.auth_mode": "chatgpt", "ai.chat_model": "a-model"})
	s.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		return response(200, map[string]any{"models": []map[string]string{{"slug": "a-model", "visibility": "list"}}}), nil
	})
	validation, err := s.PrepareModelValidation(t.Context(), map[string]string{"ai.chat_model": "a-model"})
	if err != nil {
		t.Fatal(err)
	}
	before := maps.Clone(store.values)
	if err := validation.Check(before, before); err != nil {
		t.Fatal(err)
	}
	c := stored(t, store)
	c.Active = "b"
	save(t, store, c)
	if err := validation.Check(store.values, store.values); !errors.Is(err, ErrAccountChanged) {
		t.Fatalf("changed account: %v", err)
	}
	c.Active = "a"
	c.Accounts[0].NeedsLogin = true
	save(t, store, c)
	if err := validation.Check(store.values, store.values); !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("disconnected account: %v", err)
	}
}

func TestTextConfigPinsAccountAndModelAcrossLaterSwitch(t *testing.T) {
	s, store := selectionFixture(t, map[string]string{"ai.auth_mode": "chatgpt", "ai.chat_model": "a-model"})
	cfg, err := s.TextConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	s.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		return response(200, map[string]any{"models": []map[string]string{{"slug": "b-model", "visibility": "list"}}}), nil
	})
	if err := s.SelectAccount(t.Context(), "b"); err != nil {
		t.Fatal(err)
	}
	source, err := cfg.ChatGPTTokenSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if token, err := source(t.Context()); err != nil || token != "a-token" || cfg.ChatModel != "a-model" {
		t.Fatalf("original selection=%q,%q,%v", cfg.ChatModel, token, err)
	}
	if store.values["ai.chat_model"] != "b-model" {
		t.Fatal("new account model not persisted")
	}
}

func TestSelectionReceiptUsesCommittedSnapshots(t *testing.T) {
	s, store := selectionFixture(t, map[string]string{"ai.auth_mode": "chatgpt", "ai.chat_model": "a-model", "ai.reasoning_effort": "high", "ai.text_provider_id": "old-provider"})
	s.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		// A competing administrator writes while catalog fetching is outside the lock.
		store.mu.Lock()
		store.values["ai.text_provider_id"] = "before-provider"
		store.mu.Unlock()
		return response(200, map[string]any{"models": []map[string]string{{"slug": "gpt-4o-mini", "visibility": "list"}}}), nil
	})
	s.OnSettingUpdated = func(context.Context, string, string) {
		// A later writer must not become part of the acknowledged receipt.
		store.mu.Lock()
		store.values["ai.text_provider_id"] = "after-provider"
		store.mu.Unlock()
	}
	receipt, err := s.SelectAccountWithSettings(t.Context(), "b")
	if err != nil || receipt == nil {
		t.Fatalf("receipt=%+v, %v", receipt, err)
	}
	if receipt.Before["ai.text_provider_id"] != "before-provider" || receipt.After["ai.text_provider_id"] != "before-provider" || receipt.Before["ai.chat_model"] != "a-model" || receipt.After["ai.chat_model"] != "gpt-4o-mini" || receipt.After["ai.reasoning_effort"] != "" {
		t.Fatal("receipt did not capture transaction-owned values")
	}
	if store.values["ai.text_provider_id"] != "after-provider" {
		t.Fatal("later writer was lost")
	}
	store.failNext = true
	if receipt, err := s.SelectAccountWithSettings(t.Context(), "a"); err == nil || receipt != nil {
		t.Fatalf("failed commit returned receipt=%+v, %v", receipt, err)
	}
}
