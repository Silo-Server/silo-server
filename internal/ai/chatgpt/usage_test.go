package chatgpt

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"testing/synctest"

	"github.com/Silo-Server/silo-server/internal/ai/llm"
)

func TestUsagePauseBlocksReadinessAndPinnedRequestsUntilExplicitCheck(t *testing.T) {
	s, store := selectionFixture(t, map[string]string{"ai.auth_mode": "chatgpt", "ai.chat_model": "gpt-6-luna"})
	cfg, err := s.TextConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ChatGPTUsageLimited == nil || cfg.ChatGPTUsageVerified != nil {
		t.Fatal("ordinary requests must pause usage without resuming it")
	}
	source, err := cfg.ChatGPTTokenSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := cfg.ChatGPTUsageLimited(ctx, "a-token"); err != nil {
		t.Fatal(err)
	}
	state, err := s.Status(t.Context())
	if err != nil || state.ActiveClientID != "a" || state.Accounts[0].Connected || !state.Accounts[0].UsageLimited || state.Accounts[0].NeedsLogin || !state.Accounts[1].Connected {
		t.Fatalf("pause status: %+v, %v", state, err)
	}
	if a := stored(t, store).Accounts[0]; a.RefreshToken != "a-refresh" || a.AccessToken != "a-token" {
		t.Fatal("usage pause discarded credentials")
	}
	for name, check := range map[string]func() error{
		"readiness":      func() error { _, err := s.TextConfig(t.Context()); return err },
		"fresh source":   func() error { _, err := s.TokenSource(t.Context()); return err },
		"pinned source":  func() error { _, err := source(t.Context()); return err },
		"ordinary token": func() error { _, err := s.BearerToken(t.Context()); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := check(); !errors.Is(err, ErrUsageLimited) {
				t.Fatalf("pause ignored: %v", err)
			}
		})
	}
	checkCfg, err := s.CheckTextConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	check, err := checkCfg.ChatGPTTokenSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if token, err := check(t.Context()); err != nil || token != "a-token" {
		t.Fatalf("explicit check cannot recheck usage: %q, %v", token, err)
	}
	if _, err := source(t.Context()); !errors.Is(err, ErrUsageLimited) {
		t.Fatal("fetching a check token cleared pause")
	}
	if err := checkCfg.ChatGPTUsageVerified(t.Context(), "a-token"); err != nil {
		t.Fatal(err)
	}
	if token, err := source(t.Context()); err != nil || token != "a-token" {
		t.Fatalf("explicit success did not resume: %q, %v", token, err)
	}
	if _, err := s.TextConfig(t.Context()); err != nil {
		t.Fatalf("readiness did not resume: %v", err)
	}
	if err := s.RejectUsageToken(t.Context(), "a-token"); err != nil {
		t.Fatal(err)
	}
	store.values[authModeSetting] = "api_key"
	store.values["ai.base_url"] = "https://api.example.test"
	apiConfig, err := s.TextConfig(t.Context())
	if err != nil || !apiConfig.TextConfigured() || apiConfig.ChatGPTUsageLimited != nil || apiConfig.ChatGPTUsageVerified != nil || apiConfig.ChatGPTTokenSource != nil {
		t.Fatalf("pause affected API-key provider: %+v, %v", apiConfig, err)
	}
}

func TestUsagePauseAllowsCatalogAndSelectionWithoutResuming(t *testing.T) {
	s, store := selectionFixture(t, map[string]string{"ai.auth_mode": "chatgpt", "ai.chat_model": "gpt-6-luna"})
	if err := s.RejectUsageToken(t.Context(), "b-token"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != s.modelsURL || r.Header.Get("Authorization") != "Bearer b-token" {
			t.Fatal("catalog used different registration")
		}
		return response(http.StatusOK, map[string]any{"models": []map[string]string{{"slug": "gpt-6-luna", "visibility": "list"}}}), nil
	})
	if err := s.SelectAccount(t.Context(), "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Models(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	validation, err := s.PrepareModelValidation(t.Context(), map[string]string{"ai.reasoning_effort": "high"})
	if err != nil {
		t.Fatal(err)
	}
	if err := validation.Check(store.values, store.values); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || stored(t, store).Active != "b" || !stored(t, store).Accounts[1].UsageLimited {
		t.Fatal("catalog or selection cleared pause")
	}
	if _, err := s.TextConfig(t.Context()); !errors.Is(err, ErrUsageLimited) {
		t.Fatalf("selection enabled inference: %v", err)
	}
}

