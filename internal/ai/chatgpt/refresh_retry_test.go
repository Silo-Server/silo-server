package chatgpt

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestRefreshRetriesTemporaryFailureWithinSameRequest(t *testing.T) {
	for _, test := range []struct {
		name           string
		status         int
		code           string
		transportError error
	}{
		{name: "network", transportError: io.ErrUnexpectedEOF},
		{name: "rate limit", status: http.StatusTooManyRequests},
		{name: "internal error", status: http.StatusInternalServerError},
		{name: "unavailable", status: http.StatusServiceUnavailable},
		{name: "temporary OAuth 400", status: http.StatusBadRequest, code: "temporarily_unavailable"},
		{name: "temporary OAuth 401", status: http.StatusUnauthorized, code: "temporarily_unavailable"},
		{name: "OAuth server error", status: http.StatusBadRequest, code: "server_error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := &memoryStore{values: map[string]string{}}
				save(t, store, credentials{Active: "app", Accounts: []account{{ClientID: "app", Subject: "verified", Email: "verified@example.test", IDToken: "verified-login", AccessToken: "expired-access", RefreshToken: "old-refresh", Scopes: planScope, ExpiresAt: time.Now().Add(-time.Minute)}}})
				service := NewService(store)
				var requests []time.Time
				service.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
					requests = append(requests, time.Now())
					if err := r.ParseForm(); err != nil {
						t.Fatal(err)
					}
					if r.Form.Get("grant_type") != oauthRefreshToken || r.Form.Get(oauthClientID) != "app" || r.Form.Get(oauthRefreshToken) != "old-refresh" {
						t.Fatal("refresh retry changed its registration or renewable token")
					}
					if len(requests) == 1 {
						if test.transportError != nil {
							return nil, test.transportError
						}
						return response(test.status, map[string]string{"error": test.code}), nil
					}
					return response(http.StatusOK, tokenResponse{AccessToken: "renewed-access", RefreshToken: "rotated-refresh", IDToken: "unverified-refresh-identity", TokenType: "Bearer", ExpiresIn: 3600}), nil
				})
				token, err := service.BearerToken(t.Context())
				if err != nil || token != "renewed-access" || len(requests) != 2 {
					t.Fatalf("refresh did not recover in the same request: token=%q error=%v calls=%d", token, err, len(requests))
				}
				if wait := requests[1].Sub(requests[0]); wait < time.Second {
					t.Fatalf("refresh retry omitted backoff: %v", wait)
				}
				c := stored(t, store)
				a := c.Accounts[0]
				if c.Active != "app" || a.AccessToken != "renewed-access" || a.RefreshToken != "rotated-refresh" || a.Subject != "verified" || a.Email != "verified@example.test" || a.IDToken != "verified-login" || a.Scopes != planScope || a.NeedsLogin || time.Until(a.ExpiresAt) != time.Hour {
					t.Fatalf("refresh recovery changed identity or failed to persist its token bundle: %+v", a)
				}
			})
		})
	}
}

func TestRefreshRetryExhaustionPreservesCredentials(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &memoryStore{values: map[string]string{}}
		save(t, store, credentials{Active: "app", Accounts: []account{{ClientID: "app", Subject: "verified", IDToken: "verified-login", AccessToken: "expired-access", RefreshToken: "old-refresh", Scopes: planScope, ExpiresAt: time.Now().Add(-time.Minute)}}})
		before := stored(t, store)
		service := NewService(store)
		var requests []time.Time
		service.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
			requests = append(requests, time.Now())
			return response(http.StatusServiceUnavailable, nil), nil
		})
		token, err := service.BearerToken(t.Context())
		if token != "" || !errors.Is(err, ErrProvider) || errors.Is(err, ErrSignInRequired) || len(requests) != 3 {
			t.Fatalf("refresh exhaustion classification or attempt limit: token=%q error=%v calls=%d", token, err, len(requests))
		}
		if first, second := requests[1].Sub(requests[0]), requests[2].Sub(requests[1]); first != time.Second || second != 2*time.Second {
			t.Fatalf("refresh backoff intervals: %v, %v", first, second)
		}
		after := stored(t, store)
		if after.Active != before.Active || after.Accounts[0] != before.Accounts[0] {
			t.Fatal("temporary refresh exhaustion modified saved credentials")
		}
	})
}

