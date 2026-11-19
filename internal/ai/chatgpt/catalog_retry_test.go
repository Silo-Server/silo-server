package chatgpt

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

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
