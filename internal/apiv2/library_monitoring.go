package apiv2

import (
	"context"
	"net/http"
	"sort"

	"github.com/Silo-Server/silo-server/internal/librarymonitor"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Real-time library monitoring: the administrator status read and the
// feature's capability document. Node reports are read from
// library_monitor_status; the settings-derived states are computed here.

// Settings-derived monitoring states. They are computed at read time and
// never stored by a node.
const (
	monitoringStateServerDisabled  = "server_disabled"
	monitoringStateLibraryDisabled = "library_disabled"
	monitoringStateOff             = "monitoring_off"
	monitoringStateNotReporting    = "not_reporting"
)

// monitoringNotReportingDetail explains not_reporting.
const monitoringNotReportingDetail = "No server node can see this library's folders"

// monitoringReportRank orders node-reported states when several nodes report
// one library: the best state wins.
var monitoringReportRank = map[librarymonitor.State]int{
	librarymonitor.StateMonitoring:            7,
	librarymonitor.StateStarting:              6,
	librarymonitor.StateLimitReached:          5,
	librarymonitor.StateRootUnavailable:       4,
	librarymonitor.StateUnsupportedFilesystem: 3,
	librarymonitor.StateUnsupportedPlatform:   2,
	librarymonitor.StateError:                 1,
}

// LibraryMonitoringService reads what the real-time monitoring status is
// derived from (*librarymonitor.StatusReader).
type LibraryMonitoringService interface {
	RealtimeMonitoringStatus(ctx context.Context) (librarymonitor.StatusSnapshot, error)
}

// LibraryRealtimeMonitoring is the getLibraryRealtimeMonitoring body.
type LibraryRealtimeMonitoring struct {
	ServerEnabled bool                             `json:"server_enabled" doc:"The server-wide scanner.realtime_monitoring setting" example:"true"`
	Libraries     []LibraryRealtimeMonitoringEntry `json:"libraries" doc:"One entry per library, in sort order then by ID"`
}

// LibraryRealtimeMonitoringEntry is one library's effective monitoring state.
type LibraryRealtimeMonitoringEntry struct {
	LibraryID   ID       `json:"library_id" example:"1"`
	Enabled     bool     `json:"enabled" doc:"The library's own realtime_monitoring switch" example:"true"`
	State       string   `json:"state" enum:"server_disabled,library_disabled,monitoring_off,not_reporting,starting,monitoring,unsupported_filesystem,unsupported_platform,limit_reached,root_unavailable,error" doc:"server_disabled, library_disabled, monitoring_off and not_reporting are derived from settings and report freshness, checked in that order; otherwise the best state a server node reported in the last 3 minutes (monitoring, then starting, limit_reached, root_unavailable, unsupported_filesystem, unsupported_platform, error)" example:"monitoring"`
	Backend     string   `json:"backend" doc:"Kernel notification backend of the reporting node: inotify, or empty when none is recorded" example:"inotify"`
	Detail      string   `json:"detail" doc:"Why monitoring is not working, or a caveat about it; empty when there is nothing to say" example:""`
	Directories int      `json:"directories" doc:"Folders the reporting node records for the library" example:"4812"`
	NodeID      string   `json:"node_id,omitempty" doc:"The server node whose report the state comes from; absent without a fresh report" example:"node-a"`
	UpdatedAt   *Instant `json:"updated_at,omitempty" doc:"When that node last reported; absent without a fresh report"`
}

// LibraryRealtimeMonitoringOutput is the getLibraryRealtimeMonitoring response.
type LibraryRealtimeMonitoringOutput struct {
	Body LibraryRealtimeMonitoring
}

// LibraryCapabilities is the getLibraryCapabilities body.
type LibraryCapabilities struct {
	Capability
	RealtimeMonitoring bool `json:"realtime_monitoring" doc:"This build supports real-time library monitoring and its status read" example:"true"`
}

func (c LibraryCapabilities) capabilityState() string { return StateAvailable }

// LibraryCapabilitiesOutput is the getLibraryCapabilities response.
type LibraryCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         LibraryCapabilities
}

