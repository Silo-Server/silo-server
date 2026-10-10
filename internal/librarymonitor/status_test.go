package librarymonitor

import (
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestAggregateLibrary(t *testing.T) {
	tests := []struct {
		name      string
		roots     []rootView
		wantOK    bool
		want      LibraryStatus
		wantInDet []string
		// exactDet makes wantInDet's only entry the whole detail.
		exactDet bool
		// configured is the library's path count; 0 means len(roots).
		configured int
	}{
		{
			name:   "no visible root writes no row",
			roots:  []rootView{{path: "/a", state: stateInvisible}, {path: "/b", state: stateInvisible}},
			wantOK: false,
		},
		{
			name: "all monitoring sums directories",
			roots: []rootView{
				{path: "/a", state: StateMonitoring, backend: "inotify", directories: 10},
				{path: "/b", state: StateMonitoring, backend: "inotify", directories: 5},
			},
			wantOK: true,
			want:   LibraryStatus{LibraryID: 7, State: StateMonitoring, Backend: "inotify", Directories: 15},
		},
		{
			name: "invisible roots are skipped",
			roots: []rootView{
				{path: "/a", state: StateMonitoring, backend: "inotify", directories: 10},
				{path: "/elsewhere", state: stateInvisible},
			},
			wantOK: true,
			want:   LibraryStatus{LibraryID: 7, State: StateMonitoring, Backend: "inotify", Directories: 10},
		},
		{
			name: "worst root wins and is named",
			roots: []rootView{
				{path: "/a", state: StateMonitoring, backend: "inotify", directories: 10},
				{path: "/b", state: StateLimitReached, backend: "inotify", detail: "limit text", directories: 90},
				{path: "/c", state: StateRootUnavailable, detail: "gone"},
			},
			wantOK:    true,
			want:      LibraryStatus{LibraryID: 7, State: StateLimitReached, Backend: "inotify", Directories: 100},
			wantInDet: []string{"/b: limit text", "/c: gone"},
		},
		{
			name: "error beats everything",
			roots: []rootView{
				{path: "/a", state: StateUnsupportedFilesystem, detail: "nfs"},
				{path: "/b", state: StateError, detail: "boom"},
			},
			wantOK: true,
			want:   LibraryStatus{LibraryID: 7, State: StateError},
		},
		{
			name: "starting beats monitoring",
			roots: []rootView{
				{path: "/a", state: StateMonitoring, backend: "inotify"},
				{path: "/b", state: StateStarting},
			},
			wantOK: true,
			want:   LibraryStatus{LibraryID: 7, State: StateStarting, Backend: "inotify"},
		},
		{
			name: "unsupported filesystem beats starting",
			roots: []rootView{
				{path: "/a", state: StateStarting},
				{path: "/b", state: StateUnsupportedFilesystem},
			},
			wantOK: true,
			want:   LibraryStatus{LibraryID: 7, State: StateUnsupportedFilesystem},
		},
		{
			name: "a detail every root shares is said once, without paths",
			roots: []rootView{
				{path: "/a", state: StateMonitoring, backend: "inotify", detail: "Using a caveat."},
				{path: "/b", state: StateMonitoring, backend: "inotify", detail: "Using a caveat."},
			},
			wantOK:    true,
			want:      LibraryStatus{LibraryID: 7, State: StateMonitoring, Backend: "inotify"},
			wantInDet: []string{"Using a caveat."},
			exactDet:  true,
		},
		{
			// A library path this node has no state for yet still counts:
			// the detail must name the root it belongs to.
			name: "a path without state keeps the detail's path",
			roots: []rootView{
				{path: "/a", state: StateMonitoring, backend: "inotify", detail: "Using a caveat."},
			},
			configured: 2,
			wantOK:     true,
			want:       LibraryStatus{LibraryID: 7, State: StateMonitoring, Backend: "inotify"},
			wantInDet:  []string{"/a: Using a caveat."},
			exactDet:   true,
		},
		{
			name: "roots with the same detail are named together",
			roots: []rootView{
				{path: "/a", state: StateMonitoring, backend: "inotify", detail: "One."},
				{path: "/b", state: StateMonitoring, backend: "inotify", detail: "One."},
				{path: "/c", state: StateMonitoring, backend: "inotify", detail: "Two."},
			},
			wantOK:    true,
			want:      LibraryStatus{LibraryID: 7, State: StateMonitoring, Backend: "inotify"},
			wantInDet: []string{"/a, /b: One. /c: Two."},
			exactDet:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configured := tt.configured
			if configured == 0 {
				configured = len(tt.roots)
			}
			got, ok := aggregateLibrary(7, tt.roots, configured)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			detail := got.Detail
			got.Detail = ""
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			if tt.exactDet && detail != tt.wantInDet[0] {
				t.Fatalf("detail = %q, want %q", detail, tt.wantInDet[0])
			}
			for _, part := range tt.wantInDet {
				if !strings.Contains(detail, part) {
					t.Fatalf("detail %q does not contain %q", detail, part)
				}
			}
		})
	}
}

// A library whose second path has no root state yet (its attempt was not
// spawned, or it was just added) must still name the path its first root's
// detail belongs to.
func TestStatusRowsNameThePathWhileAnotherHasNoState(t *testing.T) {
	m, err := New(Config{Folders: &fakeFolders{}, Queue: newFakeQueue(), Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	lib := library(1, "/a", "/b", "/a/")
	m.desiredOrder = []*models.MediaFolder{lib}
	m.roots["/a"] = &rootState{path: "/a", state: StateMonitoring, backendName: BackendInotify, notes: []string{"Note."}}
	rows := m.statusRowsLocked()
	if len(rows) != 1 || rows[0].Detail != "/a: Note." {
		t.Fatalf("rows = %+v, want one row whose detail names /a", rows)
	}
	m.roots["/b"] = &rootState{path: "/b", state: StateMonitoring, backendName: BackendInotify, notes: []string{"Note."}}
	if rows := m.statusRowsLocked(); rows[0].Detail != "Note." {
		t.Fatalf("detail = %q once both paths share it, want it without paths", rows[0].Detail)
	}
}

func TestRootStatusDetailSummarizesNetworkFolders(t *testing.T) {
	rs := &rootState{state: StateMonitoring, unsupportedMounts: []string{"/m/a", "/m/b", "/m/c", "/m/d", "/m/e"}}
	want := "Folders on network filesystems aren't monitored: /m/a, /m/b, /m/c and 2 more."
	if got := rs.statusDetail(); got != want {
		t.Fatalf("detail = %q, want %q", got, want)
	}
}

// A library folder of "/" is its own parent. Indexing it as its own child
// would make every walk of its subtree loop forever.
func TestChildDirsFilesystemRoot(t *testing.T) {
	c := make(childDirs)
	for _, p := range []string{"/", "/media", "/media/Movies"} {
		c.add(p)
	}
	got := c.subtree("/")
	slices.Sort(got)
	if want := []string{"/", "/media", "/media/Movies"}; !slices.Equal(got, want) {
		t.Fatalf("subtree(/) = %v, want %v", got, want)
	}
}
