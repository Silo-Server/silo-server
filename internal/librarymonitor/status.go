package librarymonitor

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// State is a node's monitoring state for one library, as stored in
// library_monitor_status. The settings-derived states (server off, library
// off, library disabled) are computed by readers, never stored.
type State string

const (
	StateStarting              State = "starting"
	StateMonitoring            State = "monitoring"
	StateUnsupportedFilesystem State = "unsupported_filesystem"
	StateUnsupportedPlatform   State = "unsupported_platform"
	StateLimitReached          State = "limit_reached"
	StateRootUnavailable       State = "root_unavailable"
	StateError                 State = "error"
)

// stateInvisible is a root this node cannot see and never monitored. It is
// never reported.
const stateInvisible State = ""

// nodeSeverity orders root states for a node's per-library row: the worst
// root wins. unsupported_platform is not in the contract's list; it sits
// with the other "cannot monitor here" states, and a node only ever has one
// platform anyway.
var nodeSeverity = map[State]int{
	StateError:                 7,
	StateLimitReached:          6,
	StateRootUnavailable:       5,
	StateUnsupportedFilesystem: 4,
	StateUnsupportedPlatform:   3,
	StateStarting:              2,
	StateMonitoring:            1,
}

// LibraryStatus is one node's report for one library.
type LibraryStatus struct {
	LibraryID int
	State     State
	// Backend is "inotify", or empty when no folder is recorded.
	Backend     string
	Detail      string
	Directories int
}

// StatusReporter persists node reports. StatusStore implements it over
// library_monitor_status.
type StatusReporter interface {
	// Report replaces this node's rows: it upserts every given status
	// (refreshing updated_at) and deletes the node's rows for libraries not
	// in the list. An empty list deletes all of the node's rows.
	Report(ctx context.Context, nodeID string, statuses []LibraryStatus) error
	// RemoveNode deletes the node's rows; called on clean shutdown.
	RemoveNode(ctx context.Context, nodeID string) error
}

// rootView is the part of a root's state the aggregation reads.
type rootView struct {
	path        string
	state       State
	backend     string
	detail      string
	directories int
}

// aggregateLibrary builds one library's node row from its roots, in library
// path order. configured is how many distinct paths the library has, which
// can be more than len(roots): a path with no state on this node yet has no
// view. Roots this node cannot see are skipped; when none is visible there
// is no row. The worst root's state wins and directories are summed. The
// detail says each distinct thing once: roots with the same detail are named
// together, and a detail every path of the library shares is not prefixed
// with paths at all.
func aggregateLibrary(libraryID int, roots []rootView, configured int) (LibraryStatus, bool) {
	status := LibraryStatus{LibraryID: libraryID}
	type detailGroup struct {
		detail string
		paths  []string
	}
	var groups []*detailGroup
	byDetail := make(map[string]*detailGroup)
	visible := 0
	for _, root := range roots {
		if root.state == stateInvisible {
			continue
		}
		visible++
		if nodeSeverity[root.state] > nodeSeverity[status.State] {
			status.State = root.state
		}
		if root.backend != "" {
			status.Backend = root.backend
		}
		if root.detail != "" {
			g := byDetail[root.detail]
			if g == nil {
				g = &detailGroup{detail: root.detail}
				byDetail[root.detail] = g
				groups = append(groups, g)
			}
			g.paths = append(g.paths, root.path)
		}
		status.Directories += root.directories
	}
	if visible == 0 {
		return LibraryStatus{}, false
	}
	details := make([]string, 0, len(groups))
	for _, g := range groups {
		// Compared with every configured path, not just the visible ones: a
		// library with a root this node cannot see, or has no state for yet,
		// still needs paths to tell its roots apart.
		if len(g.paths) == configured {
			details = append(details, g.detail)
			continue
		}
		details = append(details, strings.Join(g.paths, ", ")+": "+g.detail)
	}
	status.Detail = strings.Join(details, " ")
	return status, true
}

// statusRowsLocked builds this node's rows from the current roots. Caller
// holds m.mu.
func (m *Monitor) statusRowsLocked() []LibraryStatus {
	rows := make([]LibraryStatus, 0, len(m.desiredOrder))
	for _, folder := range m.desiredOrder {
		views, configured := m.rootViewsLocked(folder)
		row, ok := aggregateLibrary(folder.ID, views, configured)
		if ok {
			rows = append(rows, row)
		}
	}
	return rows
}

// rootViewsLocked returns the views of the folder's roots that have state,
// and how many distinct paths the folder has.
func (m *Monitor) rootViewsLocked(folder *models.MediaFolder) ([]rootView, int) {
	seen := make(map[string]bool, len(folder.Paths))
	views := make([]rootView, 0, len(folder.Paths))
	for _, raw := range folder.Paths {
		path := cleanRoot(raw)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		rs := m.roots[path]
		if rs == nil {
			continue
		}
		view := rootView{path: path, state: rs.state, backend: rs.backendName, detail: rs.statusDetail(), directories: rs.dirs}
		if rs.backend != nil {
			view.directories = rs.backend.Directories(path)
		}
		views = append(views, view)
	}
	return views, len(seen)
}

// statusDetail is the root's contribution to the row detail.
func (rs *rootState) statusDetail() string {
	switch rs.state {
	case StateMonitoring, StateStarting:
		parts := slices.Clone(rs.notes)
		if len(rs.unsupportedMounts) > 0 {
			parts = append(parts, "Folders on network filesystems aren't monitored: "+summarizePaths(rs.unsupportedMounts)+".")
		}
		if len(rs.unreadable) > 0 {
			parts = append(parts, "Folders Silo can't read aren't monitored: "+summarizePaths(rs.unreadable)+".")
		}
		return strings.Join(parts, " ")
	case StateUnsupportedFilesystem:
		return fmt.Sprintf("%s network filesystems aren't supported. Changes here are picked up by arr webhooks, the CephFS autoscan source, or the nightly scan.", rs.fsName)
	case StateUnsupportedPlatform:
		return "Real-time monitoring needs Linux."
	case StateLimitReached:
		return limitDetail(rs.limit, rs.dirs)
	case StateRootUnavailable:
		return "The folder is missing or was unmounted. Silo keeps retrying."
	case StateError:
		return rs.errText
	default:
		return ""
	}
}

// maxDetailPaths bounds how many paths a detail names; a symlink tree into
// a network share can lead to hundreds.
const maxDetailPaths = 3

// summarizePaths names up to maxDetailPaths paths and counts the rest.
func summarizePaths(paths []string) string {
	if len(paths) <= maxDetailPaths {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(paths[:maxDetailPaths], ", "), len(paths)-maxDetailPaths)
}

func limitDetail(limit, dirs int) string {
	limitText := "fs.inotify.max_user_watches"
	if limit > 0 {
		limitText = fmt.Sprintf("fs.inotify.max_user_watches (%d)", limit)
	}
	return fmt.Sprintf("Reached the inotify watch limit %s; this folder needs %d watches. "+
		"Raise the limit on the host (containers can't change it).",
		limitText, dirs)
}

// cleanRoot normalizes a configured library path the way the resolver does.
func cleanRoot(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	clean := filepath.Clean(path)
	if clean == "." {
		return ""
	}
	return clean
}
