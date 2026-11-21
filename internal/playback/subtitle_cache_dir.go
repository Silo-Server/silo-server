package playback

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/Silo-Server/silo-server/internal/privatedir"
)

// openDir opens the cache dir for one operation, or returns nil when caching
// is disabled or the dir is refused. It is checked on every call because the
// transcode dir can change at runtime. The cache dir and the transcode dir
// above it must each be a real directory owned by this process's user and
// not writable by its group or other users, so no other local account can
// plant entries in the cache dir or rename it aside. ffmpeg opens entries and
// temp files by path, which only that second check keeps inside the cache
// dir. Entries reached through the returned root cannot resolve outside it.
// The caller closes it.
func (c *SubtitleCache) openDir() *os.Root {
	dir := c.dir()
	if dir == "" {
		return nil
	}
	var root *os.Root
	err := privatedir.Ensure(filepath.Dir(dir))
	if err == nil {
		root, err = privatedir.OpenRoot(dir)
	}
	refusal := ""
	if err != nil {
		refusal = err.Error()
	}
	c.mu.Lock()
	repeated := refusal == c.refusal
	c.refusal = refusal
	c.mu.Unlock()
	if err != nil {
		if !repeated {
			slog.Warn("subtitle cache disabled: cache dir refused", "dir", dir, "error", err)
		}
		return nil
	}
	return root
}

// openEntry opens a committed entry through the cache dir's root, so a
// symlink cannot resolve outside it, and accepts only a regular file. A hit
// bumps the entry's mtime to record recency for LRU eviction.
func openEntry(root *os.Root, key string) (*os.File, bool) {
	f, err := root.Open(key)
	if err != nil {
		return nil, false
	}
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, false
	}
	// Best-effort: a failure (e.g. read-only remount) only degrades eviction
	// ordering, not correctness.
	now := time.Now()
	if err := root.Chtimes(key, now, now); err != nil {
		slog.Debug("subtitle cache recency bump failed", "path", f.Name(), "error", err)
	}
	return f, true
}
