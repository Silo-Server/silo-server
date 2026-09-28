package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type artworkProbeFunc func(context.Context) error

func (f artworkProbeFunc) Probe(ctx context.Context) error { return f(ctx) }

func TestArtworkReadinessSurvivesRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls := 0
	h := NewReadyHandler(nil, nil, artworkProbeFunc(func(ctx context.Context) error {
		calls++
		if ctx.Err() != nil {
			t.Fatalf("probe inherited canceled request: %v", ctx.Err())
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Fatal("shared probe must have a bounded lifetime")
		}
		return nil
	}))
	if !h.checkArtwork(ctx) || !h.checkArtwork(t.Context()) || calls != 1 {
		t.Fatalf("healthy probe was not cached: calls=%d healthy=%v", calls, h.artworkOK)
	}
}

func TestArtworkReadinessRecoversAfterFailedProbeExpires(t *testing.T) {
	err := errors.New("storage unavailable")
	calls := 0
	h := NewReadyHandler(nil, nil, artworkProbeFunc(func(context.Context) error {
		calls++
		return err
	}))
	if h.checkArtwork(t.Context()) {
		t.Fatal("unavailable storage reported ready")
	}
	err = nil
	if h.checkArtwork(t.Context()) || calls != 1 {
		t.Fatal("failure should remain cached until the next probe")
	}
	h.artworkChecked = time.Now().Add(-31 * time.Second)
	if !h.checkArtwork(t.Context()) || calls != 2 {
		t.Fatal("storage recovery was not observed")
	}
}

func TestReadinessReportsStorageWithoutGatingOnIt(t *testing.T) {
	failing := artworkProbeFunc(func(context.Context) error { return errors.New("storage unavailable") })
	h := NewReadyHandler(pingerFunc(func(context.Context) error { return nil }), nil, failing)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("artwork outage removed the node from service: %d %s", rec.Code, rec.Body.String())
	}
	var body readyStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "degraded" || body.Artwork == nil || *body.Artwork || body.S3 == nil || !*body.S3 || body.Postgres == nil || !*body.Postgres {
		t.Fatalf("degraded storage not reported with the frozen failure shape: %+v", body)
	}

	down := NewReadyHandler(pingerFunc(func(context.Context) error { return errors.New("down") }), nil, nil)
	rec = httptest.NewRecorder()
	down.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("database outage left the node ready: %d", rec.Code)
	}
}

type pingerFunc func(context.Context) error

func (f pingerFunc) Ping(ctx context.Context) error { return f(ctx) }
