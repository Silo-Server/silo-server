package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type AdminDashboardStatsService interface {
	ReadAdminStats(context.Context, bool) (handlers.AdminStats, error)
}
type AdminDashboardStatsInput struct {
	Refresh bool `query:"refresh" default:"false"`
}
type AdminDashboardStats struct {
	TotalItems        int                                `json:"total_items"`
	TotalFiles        int                                `json:"total_files"`
	TotalUsers        int                                `json:"total_users"`
	TotalMovies       int                                `json:"total_movies"`
	TotalMovieFiles   int                                `json:"total_movie_files"`
	TotalShows        int                                `json:"total_shows"`
	TotalShowFiles    int                                `json:"total_show_files"`
	AdvisoryTitles    int                                `json:"advisory_titles" doc:"Movies and series that carry an advisory age, the coverage a profile's max_advisory_age limit acts on; compare with total_movies + total_shows"`
	ActiveStreams     int                                `json:"active_streams"`
	TotalStorageBytes int64                              `json:"total_storage_bytes"`
	WatchProviders    []AdminDashboardWatchProviderStats `json:"watch_providers"`
}
type AdminDashboardWatchProviderStats struct {
	Provider    string `json:"provider"`
	DisplayName string `json:"display_name"`
	// Registered is false for a provider that only exists in stored rows —
	// its plugin is uninstalled or disabled.
	Registered bool `json:"registered"`
	// Scrobbling and Exporting mirror the provider's declared capabilities so
	// the widget can hide counters a provider can never produce.
	Scrobbling              bool     `json:"scrobbling"`
	Exporting               bool     `json:"exporting"`
	ConnectedProfiles       int64    `json:"connected_profiles"`
	EnabledProfiles         int64    `json:"enabled_profiles"`
	ExportEnabledProfiles   int64    `json:"export_enabled_profiles"`
	ScrobbleEnabledProfiles int64    `json:"scrobble_enabled_profiles"`
	LastSyncCompletedAt     *Instant `json:"last_sync_completed_at,omitempty"`
	SyncRuns24h             int64    `json:"sync_runs_24h"`
	SyncErrors24h           int64    `json:"sync_errors_24h"`
	ImportedWatched24h      int64    `json:"imported_watched_24h"`
	ImportedProgress24h     int64    `json:"imported_progress_24h"`
	ExportedWatched24h      int64    `json:"exported_watched_24h"`
	PendingExports          int64    `json:"pending_exports"`
	FailedExports           int64    `json:"failed_exports"`
	OpenScrobbles           int64    `json:"open_scrobbles"`
	Scrobbles24h            int64    `json:"scrobbles_24h"`
}
type AdminDashboardStatsOutput struct{ Body AdminDashboardStats }

func registerAdminDashboardStats(reg *Registry) {
	Register(reg, Operation{Operation: humaOp("GET", Prefix+"/admin/stats", "getAdminDashboardStats", "admin-observability", "Read overall dashboard statistics using the existing provider cache and fallback."), Class: ClassActingAdmin, ServiceBacked: true}, func(ctx context.Context, in *AdminDashboardStatsInput) (*AdminDashboardStatsOutput, error) {
		if reg.deps.AdminDashboardStats == nil {
			return nil, unavailable("dashboard statistics")
		}
		s, err := reg.deps.AdminDashboardStats.ReadAdminStats(ctx, in.Refresh)
		if err != nil {
			return nil, serviceProblem(err)
		}
		out := AdminDashboardStats{TotalItems: s.TotalItems, TotalFiles: s.TotalFiles, TotalUsers: s.TotalUsers, TotalMovies: s.TotalMovies, TotalMovieFiles: s.TotalMovieFiles, TotalShows: s.TotalShows, TotalShowFiles: s.TotalShowFiles, AdvisoryTitles: s.AdvisoryTitles, ActiveStreams: s.ActiveStreams, TotalStorageBytes: s.TotalStorageBytes, WatchProviders: make([]AdminDashboardWatchProviderStats, 0, len(s.WatchProviders))}
		for _, p := range s.WatchProviders {
			out.WatchProviders = append(out.WatchProviders, AdminDashboardWatchProviderStats{Provider: p.Provider, DisplayName: p.DisplayName, Registered: p.Registered, Scrobbling: p.Scrobbling, Exporting: p.Exporting, ConnectedProfiles: p.ConnectedProfiles, EnabledProfiles: p.EnabledProfiles, ExportEnabledProfiles: p.ExportEnabledProfiles, ScrobbleEnabledProfiles: p.ScrobbleEnabledProfiles, LastSyncCompletedAt: instantPtr(p.LastSyncCompletedAt), SyncRuns24h: p.SyncRuns24h, SyncErrors24h: p.SyncErrors24h, ImportedWatched24h: p.ImportedWatched24h, ImportedProgress24h: p.ImportedProgress24h, ExportedWatched24h: p.ExportedWatched24h, PendingExports: p.PendingExports, FailedExports: p.FailedExports, OpenScrobbles: p.OpenScrobbles, Scrobbles24h: p.Scrobbles24h})
		}
		return &AdminDashboardStatsOutput{Body: out}, nil
	})
}
