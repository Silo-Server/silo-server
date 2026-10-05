package catalog

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/recommendations/embeddings"
)

// funcVectorizer embeds queries with embed and counts the calls.
type funcVectorizer struct {
	mu    sync.Mutex
	calls int
	embed func(ctx context.Context, query string) ([]float32, error)
}

func (f *funcVectorizer) EmbedSearchQuery(ctx context.Context, query string) ([]float32, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.embed(ctx, query)
}

func (f *funcVectorizer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeClock is a settable clock for the query-vector cache.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func semanticTestProvider(vectorizer CatalogSearchQueryVectorizer, clock *fakeClock) *MeilisearchSearchProvider {
	return &MeilisearchSearchProvider{
		config: MeilisearchProviderConfig{
			MatchingStrategy: DefaultMeilisearchMatchingStrategy,
			SemanticEnabled:  true,
			SemanticRatio:    DefaultMeilisearchSemanticRatio,
			Embedder:         DefaultMeilisearchEmbedder,
			Vectorizer:       vectorizer,
		},
		vectorClock: clock.Now,
	}
}

// The provider gets a short deadline: a hung embedding call cannot hold a
// search for the batch client's minutes of retries.
func TestQueryVectorEmbeddingHasADeadline(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	var remaining time.Duration
	vectorizer := &funcVectorizer{embed: func(ctx context.Context, _ string) ([]float32, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			return nil, errors.New("no deadline")
		}
		remaining = time.Until(deadline)
		return []float32{0.5, 0.25}, nil
	}}
	provider := semanticTestProvider(vectorizer, clock)

	req, fallback := provider.buildMeilisearchSearchRequest(t.Context(), CatalogSearchRequest{Query: "quiet space horror"})
	if fallback != "" || req.Hybrid == nil {
		t.Fatalf("request = %#v, fallback %q; want a hybrid request", req, fallback)
	}
	if remaining <= 0 || remaining > meilisearchQueryVectorTimeout {
		t.Fatalf("embedding deadline in %v, want at most %v", remaining, meilisearchQueryVectorTimeout)
	}
}

// A provider that never answers costs one search the deadline, not more.
func TestQueryVectorTimeoutFallsBackToKeyword(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	vectorizer := &funcVectorizer{embed: func(ctx context.Context, _ string) ([]float32, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	provider := semanticTestProvider(vectorizer, clock)

	started := time.Now()
	req, fallback := provider.buildMeilisearchSearchRequest(t.Context(), CatalogSearchRequest{Query: "quiet space horror"})
	if elapsed := time.Since(started); elapsed > meilisearchQueryVectorTimeout+time.Second {
		t.Fatalf("search waited %v for the embedding provider", elapsed)
	}
	if fallback != "semantic query embedding failed" || req.Hybrid != nil || req.Vector != nil {
		t.Fatalf("request = %#v, fallback %q; want keyword with the fixed reason", req, fallback)
	}
}

// After a failure the node skips the provider for the cooldown, with the same
// fallback reason, then tries again.
func TestQueryVectorFailureCoolsDown(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	fail := true
	vectorizer := &funcVectorizer{embed: func(context.Context, string) ([]float32, error) {
		if fail {
			return nil, errors.New(`Post "http://ollama:11434/v1/embeddings": connection refused`)
		}
		return []float32{0.5, 0.25}, nil
	}}
	provider := semanticTestProvider(vectorizer, clock)
	search := func(query string) (meilisearchSearchRequest, string) {
		t.Helper()
		return provider.buildMeilisearchSearchRequest(t.Context(), CatalogSearchRequest{Query: query})
	}

	if _, fallback := search("quiet space horror"); fallback != "semantic query embedding failed" || vectorizer.callCount() != 1 {
		t.Fatalf("first failure: fallback %q, calls %d", fallback, vectorizer.callCount())
	}
	fail = false
	clock.advance(meilisearchQueryVectorFailureCooldown - time.Second)
	if req, fallback := search("slow burn detective"); fallback != "semantic query embedding failed" || req.Hybrid != nil || vectorizer.callCount() != 1 {
		t.Fatalf("during the cooldown: fallback %q, hybrid %v, calls %d; want keyword without a provider call", fallback, req.Hybrid != nil, vectorizer.callCount())
	}
	clock.advance(time.Second)
	if req, fallback := search("slow burn detective"); fallback != "" || req.Hybrid == nil || vectorizer.callCount() != 2 {
		t.Fatalf("after the cooldown: fallback %q, hybrid %v, calls %d; want a hybrid request", fallback, req.Hybrid != nil, vectorizer.callCount())
	}
	// A cached vector is served even during a later cooldown.
	fail = true
	if _, fallback := search("quiet space horror"); fallback == "" {
		t.Fatal("an uncached query embedded while the provider fails")
	}
	if req, fallback := search("slow burn detective"); fallback != "" || req.Hybrid == nil || vectorizer.callCount() != 3 {
		t.Fatalf("cached query during a cooldown: fallback %q, calls %d", fallback, vectorizer.callCount())
	}
}

// A caller that gives up does not put the provider in a cooldown.
func TestQueryVectorCanceledSearchDoesNotCoolDown(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	vectorizer := &funcVectorizer{embed: func(ctx context.Context, _ string) ([]float32, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return []float32{0.5, 0.25}, nil
	}}
	provider := semanticTestProvider(vectorizer, clock)

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, fallback := provider.buildMeilisearchSearchRequest(canceled, CatalogSearchRequest{Query: "quiet space horror"}); fallback == "" {
		t.Fatal("a canceled search embedded its query")
	}
	if req, fallback := provider.buildMeilisearchSearchRequest(t.Context(), CatalogSearchRequest{Query: "quiet space horror"}); fallback != "" || req.Hybrid == nil {
		t.Fatalf("next search after a canceled one: fallback %q", fallback)
	}
	if vectorizer.callCount() != 2 {
		t.Fatalf("provider calls = %d, want 2", vectorizer.callCount())
	}
}

// A query the provider refuses, such as an overlong search, says nothing
// about the provider, so the next search still embeds its query. That holds
// for a 4xx refusal and for the context-length 5xx a local model answers.
func TestQueryVectorRefusedQueryDoesNotCoolDown(t *testing.T) {
	for name, refusal := range map[string]error{
		"rejected request": &embeddings.StatusError{API: "embedding", StatusCode: 400},
		"context length":   &embeddings.StatusError{API: "embedding", StatusCode: 500, Body: "the input length exceeds the context length"},
	} {
		clock := &fakeClock{now: time.Unix(1_000_000, 0)}
		vectorizer := &funcVectorizer{embed: func(_ context.Context, query string) ([]float32, error) {
			if query == "an overlong refused search" {
				return nil, refusal
			}
			return []float32{0.5, 0.25}, nil
		}}
		provider := semanticTestProvider(vectorizer, clock)

		if _, fallback := provider.buildMeilisearchSearchRequest(t.Context(), CatalogSearchRequest{Query: "an overlong refused search"}); fallback == "" {
			t.Fatalf("%s: a refused query was searched semantically", name)
		}
		if req, fallback := provider.buildMeilisearchSearchRequest(t.Context(), CatalogSearchRequest{Query: "quiet space horror"}); fallback != "" || req.Hybrid == nil {
			t.Fatalf("%s: next search after a refused query: fallback %q", name, fallback)
		}
	}
}