func registerLibraryMonitoring(reg *Registry) {
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/libraries/realtime-monitoring", "getLibraryRealtimeMonitoring", "libraries",
			"Report whether real-time monitoring works for each library: the server switch, then each library's effective state from settings and the fresh reports of the server nodes that can see its folders."),
		Class:         ClassActingAdmin,
		ServiceBacked: true,
	}, reg.getLibraryRealtimeMonitoring)

	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/libraries/capabilities", "getLibraryCapabilities", "libraries",
			"Discover library features supported by this build."),
		Class: ClassActingAdmin,
	}, func(context.Context, *CapabilityInput) (*LibraryCapabilitiesOutput, error) {
		return &LibraryCapabilitiesOutput{Body: LibraryCapabilities{RealtimeMonitoring: true}}, nil
	})
}

func (reg *Registry) getLibraryRealtimeMonitoring(ctx context.Context, _ *struct{}) (*LibraryRealtimeMonitoringOutput, error) {
	svc := reg.deps.LibraryMonitoring
	if svc == nil {
		return nil, unavailable("library monitoring")
	}
	snap, err := svc.RealtimeMonitoringStatus(ctx)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &LibraryRealtimeMonitoringOutput{Body: realtimeMonitoringOf(snap)}, nil
}

// realtimeMonitoringOf derives each library's effective state.
func realtimeMonitoringOf(snap librarymonitor.StatusSnapshot) LibraryRealtimeMonitoring {
	folders := make([]*models.MediaFolder, 0, len(snap.Libraries))
	for _, f := range snap.Libraries {
		if f != nil {
			folders = append(folders, f)
		}
	}
	sort.SliceStable(folders, func(i, j int) bool {
		if folders[i].SortOrder != folders[j].SortOrder {
			return folders[i].SortOrder < folders[j].SortOrder
		}
		return folders[i].ID < folders[j].ID
	})
	best := make(map[int]librarymonitor.NodeReport, len(snap.Reports))
	for _, r := range snap.Reports {
		if current, ok := best[r.LibraryID]; !ok || betterMonitoringReport(r, current) {
			best[r.LibraryID] = r
		}
	}

	out := LibraryRealtimeMonitoring{ServerEnabled: snap.ServerEnabled, Libraries: make([]LibraryRealtimeMonitoringEntry, 0, len(folders))}
	for _, f := range folders {
		entry := LibraryRealtimeMonitoringEntry{LibraryID: IDFromInt(int64(f.ID)), Enabled: f.RealtimeMonitoring}
		report, reported := best[f.ID]
		switch {
		case !snap.ServerEnabled:
			entry.State = monitoringStateServerDisabled
		case !f.Enabled:
			entry.State = monitoringStateLibraryDisabled
		case !f.RealtimeMonitoring:
			entry.State = monitoringStateOff
		case !reported:
			entry.State = monitoringStateNotReporting
			entry.Detail = monitoringNotReportingDetail
		default:
			entry.State = string(report.State)
			if monitoringReportRank[report.State] == 0 {
				// A state this build does not know (a newer node) is shown as
				// an error so the enum holds; its detail is kept.
				entry.State = string(librarymonitor.StateError)
			}
			entry.Backend = report.Backend
			entry.Detail = report.Detail
			entry.Directories = report.Directories
			entry.NodeID = report.NodeID
			updated := NewInstant(report.UpdatedAt)
			entry.UpdatedAt = &updated
		}
		out.Libraries = append(out.Libraries, entry)
	}
	return out
}

// betterMonitoringReport reports whether a beats b: the better state wins,
// then the newer report, then the lower node ID so the answer is stable.
func betterMonitoringReport(a, b librarymonitor.NodeReport) bool {
	ra, rb := monitoringReportRank[a.State], monitoringReportRank[b.State]
	if ra != rb {
		return ra > rb
	}
	if !a.UpdatedAt.Equal(b.UpdatedAt) {
		return a.UpdatedAt.After(b.UpdatedAt)
	}
	return a.NodeID < b.NodeID
}