func TestUsageCallbacksPreserveOtherAccountsAndReplacementTokens(t *testing.T) {
	for _, limited := range []bool{true, false} {
		for _, rotated := range []bool{true, false} {
			name := "pause"
			if !limited {
				name = "resume"
			}
			if rotated {
				name += " rotated"
			} else {
				name += " switched"
			}
			t.Run(name, func(t *testing.T) {
				s, store := selectionFixture(t, map[string]string{"ai.auth_mode": "chatgpt", "ai.chat_model": "gpt-6-luna"})
				if !limited {
					if err := s.RejectUsageToken(t.Context(), "a-token"); err != nil {
						t.Fatal(err)
					}
				}
				cfg, err := s.CheckTextConfig(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				c := stored(t, store)
				c.Active = "b"
				c.Accounts[0].UsageLimited, c.Accounts[1].UsageLimited = !limited, !limited
				if rotated {
					c.Accounts[0].AccessToken = "replacement"
				}
				save(t, store, c)
				callback := cfg.ChatGPTUsageVerified
				if limited {
					callback = cfg.ChatGPTUsageLimited
				}
				if err := callback(t.Context(), "a-token"); err != nil {
					t.Fatal(err)
				}
				after := stored(t, store)
				if after.Active != "b" || after.Accounts[1] != c.Accounts[1] || after.Accounts[0].UsageLimited != (limited != rotated) {
					t.Fatal("late callback changed replacement token or another account")
				}
			})
		}
	}
}

func TestUsageCheckCannotBypassLoginEligibilityOrDisconnect(t *testing.T) {
	for _, reason := range []string{"login", "plan", "disconnect"} {
		t.Run(reason, func(t *testing.T) {
			s, store := selectionFixture(t, map[string]string{"ai.auth_mode": llm.AuthModeChatGPT})
			if err := s.RejectUsageToken(t.Context(), "a-token"); err != nil {
				t.Fatal(err)
			}
			checkCfg, err := s.CheckTextConfig(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			check, err := checkCfg.ChatGPTTokenSource(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			c := stored(t, store)
			want := ErrSignInRequired
			switch reason {
			case "login":
				c.Accounts[0].NeedsLogin = true
			case "plan":
				c.Accounts[0].PlanIneligible = true
				want = ErrPlanIneligible
			case "disconnect":
				c.Accounts[0].NeedsLogin = true
				c.Accounts[0].AccessToken = ""
				c.Active = ""
			}
			save(t, store, c)
			if _, err := check(t.Context()); !errors.Is(err, want) {
				t.Fatalf("pinned check bypassed %s: %v", reason, err)
			}
			if _, err := s.CheckTextConfig(t.Context()); !errors.Is(err, want) {
				t.Fatalf("fresh check bypassed %s: %v", reason, err)
			}
			if reason == "disconnect" {
				if err := checkCfg.ChatGPTUsageVerified(t.Context(), "a-token"); err != nil {
					t.Fatal(err)
				}
				if after := stored(t, store); !after.Accounts[0].UsageLimited || !after.Accounts[0].NeedsLogin || after.Active != "" {
					t.Fatal("late success resurrected disconnect")
				}
			}
		})
	}
}

func TestDelayedUsageCheckCannotClearNewerPause(t *testing.T) {
	for _, alreadyPaused := range []bool{false, true} {
		t.Run(map[bool]string{false: "first pause", true: "renewed pause"}[alreadyPaused], func(t *testing.T) {
			s, store := selectionFixture(t, map[string]string{"ai.auth_mode": "api_key"})
			if alreadyPaused {
				if err := s.RejectUsageToken(t.Context(), "a-token"); err != nil {
					t.Fatal(err)
				}
			}
			oldCheck, err := s.CheckTextConfig(t.Context())
			if err != nil || oldCheck.AuthMode != llm.AuthModeChatGPT {
				t.Fatalf("draft check=%+v, err=%v", oldCheck, err)
			}
			oldID := stored(t, store).Accounts[0].UsagePauseID
			if err := s.RejectUsageToken(t.Context(), "a-token"); err != nil {
				t.Fatal(err)
			}
			newerID := stored(t, store).Accounts[0].UsagePauseID
			if newerID == "" || newerID == oldID {
				t.Fatal("pause ID was not rotated")
			}
			if err := oldCheck.ChatGPTUsageVerified(t.Context(), "a-token"); err != nil {
				t.Fatal(err)
			}
			if a := stored(t, store).Accounts[0]; !a.UsageLimited || a.UsagePauseID != newerID {
				t.Fatal("old success cleared newer pause")
			}
			freshCheck, err := s.CheckTextConfig(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err := freshCheck.ChatGPTUsageVerified(t.Context(), "a-token"); err != nil {
				t.Fatal(err)
			}
			if a := stored(t, store).Accounts[0]; a.UsageLimited || a.UsagePauseID != "" {
				t.Fatal("current successful check failed to clear pause")
			}
		})
	}
}

func TestPausedRetryDoesNotReplaceProviderPause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, store := selectionFixture(t, map[string]string{"ai.auth_mode": "chatgpt", "ai.chat_model": "gpt-6-luna"})
		cfg, err := s.TextConfig(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		previous := http.DefaultTransport
		t.Cleanup(func() { http.DefaultTransport = previous })
		calls := 0
		var check llm.Config
		var pauseID string
		http.DefaultTransport = transportFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if calls != 1 || r.Header.Get("Authorization") != "Bearer a-token" {
				t.Fatal("paused retry contacted provider")
			}
			// Another request receives a real provider limit before this request's retry.
			if err := s.RejectUsageToken(t.Context(), "a-token"); err != nil {
				t.Fatal(err)
			}
			pauseID = stored(t, store).Accounts[0].UsagePauseID
			check, err = s.CheckTextConfig(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			return response(http.StatusServiceUnavailable, map[string]any{"error": "temporary failure"}), nil
		})
		out, err := llm.NewClient(cfg).Chat(t.Context(), []llm.Message{{Role: "user", Content: "check"}}, false)
		if out != "" || !errors.Is(err, ErrUsageLimited) || errors.Is(err, llm.ErrQuotaExhausted) || calls != 1 {
			t.Fatalf("local admission: output=%q, err=%v, calls=%d", out, err, calls)
		}
		if stored(t, store).Accounts[0].UsagePauseID != pauseID {
			t.Fatal("local admission rotated provider pause")
		}
		if err := check.ChatGPTUsageVerified(t.Context(), "a-token"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.TextConfig(t.Context()); err != nil {
			t.Fatalf("local admission invalidated successful explicit check: %v", err)
		}
	})
}
