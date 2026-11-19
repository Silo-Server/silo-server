package ratingsources

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Sources a metadata plugin such as MDBList would declare.
var (
	rtCritic   = models.RatingSourceDefinition{Source: "rt_critic", Name: "RT", Label: "Rotten Tomatoes critics", Scale: 100, Percent: true}
	rtAudience = models.RatingSourceDefinition{Source: "rt_audience", Name: "RT Audience", Label: "Rotten Tomatoes audience", Scale: 100, Percent: true}
	metacritic = models.RatingSourceDefinition{Source: "metacritic", Name: "Metacritic", Label: "Metacritic", Scale: 100}
	letterboxd = models.RatingSourceDefinition{Source: "letterboxd", Name: "Letterboxd", Label: "Letterboxd", Scale: 5}
	kinopoisk  = models.RatingSourceDefinition{Source: "kinopoisk", Name: "Kinopoisk", Label: "Kinopoisk", Scale: 10}
)

func declared(definitions ...models.RatingSourceDefinition) []models.RatingSourceDefinition {
	return definitions
}

type stubSettings struct {
	value string
	err   error
	reads int
}

func (s *stubSettings) Get(context.Context, string) (string, error) {
	s.reads++
	return s.value, s.err
}

func declaring(definitions ...models.RatingSourceDefinition) DeclaredFunc {
	return func(context.Context) ([]DeclaredSource, error) {
		out := make([]DeclaredSource, 0, len(definitions))
		for _, definition := range definitions {
			out = append(out, DeclaredSource{RatingSourceDefinition: definition, Provider: "MDBList"})
		}
		return out, nil
	}
}

func TestPolicyCachesAndKeepsLastGoodSelection(t *testing.T) {
	settings := &stubSettings{value: "rt_critic"}
	now := time.Unix(0, 0)
	p := NewPolicy(settings, declaring(rtCritic, rtAudience))
	p.now = func() time.Time { return now }
	ctx := context.Background()

	if !p.Selection(ctx).Shows("rt_critic") || p.Selection(ctx).Shows("rt_audience") {
		t.Fatal("selection does not follow the setting")
	}
	if settings.reads != 1 {
		t.Fatalf("reads = %d, want one read served from cache", settings.reads)
	}

	now = now.Add(cacheTTL)
	settings.err = errors.New("database down")
	if !p.Selection(ctx).Shows("rt_critic") {
		t.Fatal("a failed read dropped the administrator's selection")
	}

	settings.err = nil
	settings.value = ""
	if p.Selection(ctx).Shows("rt_critic") {
		t.Fatal("a failed read was cached; the next read did not retry")
	}

	failing := NewPolicy(&stubSettings{err: errors.New("database down")}, declaring(rtCritic))
	if sel := failing.Selection(ctx); !sel.Shows("imdb") || sel.Shows("rt_critic") {
		t.Fatal("a failed first read must answer with the default")
	}
}

// A failed read of the plugin declarations keeps the declarations last read,
// so a turned-on source does not blink off because one lookup failed.
func TestPolicyKeepsLastDeclarationsWhenARereadFails(t *testing.T) {
	now := time.Unix(0, 0)
	fail := false
	p := NewPolicy(&stubSettings{value: "rt_critic"}, func(ctx context.Context) ([]DeclaredSource, error) {
		if fail {
			return nil, errors.New("pool exhausted")
		}
		return declaring(rtCritic)(ctx)
	})
	p.now = func() time.Time { return now }
	if !p.Selection(context.Background()).Shows("rt_critic") {
		t.Fatal("rt_critic is declared and turned on")
	}
	now = now.Add(cacheTTL)
	fail = true
	if !p.Selection(context.Background()).Shows("rt_critic") {
		t.Fatal("a failed declaration read dropped a source the administrator turned on")
	}
}

// A failed read of the plugin declarations before any succeeded is not
// cached, so one failed lookup does not hide every plugin rating until the
// cache expires.
func TestPolicyRetriesDeclarationsNeverRead(t *testing.T) {
	fail := true
	reads := 0
	p := NewPolicy(&stubSettings{value: "rt_critic"}, func(ctx context.Context) ([]DeclaredSource, error) {
		reads++
		if fail {
			return nil, errors.New("pool exhausted")
		}
		return declaring(rtCritic)(ctx)
	})
	now := time.Unix(0, 0)
	p.now = func() time.Time { return now }
	if p.Selection(context.Background()).Shows("rt_critic") {
		t.Fatal("rt_critic shows without a declaration read")
	}
	fail = false
	if !p.Selection(context.Background()).Shows("rt_critic") {
		t.Fatal("the failed first read was cached; the next call did not retry")
	}
	p.Selection(context.Background())
	if reads != 2 {
		t.Fatalf("declaration reads = %d, want 2: the successful read is cached", reads)
	}
}

// The cache is shared by every request on the node, so a request that goes
// away mid-read must not fail the read for the others.
func TestPolicyReadIgnoresTheCallersCancellation(t *testing.T) {
	p := NewPolicy(&stubSettings{value: "rt_critic"}, func(ctx context.Context) ([]DeclaredSource, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return declaring(rtCritic)(ctx)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !p.Selection(ctx).Shows("rt_critic") {
		t.Fatal("a canceled request context failed the shared read")
	}
}
