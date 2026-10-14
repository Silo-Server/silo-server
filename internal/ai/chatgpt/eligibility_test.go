package chatgpt

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/ai/llm"
)

func TestPlanEligibilityRestrictionBlocksFreshAndPinnedRequests(t *testing.T) {
	s, store := selectionFixture(t, map[string]string{"ai.auth_mode": "chatgpt", "ai.chat_model": "gpt-6-luna"})
	cfg, err := s.TextConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	source, err := cfg.ChatGPTTokenSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	validation := &ModelValidation{clientID: "a", models: []Model{{ID: "gpt-6-luna"}}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := cfg.ChatGPTPlanRejected(ctx, "a-token"); err != nil {
		t.Fatal(err)
	}
	status, err := s.Status(t.Context())
	if err != nil || status.ActiveClientID != "a" || status.Accounts[0].Connected || status.Accounts[0].NeedsLogin || !status.Accounts[0].PlanIneligible || !status.Accounts[1].Connected {
		t.Fatalf("restriction status: %+v, %v", status, err)
	}
	if a := stored(t, store).Accounts[0]; a.RefreshToken != "a-refresh" || a.AccessToken != "a-token" {
		t.Fatal("restriction discarded disconnect credentials")
	}
	s.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("restricted account called provider")
		return nil, nil
	})
	for name, check := range map[string]func() error{
		"enqueue readiness": func() error { _, err := s.TextConfig(t.Context()); return err },
		"fresh source":      func() error { _, err := s.TokenSource(t.Context()); return err },
		"pinned source":     func() error { _, err := source(t.Context()); return err },
		"catalog":           func() error { _, err := s.Models(t.Context(), "a"); return err },
		"selection":         func() error { return s.SelectAccount(t.Context(), "a") },
		"model commit":      func() error { return validation.Check(store.values, store.values) },
		"reauthorization":   func() error { _, err := s.StartLogin(t.Context(), "a"); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := check(); !errors.Is(err, ErrPlanIneligible) || errors.Is(err, ErrSignInRequired) {
				t.Fatalf("eligibility restriction lost: %v", err)
			}
		})
	}
	store.mu.Lock()
	store.values[authModeSetting] = "api_key"
	store.values["ai.base_url"] = "https://api.example.test"
	store.mu.Unlock()
	apiConfig, err := s.TextConfig(t.Context())
	if err != nil || !apiConfig.TextConfigured() || apiConfig.ChatGPTPlanRejected != nil || apiConfig.ChatGPTTokenSource != nil {
		t.Fatalf("restriction affected API-key configuration: %+v, %v", apiConfig, err)
	}
}

func TestPlanEligibilityRejectionPreservesOtherAccountsAndRotatedTokens(t *testing.T) {
	for _, rotated := range []bool{false, true} {
		t.Run(map[bool]string{false: "account switch", true: "token rotation"}[rotated], func(t *testing.T) {
			s, store := selectionFixture(t, map[string]string{"ai.auth_mode": "chatgpt", "ai.chat_model": "gpt-6-luna"})
			cfg, err := s.TextConfig(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			c := stored(t, store)
			c.Active = "b"
			if rotated {
				c.Accounts[0].AccessToken = "a-new-token"
			}
			save(t, store, c)
			if err := cfg.ChatGPTPlanRejected(t.Context(), "a-token"); err != nil {
				t.Fatal(err)
			}
			c = stored(t, store)
			if c.Active != "b" || c.Accounts[1].PlanIneligible || c.Accounts[0].PlanIneligible == rotated {
				t.Fatal("late eligibility rejection changed another account or replacement token")
			}
			if cfg, err := s.TextConfig(t.Context()); err != nil || cfg.AuthMode != llm.AuthModeChatGPT {
				t.Fatalf("active account disabled: %v", err)
			}
		})
	}
}
