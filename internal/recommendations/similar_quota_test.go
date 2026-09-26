package recommendations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations/embeddings"
)

type quotaTestEmbedder func(context.Context, []string) ([][]float32, error)

func (f quotaTestEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return f(ctx, texts)
}

func TestEmbedBatchStopsOnProviderLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"daily quota", &embeddings.RateLimitError{DailyQuota: true}},
		{"temporary retries exhausted", &embeddings.RateLimitError{}},
		{"wrapped limit", fmt.Errorf("wrapped: %w", &embeddings.RateLimitError{})},
		{"OpenAI quota", errors.New("embedding API returned 429: insufficient_quota")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			e := &Engine{embClient: quotaTestEmbedder(func(context.Context, []string) ([][]float32, error) {
				calls++
				return nil, tc.err
			})}
			items := []*models.MediaItem{{ContentID: "test-one"}, {ContentID: "test-two"}}
			n, err := e.embedBatch(context.Background(), items, []string{"one", "two"}, "test-model")
			if calls != 1 || n != 0 || !errors.Is(err, tc.err) {
				t.Fatalf("batch stop: calls=%d stored=%d error=%v", calls, n, err)
			}
			if strings.Contains(err.Error(), "check billing") {
				t.Fatal("batch added an unsupported billing diagnosis")
			}
		})
	}
}

func TestEmbedBatchPreservesItemFallback(t *testing.T) {
	calls := 0
	e := &Engine{embClient: quotaTestEmbedder(func(context.Context, []string) ([][]float32, error) {
		calls++
		return nil, errors.New("input too large")
	})}
	items := []*models.MediaItem{{ContentID: "test-one"}, {ContentID: "test-two"}}
	n, err := e.embedBatch(context.Background(), items, []string{"one", "two"}, "test-model")
	if err != nil || calls != 3 || n != 0 {
		t.Fatalf("item fallback: calls=%d stored=%d error=%v", calls, n, err)
	}
}

func TestEmbedBatchStopsFallbackOnProviderLimit(t *testing.T) {
	calls := 0
	limitErr := &embeddings.RateLimitError{DailyQuota: true}
	e := &Engine{embClient: quotaTestEmbedder(func(context.Context, []string) ([][]float32, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("batch input too large")
		}
		return nil, limitErr
	})}
	items := []*models.MediaItem{{ContentID: "test-one"}, {ContentID: "test-two"}}
	n, err := e.embedBatch(context.Background(), items, []string{"one", "two"}, "test-model")
	if !errors.Is(err, limitErr) || calls != 2 || n != 0 {
		t.Fatalf("fallback stop: calls=%d stored=%d error=%v", calls, n, err)
	}
}
