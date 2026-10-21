package historyimport

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func metadataKeys(count int) []string {
	keys := make([]string, 0, count)
	for i := range count {
		keys = append(keys, strconv.Itoa(i+1))
	}
	return keys
}

// requestedKeys returns the rating keys one /library/metadata/{a,b,c} path asked for.
func requestedKeys(path string) []string {
	return strings.Split(strings.TrimPrefix(path, "/library/metadata/"), ",")
}

// sweepMetadata runs fetchMetadataByKey for keys against a server using handler.
func sweepMetadata(t *testing.T, keys []string, handler http.HandlerFunc) plexMetadataSweep {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := &PlexClient{httpClient: server.Client()}
	sweep, err := client.fetchMetadataByKey(trustLoopback(t.Context()), server.URL, "token", keys)
	if err != nil {
		t.Fatalf("fetchMetadataByKey: %v", err)
	}
	return sweep
}

// A server that fails every batch the same way will not start answering on the
// next one, so the sweep must stop instead of spending one client timeout per
// batch across thousands of keys.
func TestFetchMetadataByKeyStopsAfterSystematicFailures(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	keys := metadataKeys(plexMetadataBatchSize * 10)
	sweep := sweepMetadata(t, keys, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	if !sweep.aborted {
		t.Error("sweep.aborted = false, want true")
	}
	if sweep.firstErr == nil {
		t.Error("sweep.firstErr = nil, want the first upstream failure")
	}
	if len(sweep.items) != 0 {
		t.Errorf("sweep.items = %d, want 0", len(sweep.items))
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != plexMetadataFailureStreakLimit {
		t.Errorf("requests = %d, want %d — the sweep asked past the failure streak",
			requests, plexMetadataFailureStreakLimit)
	}
}

// A 404 batch is the deleted-key case, not a failing server: it still fans out to
// one request per key, and any number of them in a row must not end the sweep.
func TestFetchMetadataByKeyKeepsSweepingPastDeletedKeys(t *testing.T) {
	keys := metadataKeys(plexMetadataBatchSize * (plexMetadataFailureStreakLimit + 1))
	sweep := sweepMetadata(t, keys, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys := requestedKeys(r.URL.Path)
		if len(keys) > 1 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"MediaContainer":{"Metadata":[{"ratingKey":%q,"type":"movie","title":"Movie"}]}}`, keys[0])
	}))
	if sweep.aborted {
		t.Error("sweep.aborted = true, want false — 404 batches are not a failing server")
	}
	if len(sweep.items) != len(keys) {
		t.Errorf("sweep.items = %d, want %d", len(sweep.items), len(keys))
	}
}

// A blip that the next batch recovers from resets the streak, so one bad batch
// early in a long sweep never costs the rest of it.
func TestFetchMetadataByKeyResumesAfterRecoveredFailure(t *testing.T) {
	var mu sync.Mutex
	failed := 0
	batches := 6
	keys := metadataKeys(plexMetadataBatchSize * batches)
	sweep := sweepMetadata(t, keys, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys := requestedKeys(r.URL.Path)
		mu.Lock()
		// Fail every other batch: the streak never reaches the limit.
		shouldFail := failed%2 == 0
		failed++
		mu.Unlock()
		if shouldFail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, fmt.Sprintf(`{"ratingKey":%q,"type":"movie","title":"Movie"}`, key))
		}
		_, _ = fmt.Fprintf(w, `{"MediaContainer":{"Metadata":[%s]}}`, strings.Join(parts, ","))
	}))
	if sweep.aborted {
		t.Error("sweep.aborted = true, want false — a recovered failure must reset the streak")
	}
	if want := len(keys) / 2; len(sweep.items) != want {
		t.Errorf("sweep.items = %d, want %d", len(sweep.items), want)
	}
}

// A 404 batch means the server answered, so it breaks the run of failures. A
// server alternating 500 and 404 is reachable and resolving keys per retry, and
// must not be given up on.
func TestFetchMetadataByKeyTreats404AsAnAnswer(t *testing.T) {
	var mu sync.Mutex
	batches := 0
	keys := metadataKeys(plexMetadataBatchSize * 8)
	sweep := sweepMetadata(t, keys, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys := requestedKeys(r.URL.Path)
		if len(keys) == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"MediaContainer":{"Metadata":[{"ratingKey":%q,"type":"movie","title":"Movie"}]}}`, keys[0])
			return
		}
		mu.Lock()
		even := batches%2 == 0
		batches++
		mu.Unlock()
		if even {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	if sweep.aborted {
		t.Error("sweep.aborted = true, want false — a 404 batch is not a consecutive failure")
	}
	// Every 404 batch resolves its keys one at a time; the 500 batches resolve none.
	if want := len(keys) / 2; len(sweep.items) != want {
		t.Errorf("sweep.items = %d, want %d", len(sweep.items), want)
	}
}

// Giving up on the final batch left no key unasked, so the run must not claim the
// lookup stopped early.
func TestFetchMetadataByKeyDoesNotReportAnEarlyStopOnTheLastBatch(t *testing.T) {
	keys := metadataKeys(plexMetadataBatchSize * plexMetadataFailureStreakLimit)
	sweep := sweepMetadata(t, keys, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	if sweep.aborted {
		t.Error("sweep.aborted = true, want false — every key was attempted")
	}
	if sweep.firstErr == nil {
		t.Error("sweep.firstErr = nil, want the failures still reported")
	}
}
