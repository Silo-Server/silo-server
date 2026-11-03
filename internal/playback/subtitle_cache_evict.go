package playback

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// removeStaleSiblings deletes committed entries for the same source+track
// with a different mtime/size suffix — the source was replaced, so those can
// never be served again.
func (c *SubtitleCache) removeStaleSiblings(dir, inputPath string, trackIndex int, keepKey string) {
	prefix := subtitleCacheKeyPrefix(inputPath, trackIndex)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if name == keepKey || !strings.HasPrefix(name, prefix) || filepath.Ext(name) != filepath.Ext(keepKey) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			slog.Warn("subtitle cache stale entry remove failed", "name", name, "error", err)
		}
	}
}

// evict is the scan-on-write LRU pass: when committed entries exceed the
// byte budget, the oldest-mtime entries are removed until the total fits.
// It also sweeps orphaned .part temp files older than stalePartMaxAge
// (crash leftovers). No background daemon — commits are rare enough that a
// directory scan per commit is cheap.
func (c *SubtitleCache) evict(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type cacheEnt struct {
		path  string
		size  int64
		mtime time.Time
	}
	var (
		ents  []cacheEnt
		total int64
	)
	now := time.Now()
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if strings.Contains(e.Name(), ".part-") {
			if now.Sub(info.ModTime()) > stalePartMaxAge {
				_ = os.Remove(path)
			}
			continue
		}
		if !slices.Contains([]string{SubtitleExtPGSV3, SubtitleExtVTTV3, SubtitleExtASSV3, SubtitleExtSRTV3}, filepath.Ext(e.Name())) {
			continue
		}
		ents = append(ents, cacheEnt{path: path, size: info.Size(), mtime: info.ModTime()})
		total += info.Size()
	}
	if total <= c.maxBytes {
		return
	}
	slices.SortFunc(ents, func(a, b cacheEnt) int { return a.mtime.Compare(b.mtime) })
	for _, e := range ents {
		if total <= c.maxBytes {
			break
		}
		if err := os.Remove(e.path); err != nil {
			if !os.IsNotExist(err) {
				slog.Warn("subtitle cache eviction remove failed", "path", e.path, "error", err)
			}
			continue
		}
		slog.Info("evicted cached subtitle track (LRU)", "path", e.path, "bytes", e.size)
		total -= e.size
	}
}
