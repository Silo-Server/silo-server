package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

// fixedWaitLimiter refuses every request with a fixed wait.
type fixedWaitLimiter struct{ wait time.Duration }

func (l fixedWaitLimiter) Allow(context.Context, string, ratelimit.Rate) ratelimit.AllowResult {
	return ratelimit.AllowResult{Allowed: false, RetryAfter: l.wait}
}

func (fixedWaitLimiter) Close() {}

// A client that waits the advertised Retry-After must find the limiter ready,
// so a fractional wait rounds up.
func TestRefreshLimitsRoundRetryAfterUp(t *testing.T) {
	limiter := fixedWaitLimiter{wait: 5900 * time.Millisecond}

	people := &PeopleHandler{refreshQueue: &recordingPersonRefreshQueue{}, refreshLimiter: limiter}
	err := people.RefreshPerson(context.Background(), 7, 1, catalog.AccessFilter{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests {
		t.Fatalf("RefreshPerson error = %v, want 429", err)
	}
	if apiErr.RetryAfter != 6 {
		t.Fatalf("person refresh retry after = %d, want 6", apiErr.RetryAfter)
	}

	items := newTrailerRefreshHandler(
		&fakeTrailerItemAccess{
			items:     map[string]*models.MediaItem{"movie-1": {ContentID: "movie-1", Type: "movie"}},
			ensureErr: map[string]error{},
		},
		&fakeTrailerRefreshRequester{outcome: metadata.TrailerRefreshOutcome{Status: metadata.TrailerRefreshStatusQueued}},
	)
	items.SetTrailerRefreshLimiter(limiter)
	_, err = items.RequestTrailersRefresh(context.Background(), 7, "movie-1", func() (catalog.AccessFilter, error) {
		return catalog.AccessFilter{}, nil
	})
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests {
		t.Fatalf("RequestTrailersRefresh error = %v, want 429", err)
	}
	if apiErr.RetryAfter != 6 {
		t.Fatalf("trailer refresh retry after = %d, want 6", apiErr.RetryAfter)
	}
}
