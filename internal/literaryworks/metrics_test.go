package literaryworks

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestMatchOriginIsBounded(t *testing.T) {
	for _, origin := range []string{"scanner", "ebook_enrichment", "audiobook_enrichment", "other", "untrusted-value"} {
		want := origin
		if origin == "untrusted-value" {
			want = "other"
		}
		if got := matchOrigin(WithMatchOrigin(context.Background(), origin)); got != want {
			t.Fatalf("origin=%q got=%q want=%q", origin, got, want)
		}
	}
	if got := matchOrigin(context.Background()); got != "other" {
		t.Fatalf("default=%q", got)
	}
}

func TestAutoMatchErrorReleasesActiveGauge(t *testing.T) {
	ctx := WithMatchOrigin(context.Background(), "ebook_enrichment")
	active := autoMatchActive.WithLabelValues("ebook_enrichment")
	failures := autoMatchTotal.WithLabelValues("ebook_enrichment", "error")
	beforeActive, beforeErrors := metricValue(t, active), metricValue(t, failures)
	var service *Service
	if _, _, err := service.AutoLinkContent(ctx, "missing"); !errors.Is(err, ErrWorkNotFound) {
		t.Fatalf("error=%v", err)
	}
	if got := metricValue(t, active); got != beforeActive {
		t.Fatalf("active gauge leaked: %v", got)
	}
	if got := metricValue(t, failures); got != beforeErrors+1 {
		t.Fatalf("error counter=%v", got)
	}
}

func TestAutoMatchAlreadyLinkedSkipsCandidateQuery(t *testing.T) {
	pool := candidateTestPool(t)
	ctx := WithMatchOrigin(context.Background(), "scanner")
	_, err := pool.Exec(ctx, `CREATE TEMP TABLE literary_work_items (content_id text, work_id text, updated_at timestamptz);
 INSERT INTO literary_work_items VALUES ('linked','existing-work',now());`)
	if err != nil {
		t.Fatal(err)
	}
	// The minimal media_items fixture cannot satisfy GetMatchItem's hydration
	// query, so any regression past the linked-item guard would fail this call.
	counter := autoMatchTotal.WithLabelValues("scanner", "already_linked")
	before := metricValue(t, counter)
	workID, linked, err := NewService(NewRepository(pool)).AutoLinkContent(ctx, "linked")
	if err != nil || linked || workID != "existing-work" {
		t.Fatalf("work=%q linked=%v err=%v", workID, linked, err)
	}
	if got := metricValue(t, counter); got != before+1 {
		t.Fatalf("already-linked counter=%v", got)
	}
}

func metricValue(t *testing.T, metric prometheus.Metric) float64 {
	t.Helper()
	var value dto.Metric
	if err := metric.Write(&value); err != nil {
		t.Fatal(err)
	}
	if value.Gauge != nil {
		return value.Gauge.GetValue()
	}
	return value.Counter.GetValue()
}
