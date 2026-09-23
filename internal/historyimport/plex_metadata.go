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
type plexMetadataSweep struct {
	items    map[string]*PlexItem
	firstErr error
}

// fetchMetadataByKey resolves full metadata for each distinct key, in batches.
// Individual failures stay best-effort so other records can still import; only
// context cancellation aborts the sweep.
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

	for start := 0; start < len(pending); start += plexMetadataBatchSize {
		batch := pending[start:min(start+plexMetadataBatchSize, len(pending))]
		metas, err := c.FetchMetadataBatch(ctx, baseURL, token, batch)
		if err == nil {
			for i := range metas {
				sweep.items[metas[i].RatingKey] = &metas[i]
			}
			continue
		}
		if ctx.Err() != nil {
			return plexMetadataSweep{}, ctx.Err()
		}
		noteErr(err, batch)
		if len(batch) == 1 {
			continue
		}
		// Older PMS releases answered 404 for a whole batch when only one key was
		// deleted. Retry that case per key, but do not multiply systematic failures
		// such as authentication errors, outages, or timeouts into one request per item.
		if !isPlexHTTPStatus(err, http.StatusNotFound) {
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
			"failed to fetch series metadata for %d of %d series; their episodes can only match on their own ids (first error: %v)",
			missing, len(keys), sweep.firstErr))
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
