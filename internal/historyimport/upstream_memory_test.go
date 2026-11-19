package historyimport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
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

// The run budget spans every request of a run, including lookups that have
// no listing budget of their own.
func TestRunBudgetCoversLookups(t *testing.T) {
	t.Parallel()

	ctx := trustLoopback(context.Background())
	server := serveBody(t, []byte(`{"Items":[{"Id":"1","Type":"Series","ProviderIds":{"Tvdb":"1"}}]}`))
	auth := jellyfinLocalAuth{BaseURL: server.URL, UserID: "u", AccessToken: "t"}

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
