package chatgpt

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestModelCatalogRetriesTemporaryFailures(t *testing.T) {
	for _, test := range []struct {
		name                                  string
		status                                int
		transportError, readError, persistent bool
	}{
		{name: "transport", transportError: true},
		{name: "interrupted body", readError: true},
		{name: "rate limit", status: http.StatusTooManyRequests},
		{name: "server error", status: http.StatusServiceUnavailable},
		{name: "exhausted retries", status: http.StatusServiceUnavailable, persistent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				service, store := selectionFixture(t, map[string]string{authModeSetting: authModeChatGPT, chatModelSetting: "b-model"})
				before := stored(t, store)
				var requests []time.Time
				service.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
					requests = append(requests, time.Now())
					if r.URL.String() != service.modelsURL || r.Header.Get("Authorization") != "Bearer b-token" {
						t.Fatal("catalog retry changed account or made a token exchange")
					}
					if len(requests) == 1 || test.persistent {
						if test.transportError {
							return nil, io.ErrUnexpectedEOF
						}
						if test.readError {
							r := response(http.StatusOK, nil)
							r.Body = io.NopCloser(strings.NewReader(`{"models":[`))
							return r, nil
						}
						return response(test.status, nil), nil
					}
					return response(http.StatusOK, map[string]any{"models": []map[string]string{{"slug": "b-model", "visibility": visibilityListed}}}), nil
				})
				_, err := service.SelectAccountWithSettings(t.Context(), "b")
				wantCalls := 2
				if test.persistent {
					wantCalls = 3
				}
				if len(requests) != wantCalls || (err != nil) != test.persistent {
					t.Fatalf("catalog recovery: error=%v calls=%d", err, len(requests))
				}
				for i := 1; i < len(requests); i++ {
					if requests[i].Sub(requests[i-1]) != time.Second<<(i-1) {
						t.Fatal("catalog retry omitted bounded backoff")
					}
				}
				after := stored(t, store)
				if after.Accounts[0] != before.Accounts[0] || after.Accounts[1] != before.Accounts[1] || (test.persistent && after.Active != before.Active) {
					t.Fatal("catalog retry modified credentials or failed selection")
				}
				if test.persistent && !errors.Is(err, ErrProvider) {
					t.Fatal("temporary exhaustion lost provider classification")
				}
			})
		})
	}
}

func TestModelCatalogRetryHonorsDeadlineAndCancellation(t *testing.T) {
	for _, test := range []string{"retry after", "deadline", "canceled", "provider budget"} {
		t.Run(test, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				service := NewService(&memoryStore{values: map[string]string{}})
				ctx := t.Context()
				cancel := func() {}
				if test != "provider budget" {
					ctx, cancel = context.WithTimeout(ctx, 3*time.Second)
				}
				defer cancel()
				calls := 0
				start := time.Now()
				service.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
					calls++
					if calls > 1 {
						return response(http.StatusOK, map[string]any{"models": []map[string]string{{"slug": "model", "visibility": visibilityListed}}}), nil
					}
					r := response(http.StatusTooManyRequests, nil)
					r.Header.Set("Retry-After", "2")
					if test == "deadline" {
						r.Header.Set("Retry-After", "3")
					}
					if test == "provider budget" {
						r.Header.Set("Retry-After", "86400")
					}
					if test == "canceled" {
						cancel()
					}
					return r, nil
				})
				models, err := service.modelCatalog(ctx, "token")
				if test == "retry after" {
					if err != nil || len(models) != 1 || calls != 2 || time.Since(start) != 2*time.Second {
						t.Fatalf("Retry-After ignored: models=%v error=%v calls=%d elapsed=%v", models, err, calls, time.Since(start))
					}
				} else if !errors.Is(err, ErrProvider) || calls != 1 || time.Since(start) != 0 {
					t.Fatalf("impossible retry did not stop: error=%v calls=%d elapsed=%v", err, calls, time.Since(start))
				}
			})
		})
	}
}
