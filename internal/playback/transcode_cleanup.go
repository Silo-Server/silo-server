package playback

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

var orphanCleanupMu sync.Mutex

// CleanupOrphanedTranscodeDirs removes per-session transcode directories that
// are not associated with any currently active session IDs.
//
// minAge spares a dir whose most recent modification is younger than the given
// age even when it is absent from activeSessionIDs. Under token-carried
// reconstruction there is no durable card index, so an in-memory miss does not
// prove a session is dead — a client holding a still-valid token may yet
// reconstruct it. Sparing dirs younger than the maximum token lifetime closes
// that race; once a dir is older than any surviving token, it is safe to reap.
// Pass 0 to disable age-sparing (e.g. a dedicated node's boot-time full wipe,
// where node restart is an accepted session loss).
func CleanupOrphanedTranscodeDirs(root string, activeSessionIDs map[string]struct{}, minAge time.Duration) (int, error) {
	// Serialize concurrent orphan sweeps of the shared transcode root so two
	// rare startup sweeps cannot race on os.RemoveAll; one global mutex is fine.
	orphanCleanupMu.Lock()
	defer orphanCleanupMu.Unlock()

	if root == "" {
		return 0, nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read transcode dir %q: %w", root, err)
	}

	removed := 0
	cutoff := time.Now().Add(-minAge)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		if transcodeDirBelongsToActiveSession(entry.Name(), activeSessionIDs) {
			continue
		}

		// The subtitle cache is not session state; it manages its own eviction.
		if entry.Name() == subtitleCacheDirName {
			continue
		}

		dir := filepath.Join(root, entry.Name())
		if minAge > 0 {
			info, statErr := entry.Info()
			if statErr != nil {
				// Can't verify age: conservatively retain rather than risk
				// reaping a dir a surviving token could still reconstruct.
				continue
			}
			if info.ModTime().After(cutoff) {
				// Recently active: a surviving token could still reconstruct it.
				continue
			}
		}
		if err := os.RemoveAll(dir); err != nil {
			return removed, fmt.Errorf("remove orphaned transcode dir %q: %w", dir, err)
		}
		removed++
	}

	return removed, nil
}

// OrphanCleanupInterval is how often the periodic orphan sweep re-runs. A dir is
// only reapable once it is older than MaxTokenTTL (24h), so sweeping much more
// often than hourly buys nothing; hourly bounds how long an untracked orphan (a
// dir whose owning session vanished without its RemoveAll succeeding) lingers on
// a process that is never restarted, without adding meaningful load.
const OrphanCleanupInterval = time.Hour

// runOrphanCleanup executes one sweep, recovering from a panic (a background
// goroutine's unrecovered panic would crash the process) and logging the result.
func runOrphanCleanup(component, dir string, cleanup func() (int, error)) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("transcode cleanup panicked", "component", component, "dir", dir, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	if cleaned, err := cleanup(); err != nil {
		slog.Warn("transcode cleanup failed", "component", component, "dir", dir, "error", err)
	} else if cleaned > 0 {
		slog.Info("transcode cleanup removed orphaned dirs", "component", component, "dir", dir, "count", cleaned)
	}
}

// StartBackgroundOrphanCleanup runs a single orphaned-transcode sweep in its own
// goroutine so a slow network-filesystem delete never blocks server startup.
// The sweep is already safe to run concurrently with request handling: it
// spares live/in-flight sessions and any dir younger than MaxTokenTTL, and
// CleanupOrphanedTranscodeDirs serializes concurrent sweeps of the same root.
func StartBackgroundOrphanCleanup(component, dir string, cleanup func() (int, error)) {
	go runOrphanCleanup(component, dir, cleanup)
}

// StartPeriodicOrphanCleanup runs an immediate background sweep and then repeats
// it every interval until ctx is cancelled. The startup sweep only reclaims dirs
// orphaned by an ungraceful prior shutdown; the periodic re-run additionally
// bounds "untracked orphan" accumulation (a dir whose owning session was dropped
// without its RemoveAll succeeding) on a process that stays up for weeks. When
// ctx is nil or interval is non-positive it degrades to a single boot-time sweep
// so no ticker goroutine outlives a caller with no lifecycle handle (e.g. tests).
func StartPeriodicOrphanCleanup(ctx context.Context, component, dir string, cleanup func() (int, error), interval time.Duration) {
	if ctx == nil || interval <= 0 {
		StartBackgroundOrphanCleanup(component, dir, cleanup)
		return
	}
	go func() {
		runOrphanCleanup(component, dir, cleanup)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runOrphanCleanup(component, dir, cleanup)
			}
		}
	}()
}

func transcodeDirBelongsToActiveSession(name string, activeSessionIDs map[string]struct{}) bool {
	if _, ok := activeSessionIDs[name]; ok {
		return true
	}
	for sessionID := range activeSessionIDs {
		if sessionID != "" && strings.HasPrefix(name, sessionID+"-") {
			return true
		}
	}
	return false
}
