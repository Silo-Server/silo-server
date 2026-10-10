package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata/translation"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

type guardItemAccess struct{}

func (guardItemAccess) GetByID(_ context.Context, contentID string) (*models.MediaItem, error) {
	return &models.MediaItem{ContentID: contentID, Overview: "An overview."}, nil
}

func (guardItemAccess) EnsureAccessible(context.Context, string, catalog.AccessFilter) error {
	return nil
}

type guardPending struct{ language string }

func (p guardPending) PendingTranslationLanguage(context.Context, *models.MediaItem, catalog.AccessFilter) string {
	return p.language
}

func (p guardPending) PendingSeasonTranslationLanguage(context.Context, *models.Season, catalog.AccessFilter) string {
	return p.language
}

func (p guardPending) PendingEpisodeTranslationLanguage(context.Context, *models.Episode, catalog.AccessFilter) string {
	return p.language
}

type denyLimiter struct{}

func (denyLimiter) Allow(context.Context, string, ratelimit.Rate) ratelimit.AllowResult {
	return ratelimit.AllowResult{Allowed: false}
}
func (denyLimiter) Close() {}

// newGuardHandler wires a handler whose service is not configured, so a
// request that passes every guard ends in the service's not_configured answer.
func newGuardHandler(pending string, limiter ratelimit.RateLimiter) *MetadataAIHandler {
	svc := translation.NewService(context.Background(), translation.Config{}, nil, nil, nil, nil, nil, nil)
	h := NewMetadataAIHandler(svc)
	h.ItemAccess = guardItemAccess{}
	h.Pending = guardPending{language: pending}
	h.Limiter = limiter
	return h
}

func TestTranslateOnViewOnlyQueuesTheViewersPendingLanguage(t *testing.T) {
	userID := 7
	cases := []struct {
		name       string
		pending    string
		requested  string
		limiter    ratelimit.RateLimiter
		wantStatus int
		wantField  string
	}{
		{name: "nothing missing", pending: "", requested: "de", wantStatus: http.StatusBadRequest, wantField: "target_language"},
		{name: "another language", pending: "de", requested: "ja", wantStatus: http.StatusBadRequest, wantField: "target_language"},
		{name: "rate limited", pending: "de", requested: "de", limiter: denyLimiter{}, wantStatus: http.StatusTooManyRequests},
		{name: "matching language reaches the service", pending: "de", requested: "DE", wantStatus: http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newGuardHandler(tc.pending, tc.limiter)
			_, err := h.TranslateOnView(context.Background(), catalog.AccessFilter{}, "item1", tc.requested, &userID)
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v, want *APIError", err)
			}
			if apiErr.Status != tc.wantStatus || apiErr.Field != tc.wantField {
				t.Fatalf("got status %d field %q (%s), want %d field %q", apiErr.Status, apiErr.Field, apiErr.Message, tc.wantStatus, tc.wantField)
			}
		})
	}
}

// libraryPending reports German only for a page scoped to library 7, as a
// detail page does for a profile without a metadata language.
type libraryPending struct{}

func (libraryPending) PendingTranslationLanguage(_ context.Context, _ *models.MediaItem, filter catalog.AccessFilter) string {
	if filter.PresentationLibraryID != nil && *filter.PresentationLibraryID == 7 {
		return "de"
	}
	return ""
}

func (libraryPending) PendingSeasonTranslationLanguage(context.Context, *models.Season, catalog.AccessFilter) string {
	return ""
}

func (libraryPending) PendingEpisodeTranslationLanguage(context.Context, *models.Episode, catalog.AccessFilter) string {
	return ""
}

type itemLibraries []int

func (l itemLibraries) LibraryIDsForItem(context.Context, string) ([]int, error) { return l, nil }

func TestTranslateOnViewAcceptsTheLanguageOfAViewerLibrary(t *testing.T) {
	userID := 7
	h := newGuardHandler("", nil)
	h.Pending = libraryPending{}
	h.ItemLibraries = itemLibraries{3, 7}

	// The library-scoped page asked for German: the check reaches the service.
	_, err := h.TranslateOnView(context.Background(), catalog.AccessFilter{}, "item1", "de", &userID)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("library language: err = %v, want the service's not_configured answer", err)
	}
	// A library the viewer cannot see does not count.
	_, err = h.TranslateOnView(context.Background(), catalog.AccessFilter{DisabledLibraryIDs: []int{7}}, "item1", "de", &userID)
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("hidden library: err = %v, want 400", err)
	}
	_, err = h.TranslateOnView(context.Background(), catalog.AccessFilter{AllowedLibraryIDs: []int{3}}, "item1", "de", &userID)
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("library outside the allowlist: err = %v, want 400", err)
	}
	// Other languages are still refused.
	_, err = h.TranslateOnView(context.Background(), catalog.AccessFilter{}, "item1", "ja", &userID)
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("other language: err = %v, want 400", err)
	}
}

// failOpenLimiter is a shared limiter whose store is unreachable: it allows
// everything and says so with Remaining -1.
type failOpenLimiter struct{}

func (failOpenLimiter) Allow(context.Context, string, ratelimit.Rate) ratelimit.AllowResult {
	return ratelimit.AllowResult{Allowed: true, Remaining: -1}
}
func (failOpenLimiter) Close() {}

func TestTranslateOnViewKeepsABudgetWhileTheSharedLimiterFailsOpen(t *testing.T) {
	userID := 7
	h := newGuardHandler("de", failOpenLimiter{})
	var limited int
	for i := 0; i < 15; i++ {
		_, err := h.TranslateOnView(context.Background(), catalog.AccessFilter{}, "item1", "de", &userID)
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("no request was limited while the shared limiter failed open")
	}
}

// A refused request spends budget before the pending-language lookup runs.
func TestTranslateOnViewChecksTheBudgetBeforeTheLanguage(t *testing.T) {
	userID := 7
	h := newGuardHandler("de", denyLimiter{})
	_, err := h.TranslateOnView(context.Background(), catalog.AccessFilter{}, "item1", "ja", &userID)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests {
		t.Fatalf("err = %v, want 429 before the language check", err)
	}
}

type waitLimiter struct{ wait time.Duration }

func (l waitLimiter) Allow(context.Context, string, ratelimit.Rate) ratelimit.AllowResult {
	return ratelimit.AllowResult{Allowed: false, RetryAfter: l.wait}
}
func (waitLimiter) Close() {}

// Retry-After rounds up, so a client that waits the advertised time is let in.
func TestTranslateOnViewRoundsRetryAfterUp(t *testing.T) {
	userID := 7
	h := newGuardHandler("de", waitLimiter{wait: 1500 * time.Millisecond})
	_, err := h.TranslateOnView(context.Background(), catalog.AccessFilter{}, "item1", "de", &userID)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.RetryAfter != 2 {
		t.Fatalf("err = %#v, want a 429 with Retry-After 2", err)
	}
}
