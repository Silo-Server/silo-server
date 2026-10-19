package historyimport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// expandingBody returns a JSON body just under the response byte limit made
// of prefix, repeated elements, and suffix. Tiny elements such as "{}" decode
// into one struct each, so the body is small but its decoded form is not.
func expandingBody(prefix, element, suffix string) []byte {
	var body strings.Builder
	body.WriteString(prefix)
	budget := int(maxUpstreamResponseBytes) - len(prefix) - len(suffix) - len(element)
	for i := 0; body.Len() < budget; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		body.WriteString(element)
	}
	body.WriteString(suffix)
	return []byte(body.String())
}

func serveBody(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server
}

// allocatedBytes reports the bytes fn allocates, including any goroutines
// running meanwhile; the cases below differ from the old behavior by two
// orders of magnitude, so that noise does not matter.
func allocatedBytes(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A body within the byte limit must not decode into millions of values: the
// response fails before the decoder allocates for its contents. Not parallel,
// because it measures allocations.
func TestHistoryImportClientsBoundDecodedValues(t *testing.T) {
	ctx := trustLoopback(context.Background())
	cases := []struct {
		name  string
		body  []byte
		fetch func(baseURL string) error
	}{
		{
			// The case measured in #2382: 16 MiB of empty Jellyfin items.
			name: "jellyfin empty items",
			body: expandingBody(`{"Items":[`, `{}`, `]}`),
			fetch: func(baseURL string) error {
				auth := jellyfinLocalAuth{BaseURL: baseURL, UserID: "u", AccessToken: "t"}
				_, err := newUnthrottledJellyfinClient().FetchItems(ctx, auth, "IsPlayed", jellyfinPlayableItemTypes)
				return err
			},
		},
		{
			// Mismatched elements still grow the slice before the type error.
			name: "emby scalar items",
			body: expandingBody(`{"Items":[`, `0`, `]}`),
			fetch: func(baseURL string) error {
				client := NewEmbyClient()
				client.limiter = nil
				_, err := client.FetchItems(ctx, embyLocalAuth{BaseURL: baseURL, UserID: "u", AccessToken: "t"}, "IsPlayed")
				return err
			},
		},
		{
			// One item passes the listing item cap; its nested Guid array
			// carries the expansion.
			name: "plex nested guids",
			body: expandingBody(`{"MediaContainer":{"totalSize":1,"Metadata":[{"ratingKey":"1","Guid":[`, `{}`, `]}]}}`),
			fetch: func(baseURL string) error {
				_, err := newUnthrottledPlexClient().FetchUserHistory(ctx, baseURL, "t", "1")
				return err
			},
		},
		{
			name: "jellyfin provider ids",
			body: expandingBody(`{"Items":[{"Id":"1","ProviderIds":{`, `"":""`, `}}]}`),
			fetch: func(baseURL string) error {
				auth := jellyfinLocalAuth{BaseURL: baseURL, UserID: "u", AccessToken: "t"}
				_, err := newUnthrottledJellyfinClient().FetchItemsByIDs(ctx, auth, []string{"1"}, "Series")
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := serveBody(t, tc.body)
			var err error
			allocated := allocatedBytes(func() { err = tc.fetch(server.URL) })
			if !errors.Is(err, ErrUpstreamResponseTooLarge) {
				t.Fatalf("err = %v, want ErrUpstreamResponseTooLarge", err)
			}
			if allocated > 64<<20 {
				t.Fatalf("decoding a %d-byte body allocated %d MiB", len(tc.body), allocated>>20)
			}
		})
	}
}

// A real Plex metadata batch, with full cast and stream lists, stays well
// under the value limit.
func TestPlexMetadataBatchWithCastListsDecodes(t *testing.T) {
	t.Parallel()

	var body strings.Builder
	body.WriteString(`{"MediaContainer":{"size":20,"Metadata":[`)
	keys := make([]string, plexMetadataBatchSize)
	for i := range plexMetadataBatchSize {
		keys[i] = strconv.Itoa(i + 1)
		if i > 0 {
			body.WriteByte(',')
		}
		body.WriteString(`{"ratingKey":"` + keys[i] + `","type":"movie","title":"Movie","Guid":[{"id":"imdb://tt1"},{"id":"tmdb://1"}],"Role":[`)
		for r := range 300 {
			if r > 0 {
				body.WriteByte(',')
			}
			body.WriteString(`{"id":1,"filter":"actor=1","tag":"Actor","tagKey":"k","role":"Role","thumb":"t"}`)
		}
		body.WriteString(`],"Media":[{"id":1,"Part":[{"id":1,"Stream":[`)
		for s := range 40 {
			if s > 0 {
				body.WriteByte(',')
			}
			body.WriteString(`{"id":1,"streamType":3,"codec":"srt","index":1,"bitrate":1,"language":"English","languageTag":"en","languageCode":"eng","displayTitle":"English","extendedDisplayTitle":"English (SRT)","title":"t","selected":false,"forced":false,"key":"/k","format":"srt","default":false}`)
		}
		body.WriteString(`]}]}]}`)
	}
	body.WriteString(`]}}`)
	server := serveBody(t, []byte(body.String()))

	items, err := newUnthrottledPlexClient().FetchMetadataBatch(trustLoopback(context.Background()), server.URL, "t", keys)
	if err != nil {
		t.Fatalf("FetchMetadataBatch: %v", err)
	}
	if len(items) != plexMetadataBatchSize {
		t.Fatalf("items = %d, want %d", len(items), plexMetadataBatchSize)
	}
}

// Lookups by id keep one item per requested id and drop everything else the
// source sends back.
func TestHistoryImportLookupsKeepOnlyRequestedIDs(t *testing.T) {
	t.Parallel()

	ctx := trustLoopback(context.Background())
	items := func(format string, requested, extra int) string {
		var parts []string
		for i := range requested {
			entry := strings.ReplaceAll(format, "ID", "req-"+strconv.Itoa(i))
			parts = append(parts, entry, entry)
		}
		for i := range extra {
			parts = append(parts, strings.ReplaceAll(format, "ID", "extra-"+strconv.Itoa(i)))
		}
		return strings.Join(parts, ",")
	}
	ids := []string{"req-0", "req-1", "req-2"}

	t.Run("jellyfin", func(t *testing.T) {
		t.Parallel()
		server := serveBody(t, []byte(`{"Items":[`+items(`{"Id":"ID","Type":"Series"}`, len(ids), 1000)+`]}`))
		got, err := newUnthrottledJellyfinClient().FetchItemsByIDs(ctx, jellyfinLocalAuth{BaseURL: server.URL, UserID: "u", AccessToken: "t"}, ids, "Series")
		if err != nil || len(got) != len(ids) {
			t.Fatalf("items = %d, err = %v, want %d", len(got), err, len(ids))
		}
	})
	t.Run("emby", func(t *testing.T) {
		t.Parallel()
		server := serveBody(t, []byte(`{"Items":[`+items(`{"Id":"ID","Type":"Series"}`, len(ids), 1000)+`]}`))
		client := NewEmbyClient()
		client.limiter = nil
		got, err := client.FetchItemsByIDs(ctx, embyLocalAuth{BaseURL: server.URL, UserID: "u", AccessToken: "t"}, ids, "Series")
		if err != nil || len(got) != len(ids) {
			t.Fatalf("items = %d, err = %v, want %d", len(got), err, len(ids))
		}
	})
	t.Run("plex", func(t *testing.T) {
		t.Parallel()
		server := serveBody(t, []byte(`{"MediaContainer":{"Metadata":[`+items(`{"ratingKey":"ID","type":"show"}`, len(ids), 1000)+`]}}`))
		got, err := newUnthrottledPlexClient().FetchMetadataBatch(ctx, server.URL, "t", ids)
		if err != nil || len(got) != len(ids) {
			t.Fatalf("items = %d, err = %v, want %d", len(got), err, len(ids))
		}
	})
}

// The run budget spans every request of a run, including lookups that have
// no listing budget of their own.
func TestRunBudgetCoversLookups(t *testing.T) {
	t.Parallel()

	ctx := trustLoopback(context.Background())
	server := serveBody(t, []byte(`{"Items":[{"Id":"1","Type":"Series","ProviderIds":{"Tvdb":"1"}}]}`))
	auth := jellyfinLocalAuth{BaseURL: server.URL, UserID: "u", AccessToken: "t"}

	t.Run("retained items", func(t *testing.T) {
		t.Parallel()
		runCtx := withRunBudget(ctx)
		runBudgetFrom(runCtx).retained = maxRunRetainedBytes - 10
		_, err := newUnthrottledJellyfinClient().FetchItemsByIDs(runCtx, auth, []string{"1"}, "Series")
		if !errors.Is(err, ErrUpstreamResponseTooLarge) || !strings.Contains(err.Error(), "import keeps") {
			t.Fatalf("err = %v, want the run retained-items limit", err)
		}
	})
	t.Run("bytes", func(t *testing.T) {
		t.Parallel()
		runCtx := withRunBudget(ctx)
		runBudgetFrom(runCtx).bytes = maxRunBytes - 10
		_, err := newUnthrottledJellyfinClient().FetchItemsByIDs(runCtx, auth, []string{"1"}, "Series")
		if !errors.Is(err, ErrUpstreamResponseTooLarge) || !strings.Contains(err.Error(), "import exceeds") {
			t.Fatalf("err = %v, want the run byte limit", err)
		}
	})
}

// Every Plex section draws on the same run budget instead of a fresh one.
func TestPlexSectionsShareRunBudget(t *testing.T) {
	t.Parallel()

	const sections = 4
	page := `{"MediaContainer":{"totalSize":1,"Metadata":[{"ratingKey":"1","type":"movie","title":"Movie","viewCount":1,"Guid":[{"id":"imdb://tt1"}]}]}}`
	var directory []string
	for i := range sections {
		directory = append(directory, `{"key":"`+strconv.Itoa(i)+`","type":"movie","title":"Movies"}`)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/library/sections" {
			_, _ = w.Write([]byte(`{"MediaContainer":{"Directory":[` + strings.Join(directory, ",") + `]}}`))
			return
		}
		_, _ = w.Write([]byte(page))
	}))
	t.Cleanup(server.Close)

	runCtx := withRunBudget(trustLoopback(context.Background()))
	if _, _, err := NewPlexServerProvider(newUnthrottledPlexClient(), []string{server.URL}, "t").Fetch(runCtx); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	item := PlexItem{RatingKey: "1", Type: "movie", Title: "Movie", ViewCount: 1, Guid: PlexGuids{{ID: "imdb://tt1"}}}
	// Watched and in-progress listings of every section each keep one item.
	if got, want := runBudgetFrom(runCtx).retained, 2*sections*item.retainedSize(); got != want {
		t.Fatalf("retained = %d, want %d across all sections", got, want)
	}
	if got := runBudgetFrom(runCtx).bytes; got < int64(2*sections*len(page)) {
		t.Fatalf("bytes = %d, want every section listing counted", got)
	}
}
