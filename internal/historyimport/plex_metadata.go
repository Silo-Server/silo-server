package historyimport

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
)

// plexMetadataSweep is the best-effort result of resolving many rating keys.
// Keys that failed or no longer exist are absent from items; firstErr names the
// first upstream failure so a systematic cause shows up in the run summary.
// aborted reports that the sweep gave up before reading every key.
type plexMetadataSweep struct {
	items    map[string]*PlexItem
	firstErr error
	aborted  bool
}

// plexMetadataFailureStreakLimit ends a sweep after this many consecutive batch
// failures that are not the deleted-key 404 case. A PMS that rejects or times out
// one batch answers the next one the same way, so a large sweep would otherwise
// spend one 30s client timeout per batch — over an hour for a few thousand keys —
// and resolve nothing, all while the run's heartbeat keeps it out of the stale sweep.
const plexMetadataFailureStreakLimit = 3

// fetchMetadataByKey resolves full metadata for each distinct key, in batches.
// Individual failures stay best-effort so other records can still import; context
// cancellation aborts the sweep, and so does a run of systematic batch failures.
func (c *PlexClient) fetchMetadataByKey(ctx context.Context, baseURL, token string, keys []string) (plexMetadataSweep, error) {
	sweep := plexMetadataSweep{items: make(map[string]*PlexItem, len(keys))}
	pending := uniqueNonEmpty(keys)
	noteErr := func(err error, keys []string) {
		slog.WarnContext(ctx, "plex history import: failed to fetch item metadata",
			"component", "historyimport", "rating_keys", keys, "error", err)
		if sweep.firstErr == nil {
			sweep.firstErr = err
		}
	}

	failureStreak := 0
	for start := 0; start < len(pending); start += plexMetadataBatchSize {
		batch := pending[start:min(start+plexMetadataBatchSize, len(pending))]
		metas, err := c.FetchMetadataBatch(ctx, baseURL, token, batch)
		if err == nil {
			failureStreak = 0
			for i := range metas {
				sweep.items[metas[i].RatingKey] = &metas[i]
			}
			continue
		}
		if ctx.Err() != nil {
			return plexMetadataSweep{}, ctx.Err()
		}
		noteErr(err, batch)
		// Older PMS releases answered 404 for a whole batch when only one key was
		// deleted. Retry that case per key, but do not multiply systematic failures
		// such as authentication errors, outages, or timeouts into one request per item.
		if !isPlexHTTPStatus(err, http.StatusNotFound) {
			failureStreak++
			if failureStreak >= plexMetadataFailureStreakLimit {
				// Only an early stop that left keys unasked is worth reporting as
				// one; the same streak ending on the last batch skipped nothing.
				sweep.aborted = start+plexMetadataBatchSize < len(pending)
				if sweep.aborted {
					slog.WarnContext(ctx, "plex history import: giving up on item metadata after repeated failures",
						"component", "historyimport", "consecutive_failures", failureStreak,
						"resolved", len(sweep.items), "requested", len(pending), "error", err)
				}
				return sweep, nil
			}
			continue
		}
		// A 404 means the server answered, so it breaks the run of failures even
		// though the keys it named are gone.
		failureStreak = 0
		if len(batch) == 1 {
			continue
		}
		for _, key := range batch {
			meta, err := c.FetchMetadata(ctx, baseURL, token, key)
			if err != nil {
				if ctx.Err() != nil {
					return plexMetadataSweep{}, ctx.Err()
				}
				noteErr(err, []string{key})
				continue
			}
			if meta != nil {
				sweep.items[key] = meta
			}
		}
	}
	return sweep, nil
}

// fetchPlexSeriesMetadata resolves the shows episodes belong to, keyed by
// grandparent rating key, so episodes can match on series ids plus
// season/episode numbers when they carry no usable ids of their own.
func fetchPlexSeriesMetadata(ctx context.Context, client *PlexClient, baseURL, token string, seriesKeys []string, warnings *[]string) (map[string]*PlexItem, error) {
	keys := uniqueNonEmpty(seriesKeys)
	sweep, err := client.fetchMetadataByKey(ctx, baseURL, token, keys)
	if err != nil {
		return nil, err
	}
	if missing := len(keys) - len(sweep.items); missing > 0 && sweep.firstErr != nil {
		*warnings = append(*warnings, fmt.Sprintf(
			"failed to fetch series metadata for %d of %d series; their episodes can only match on their own ids%s (first error: %v)",
			missing, len(keys), plexSweepAbortedSuffix(sweep.aborted), sweep.firstErr))
	}
	return sweep.items, nil
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
