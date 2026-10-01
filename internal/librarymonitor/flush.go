package librarymonitor

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

// flush resolves the changes due at now into scan targets and queues them.
// A *scantrigger.RequestError is an expected skip (a sidecar file, a path
// that vanished again, a library root that went offline). Other resolve and
// enqueue failures are retried once on the next flush, then dropped with a
// warning.
func (m *Monitor) flush(ctx context.Context, now time.Time) {
	changes := append(m.retry, m.tracker.ready(now)...)
	m.retry = nil

	m.mu.Lock()
	desired := m.desired
	resolver := m.cfg.NewResolver(folderSnapshot(m.listed))
	libraryScans := make([]int, 0, len(m.libraryScans))
	for id := range m.libraryScans {
		libraryScans = append(libraryScans, id)
	}
	clear(m.libraryScans)
	m.mu.Unlock()
	// A target kept for a retry is dropped like a fresh one when its
	// library turned monitoring off in the meantime.
	var retried []scantrigger.Target
	for _, t := range m.retryTargets {
		if _, ok := desired[t.Folder.ID]; ok {
			retried = append(retried, t)
		}
	}
	m.retryTargets = nil
	if len(changes) == 0 && len(libraryScans) == 0 && len(retried) == 0 {
		return
	}

	sort.Ints(libraryScans)
	var fresh []scantrigger.Target
	for _, id := range libraryScans {
		if folder := desired[id]; folder != nil {
			fresh = append(fresh, libraryTarget(folder))
		}
	}
	for _, c := range changes {
		target, err := m.resolve(ctx, resolver, c)
		if err != nil {
			var reqErr *scantrigger.RequestError
			if errors.As(err, &reqErr) {
				m.log.DebugContext(ctx, "librarymonitor: change not scannable", "path", c.path, "kind", c.kind.String(), "reason", string(reqErr.Reason))
				continue
			}
			if ctx.Err() != nil {
				return
			}
			m.retryOrDrop(ctx, c, err)
			continue
		}
		// A single path never widens to a whole library: that happens only
		// after an overflow or an oversized burst.
		if target == nil || target.Folder == nil || target.Mode == scantrigger.ModeLibrary {
			continue
		}
		if _, ok := desired[target.Folder.ID]; !ok {
			continue // the owning library has monitoring off
		}
		fresh = append(fresh, *target)
	}

	targets := prepareTargets(append(retried, fresh...))
	if len(targets) == 0 {
		return
	}
	for _, collapsed := range collapsedLibraries(targets) {
		m.log.WarnContext(ctx, "librarymonitor: large change burst collapsed to a library scan", "library_id", collapsed, "limit", maxTargetsPerLibrary)
	}
	targets = collapseLargeLibraries(targets)

	enqueueCtx, cancel := context.WithTimeout(ctx, enqueueTimeout)
	err := m.cfg.Queue.EnqueueScans(enqueueCtx, targets)
	cancel()
	if err == nil || ctx.Err() != nil {
		return
	}
	// Retry what has not been retried yet.
	m.retryTargets = prepareTargets(fresh)
	m.log.WarnContext(ctx, "librarymonitor: queueing scans failed", "targets", len(targets), "retrying", len(m.retryTargets), "err", err)
}

// retryOrDrop keeps a change whose resolution failed for the next flush, or
// drops it when it already had its one retry.
func (m *Monitor) retryOrDrop(ctx context.Context, c change, err error) {
	c.attempts++
	if c.attempts < 2 {
		m.log.WarnContext(ctx, "librarymonitor: resolving change failed; retrying", "path", c.path, "kind", c.kind.String(), "err", err)
		m.retry = append(m.retry, c)
		return
	}
	m.log.WarnContext(ctx, "librarymonitor: resolving change failed again; dropping it", "path", c.path, "kind", c.kind.String(), "err", err)
}

// resolve maps one change to a scan target with the resolver method for its
// kind.
func (m *Monitor) resolve(ctx context.Context, r Resolver, c change) (*scantrigger.Target, error) {
	switch c.kind {
	case changeFile, changeDir:
		return r.Resolve(ctx, scantrigger.Request{Path: c.path, Trigger: Trigger})
	case changeVanishedFile:
		return r.ResolveVanishedPath(ctx, c.path, Trigger)
	case changeVanishedDir:
		return r.ResolveMissingSubtree(ctx, c.path, Trigger)
	default:
		return nil, fmt.Errorf("unknown change kind %d", c.kind)
	}
}

// folderSnapshot serves the libraries the last reconcile listed, so a flush
// resolves a whole burst of changes without a database query per path. A
// library created or edited on this node is re-listed at once (Poke); one
// edited on another node shows up within the reconcile interval.
type folderSnapshot []*models.MediaFolder

func (s folderSnapshot) List(context.Context) ([]*models.MediaFolder, error) { return s, nil }

func (s folderSnapshot) GetByID(_ context.Context, id int) (*models.MediaFolder, error) {
	for _, f := range s {
		if f.ID == id {
			return f, nil
		}
	}
	return nil, catalog.ErrFolderNotFound
}

func libraryTarget(folder *models.MediaFolder) scantrigger.Target {
	return scantrigger.Target{Folder: folder, Mode: scantrigger.ModeLibrary, Trigger: Trigger}
}

// prepareTargets deduplicates targets, and drops every other target of a
// library that also has a library scan.
func prepareTargets(targets []scantrigger.Target) []scantrigger.Target {
	wholeLibrary := make(map[int]bool)
	for _, t := range targets {
		if t.Folder != nil && t.Mode == scantrigger.ModeLibrary {
			wholeLibrary[t.Folder.ID] = true
		}
	}
	seen := make(map[string]bool, len(targets))
	out := make([]scantrigger.Target, 0, len(targets))
	for _, t := range targets {
		if t.Folder == nil {
			continue
		}
		if wholeLibrary[t.Folder.ID] && t.Mode != scantrigger.ModeLibrary {
			continue
		}
		key := fmt.Sprintf("%d|%s|%s", t.Folder.ID, t.Mode, t.Path)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	return out
}

func collapsedLibraries(targets []scantrigger.Target) []int {
	counts := make(map[int]int)
	for _, t := range targets {
		counts[t.Folder.ID]++
	}
	var ids []int
	for id, n := range counts {
		if n > maxTargetsPerLibrary {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	return ids
}

// collapseLargeLibraries replaces the targets of any library with more than
// maxTargetsPerLibrary targets by one library scan, at the position of its
// first target.
func collapseLargeLibraries(targets []scantrigger.Target) []scantrigger.Target {
	collapse := make(map[int]bool)
	for _, id := range collapsedLibraries(targets) {
		collapse[id] = true
	}
	if len(collapse) == 0 {
		return targets
	}
	out := make([]scantrigger.Target, 0, len(targets))
	emitted := make(map[int]bool)
	for _, t := range targets {
		id := t.Folder.ID
		if !collapse[id] {
			out = append(out, t)
			continue
		}
		if !emitted[id] {
			emitted[id] = true
			out = append(out, libraryTarget(t.Folder))
		}
	}
	return out
}