func TestRefreshRetryStopsOnTerminalError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &memoryStore{values: map[string]string{}}
		save(t, store, credentials{Active: "app", Accounts: []account{{ClientID: "app", Subject: "verified", IDToken: "verified-login", RefreshToken: "old-refresh", Scopes: planScope}}})
		service := NewService(store)
		calls := 0
		service.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return response(http.StatusServiceUnavailable, nil), nil
			}
			return response(http.StatusBadRequest, map[string]string{"error": oauthInvalidGrant}), nil
		})
		if _, err := service.BearerToken(t.Context()); !errors.Is(err, ErrSignInRequired) || calls != 2 {
			t.Fatalf("terminal error did not stop refresh retry: error=%v calls=%d", err, calls)
		}
		if !stored(t, store).Accounts[0].NeedsLogin {
			t.Fatal("terminal refresh error did not require sign-in")
		}
		if _, err := service.BearerToken(t.Context()); !errors.Is(err, ErrSignInRequired) || calls != 2 {
			t.Fatalf("unusable refresh token was submitted again: error=%v calls=%d", err, calls)
		}
	})
}

func TestRefreshRetryHonorsRetryAfter(t *testing.T) {
	for _, format := range []string{"seconds", "date", "at deadline", "beyond deadline"} {
		t.Run(format, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := &memoryStore{values: map[string]string{}}
				save(t, store, credentials{Active: "app", Accounts: []account{{ClientID: "app", RefreshToken: "old-refresh", Scopes: planScope}}})
				before := stored(t, store).Accounts[0]
				service := NewService(store)
				start := time.Now()
				var requests []time.Time
				service.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
					requests = append(requests, time.Now())
					if len(requests) == 1 {
						r := response(http.StatusTooManyRequests, nil)
						r.Header.Set("Retry-After", "2")
						switch format {
						case "date":
							r.Header.Set("Retry-After", time.Now().Add(2*time.Second).UTC().Format(http.TimeFormat))
						case "at deadline":
							r.Header.Set("Retry-After", "60")
						case "beyond deadline":
							r.Header.Set("Retry-After", "120")
						}
						return r, nil
					}
					return response(http.StatusOK, tokenResponse{AccessToken: "renewed-access", RefreshToken: "rotated-refresh", TokenType: "Bearer", ExpiresIn: 3600}), nil
				})
				token, err := service.BearerToken(t.Context())
				if format == "at deadline" || format == "beyond deadline" {
					if token != "" || !errors.Is(err, ErrProvider) || errors.Is(err, context.DeadlineExceeded) || len(requests) != 1 || time.Since(start) != 0 || stored(t, store).Accounts[0] != before {
						t.Fatalf("unusable Retry-After did not release refresh immediately: token=%q error=%v calls=%d elapsed=%v", token, err, len(requests), time.Since(start))
					}
					return
				}
				if err != nil || token != "renewed-access" || len(requests) != 2 {
					t.Fatalf("rate-limit refresh did not recover: token=%q error=%v calls=%d", token, err, len(requests))
				}
				if wait := requests[1].Sub(requests[0]); wait != 2*time.Second {
					t.Fatalf("refresh ignored Retry-After: %v", wait)
				}
			})
		})
	}
}

func TestRefreshRetriesSerializeRotatingTokenAcrossServices(t *testing.T) {
	store := &memoryStore{values: map[string]string{}}
	save(t, store, credentials{Active: "app", Accounts: []account{{ClientID: "app", Subject: "verified", IDToken: "verified-login", RefreshToken: "old-refresh", Scopes: planScope}}})
	var calls atomic.Int32
	transport := transportFunc(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get(oauthClientID) != "app" || r.Form.Get(oauthRefreshToken) != "old-refresh" {
			t.Error("concurrent refresh reused a rotated token or changed registration")
		}
		if calls.Add(1) == 1 {
			return response(http.StatusServiceUnavailable, nil), nil
		}
		return response(http.StatusOK, tokenResponse{AccessToken: "renewed-access", RefreshToken: "rotated-refresh", TokenType: "Bearer", ExpiresIn: 3600}), nil
	})
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			service := NewService(store)
			service.http.Transport = transport
			token, err := service.BearerToken(t.Context())
			if err != nil || token != "renewed-access" {
				t.Errorf("concurrent refresh failed: token=%q error=%v", token, err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 2 {
		t.Fatalf("concurrent refresh made %d exchanges; expected one failed attempt and one rotation", calls.Load())
	}
	a := stored(t, store).Accounts[0]
	if a.AccessToken != "renewed-access" || a.RefreshToken != "rotated-refresh" || a.IDToken != "verified-login" || a.NeedsLogin {
		t.Fatal("concurrent refresh failed to preserve the replacement token bundle")
	}
}
