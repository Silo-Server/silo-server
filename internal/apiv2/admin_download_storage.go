package apiv2

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/downloadstorage"
	evt "github.com/Silo-Server/silo-server/internal/events"
)

// AdminDownloadStorageService reads and cleans up prepared download files on
// the server and transcode nodes (*downloads.ArtifactManager).
type AdminDownloadStorageService interface {
	StorageOverview(context.Context) (*downloads.StorageOverview, error)
	StorageFilesPage(context.Context, downloads.StorageFileFilter, *downloads.StorageFilePosition, int) ([]downloads.StorageFile, error)
	StorageEventsPage(context.Context, downloads.StorageEventFilter, *downloads.StorageEventPosition, int) ([]downloads.StorageEventBatch, error)
	DeleteStorageFiles(ctx context.Context, ids []string, includeInUse bool, actor int) ([]downloads.StorageDeleteResult, error)
	CleanupLocation(ctx context.Context, location string) (int64, error)
	DeleteUntrackedFiles(ctx context.Context, location string, actor int) (downloads.UntrackedDeleteResult, error)
}

// AdminDownloadDeviceService reads every device's managed downloads and
// revokes them (*downloads.Service).
type AdminDownloadDeviceService interface {
	AdminListDevicesPage(context.Context, downloads.AdminDeviceFilter, *downloads.AdminDevicePosition, int) ([]downloads.AdminDeviceRow, error)
	AdminListEntriesPage(context.Context, downloads.AdminEntryFilter, *downloads.RegistryPosition, int) ([]downloads.AdminEntryRow, error)
	RevokeDeviceDownloads(context.Context, downloads.RevokeRequest) (*downloads.RevokeResult, error)
}

// AdminDownloadStorageUsage is one measurement of a location's directory,
// read from the files on disk.
type AdminDownloadStorageUsage struct {
	MeasuredAt    Instant `json:"measured_at"`
	Files         int     `json:"files" minimum:"0" doc:"Finished prepared files"`
	Bytes         int64   `json:"bytes" minimum:"0" doc:"Size of the finished prepared files"`
	PartialFiles  int     `json:"partial_files" minimum:"0" doc:"Files an encode is writing, or left behind"`
	PartialBytes  int64   `json:"partial_bytes" minimum:"0"`
	OtherBytes    int64   `json:"other_bytes" minimum:"0" doc:"Receipts and other small files"`
	FSUsedBytes   int64   `json:"fs_used_bytes" minimum:"0" doc:"Bytes used on the filesystem the directory is on, as df counts them"`
	FSTotalBytes  int64   `json:"fs_total_bytes" minimum:"0" doc:"Capacity of that filesystem available to Silo; 0 when unknown"`
	FSType        string  `json:"fs_type,omitempty" doc:"ext4, xfs, tmpfs, overlay, nfs and so on; empty when unknown"`
	SharesScratch bool    `json:"shares_scratch" doc:"The directory is on the same filesystem as the transcode scratch directory"`
	Ephemeral     bool    `json:"ephemeral" doc:"Files here do not survive a restart (tmpfs) or a container recreation (the container's overlay layer)"`
	Stale         bool    `json:"stale" doc:"The last measurement did not finish or failed; the numbers are older"`
	Error         string  `json:"error,omitempty" doc:"Why the last measurement failed"`
}

// AdminDownloadStorageLocation is the server or one transcode node.
type AdminDownloadStorageLocation struct {
	Key               string                     `json:"key" doc:"server, or node:<id>"`
	Kind              string                     `json:"kind" enum:"server,node"`
	NodeID            *ID                        `json:"node_id,omitempty"`
	Name              string                     `json:"name"`
	Enabled           bool                       `json:"enabled"`
	Online            bool                       `json:"online" doc:"The server, or an enabled node whose last health check succeeded"`
	LastHealthCheck   *Instant                   `json:"last_health_check,omitempty"`
	Dir               string                     `json:"dir,omitempty" doc:"Directory of prepared files. For a node, the one it was last listed in, which is the one it uses; before its first listing, the configured one, or empty when that is the default."`
	DirSource         string                     `json:"dir_source" enum:"default,setting,override" doc:"Where the configured directory comes from. default: the built-in location. setting: download.artifact_dir. override: this node's own directory."`
	PendingDir        string                     `json:"pending_dir,omitempty" doc:"A node's configured directory when it differs from the one it was last listed in. The node moves to it when it restarts; files are not moved."`
	Usage             *AdminDownloadStorageUsage `json:"usage,omitempty" doc:"Latest measurement of the files on disk; absent until one is reported"`
	InUseFiles        int                        `json:"in_use_files" minimum:"0" doc:"Files a download is waiting on or fetching, by Silo's records"`
	InUseBytes        int64                      `json:"in_use_bytes" minimum:"0"`
	CachedFiles       int                        `json:"cached_files" minimum:"0" doc:"Files nothing is waiting on; deleted when their cache period ends"`
	CachedBytes       int64                      `json:"cached_bytes" minimum:"0"`
	WaitingDownloads  int                        `json:"waiting_downloads" minimum:"0" doc:"Downloads waiting on or fetching files here"`
	StaleWaitingBytes int64                      `json:"stale_waiting_bytes" minimum:"0" doc:"In-use bytes kept only for devices not seen within stale_device_days"`
	UntrackedFiles    int                        `json:"untracked_files" minimum:"0" doc:"Files on disk that no prepared-file record accounts for, at the last reconciliation"`
	UntrackedBytes    int64                      `json:"untracked_bytes" minimum:"0"`
	ReconciledAt      *Instant                   `json:"reconciled_at,omitempty"`
	BudgetBytes       int64                      `json:"budget_bytes" minimum:"0" doc:"Storage budget for prepared files here; 0 means none"`
	BudgetSource      string                     `json:"budget_source" enum:"setting,override,none"`
	CleanupBacklog    int                        `json:"cleanup_backlog" minimum:"0" doc:"Node files queued for deletion and not yet deleted"`
	StorageFull       bool                       `json:"storage_full" doc:"Over its budget or the disk ceiling with nothing left to free. A full node gets no new preparations; while the server is full, jobs that would be prepared on it wait."`
	ReplicasDisagree  bool                       `json:"replicas_disagree" doc:"API replicas report different directories or filesystems for the server's prepared files; they should share one volume"`
}

// AdminDownloadStorage is the server-wide prepared-file storage summary.
type AdminDownloadStorage struct {
	Locations          []AdminDownloadStorageLocation `json:"locations" doc:"The server first, then transcode nodes by name"`
	CacheHours         int                            `json:"cache_hours" minimum:"0" doc:"How long a prepared file nothing is waiting on stays after its last use"`
	DiskCeilingPercent int                            `json:"disk_ceiling_percent" minimum:"0" maximum:"100" doc:"Filesystem fill at which clean-up deletes cached files early"`
	DefaultBudgetBytes int64                          `json:"default_budget_bytes" minimum:"0" doc:"download.artifact_max_bytes, the budget at every location without its own; 0 means none"`
	StaleDeviceDays    int                            `json:"stale_device_days" minimum:"0" doc:"Devices not seen for this long are counted as stale"`
	PreparingJobs      int                            `json:"preparing_jobs" minimum:"0" doc:"Unfinished preparation jobs (see the preparation queue)"`
	FreedLast30Days    int64                          `json:"freed_last_30_days_bytes" minimum:"0" doc:"Bytes clean-up and administrators removed from the server and nodes in the last 30 days"`
}

type AdminDownloadStorageOutput struct{ Body AdminDownloadStorage }

// AdminDownloadStorageFile is one prepared file.
type AdminDownloadStorageFile struct {
	ID            ID                          `json:"id" doc:"Prepared file id (the preparation job id)"`
	State         string                      `json:"state" enum:"in_use,cached,expired" doc:"in_use: a download is waiting on or fetching it. cached: nothing is; it is deleted when expires_at passes. expired: its bytes were deleted; finished devices keep their copies."`
	Format        string                      `json:"format" enum:"remux,transcode"`
	Location      string                      `json:"location" doc:"server, or node:<id>"`
	LocationName  string                      `json:"location_name"`
	MediaFileID   ID                          `json:"media_file_id"`
	LibraryID     *ID                         `json:"library_id,omitempty"`
	ContentID     string                      `json:"content_id,omitempty" doc:"The movie, or the series an episode belongs to"`
	EpisodeID     string                      `json:"episode_id,omitempty"`
	Title         string                      `json:"title" doc:"Catalog title; empty when the item left the catalog"`
	MediaType     string                      `json:"media_type"`
	Year          *int                        `json:"year,omitempty"`
	SeasonNumber  *int                        `json:"season_number,omitempty"`
	EpisodeNumber *int                        `json:"episode_number,omitempty"`
	EpisodeTitle  string                      `json:"episode_title,omitempty"`
	Container     string                      `json:"container"`
	VideoCodec    string                      `json:"video_codec" doc:"Target video codec, or copy for a remux"`
	AudioCodec    string                      `json:"audio_codec"`
	Resolution    string                      `json:"resolution,omitempty"`
	BitrateKbps   *int                        `json:"bitrate_kbps,omitempty"`
	Bytes         int64                       `json:"bytes" minimum:"0"`
	CreatedAt     Instant                     `json:"created_at"`
	LastUsedAt    Instant                     `json:"last_used_at" doc:"Last served, linked, or finished by a device"`
	ExpiresAt     *Instant                    `json:"expires_at,omitempty" doc:"When a cached file's cache period ends"`
	Waiting       int                         `json:"waiting" minimum:"0" doc:"Downloads that have not started fetching it"`
	Downloading   int                         `json:"downloading" minimum:"0"`
	Finished      int                         `json:"finished" minimum:"0" doc:"Devices that already hold their copy"`
	StaleWaiting  int                         `json:"stale_waiting" minimum:"0" doc:"Waiting downloads on devices not seen within stale_device_days"`
	OldestWaiting *AdminDownloadWaitingDevice `json:"oldest_waiting,omitempty" doc:"The longest-unseen device a stale waiting download belongs to"`
}

type AdminDownloadWaitingDevice struct {
	DeviceName string   `json:"device_name"`
	Username   string   `json:"username"`
	LastSeenAt *Instant `json:"last_seen_at,omitempty"`
}

type AdminDownloadStorageFilesInput struct {
	LimitParam
	Cursor    string `query:"cursor" maxLength:"8192" doc:"Opaque cursor from page.next_cursor"`
	Location  string `query:"location" pattern:"^(server|node:[1-9][0-9]*)?$" maxLength:"32" doc:"Only files at this location"`
	State     string `query:"state" enum:"in_use,cached,expired," doc:"Only files in this state; ready files by default"`
	Format    string `query:"format" enum:"remux,transcode," doc:"Only files of this format"`
	LibraryID string `query:"library_id" pattern:"^([1-9][0-9]*)?$" maxLength:"10" doc:"Only files from this library"`
	Query     string `query:"q" maxLength:"200" doc:"Title search"`
	Sort      string `query:"sort" enum:"size,last_used,created" default:"size" doc:"size: largest first. last_used: least recently used first. created: newest first."`
}

type AdminDownloadStorageFilesOutput struct {
	Body Collection[AdminDownloadStorageFile]
}

type AdminDownloadStorageDeleteInput struct {
	Body struct {
		IDs          []string `json:"ids" minItems:"1" maxItems:"500" doc:"Prepared file ids. A repeated id is reported once."`
		IncludeInUse bool     `json:"include_in_use,omitempty" doc:"Also delete files a download is waiting on or fetching. Each such file is queued to be prepared again and those downloads wait for the new copy."`
	}
}

type AdminDownloadStorageDeleteResult struct {
	ID      string `json:"id"`
	Outcome string `json:"outcome" enum:"deleted,requeued,in_use,not_found,not_ready,failed" doc:"deleted: the file is gone; finished devices keep their copies. requeued: it was in use; it was deleted and queued to be prepared again. in_use: refused because a download is waiting on or fetching it (set include_in_use to delete it anyway) or it was used in the last minute. not_found: no such prepared file. not_ready: it is still being prepared, failed, or was already deleted. failed: an error stopped this file's delete; the others went on. A file left on disk is reported as untracked."`
	Bytes   int64  `json:"bytes" minimum:"0"`
}

type AdminDownloadStorageDeleteOutput struct {
	Body struct {
		Results []AdminDownloadStorageDeleteResult `json:"results" maxItems:"500" doc:"One result per distinct requested id, in request order"`
	}
}

type AdminDownloadStorageLocationInput struct {
	Location string `path:"location" pattern:"^(server|node:[1-9][0-9]*)$" maxLength:"32" doc:"server, or node:<id>"`
}

type AdminDownloadStorageCleanupOutput struct {
	Body struct {
		FreedBytes int64 `json:"freed_bytes" minimum:"0" doc:"Bytes this run deleted. 0 when nothing was due or another replica was already cleaning up."`
	}
}

type AdminDownloadStorageUntrackedOutput struct {
	Body struct {
		Files       int   `json:"files" minimum:"0" doc:"Untracked files deleted"`
		Bytes       int64 `json:"bytes" minimum:"0"`
		FailedFiles int   `json:"failed_files" minimum:"0" doc:"Untracked files that could not be deleted, for example on a read-only directory; they stay untracked"`
	}
}

// AdminDownloadStorageEvent is one clean-up pass or administrator action.
type AdminDownloadStorageEvent struct {
	ID           string                    `json:"id" doc:"Opaque row id: one batch at one location for one reason and account"`
	Reason       string                    `json:"reason" doc:"cache_expired, budget, disk_ceiling, admin_delete, untracked, missing, revoked or device_removed. More values may be added."`
	Location     string                    `json:"location" doc:"server, node:<id>, or device"`
	LocationName string                    `json:"location_name" doc:"Node or device name"`
	OccurredAt   Instant                   `json:"occurred_at"`
	Count        int                       `json:"count" minimum:"0" doc:"Files or downloads the batch touched"`
	Bytes        int64                     `json:"bytes" minimum:"0"`
	Titles       []string                  `json:"titles" maxItems:"3" doc:"The first catalog titles it touched"`
	Detail       string                    `json:"detail,omitempty" doc:"The administrator's reason, or a note such as a file count"`
	Actor        *AdminDownloadStorageUser `json:"actor,omitempty" doc:"The administrator; absent for automatic clean-up"`
	Account      *AdminDownloadStorageUser `json:"account,omitempty" doc:"Whose device a revocation or removal concerned"`
}

type AdminDownloadStorageUser struct {
	ID       ID     `json:"id"`
	Username string `json:"username"`
}

type AdminDownloadStorageEventsInput struct {
	LimitParam
	Cursor   string `query:"cursor" maxLength:"8192"`
	Reason   string `query:"reason" maxLength:"32" doc:"Only this reason"`
	Location string `query:"location" pattern:"^(server|device|node:[1-9][0-9]*)?$" maxLength:"32"`
	Days     int    `query:"days" minimum:"0" maximum:"90" doc:"Only the last N days; 0 for all kept history (90 days)"`
}

type AdminDownloadStorageEventsOutput struct {
	Body Collection[AdminDownloadStorageEvent]
}

// AdminDownloadDevice is one device that holds managed downloads.
type AdminDownloadDevice struct {
	UserID        ID       `json:"user_id"`
	Username      string   `json:"username"`
	ProfileID     ID       `json:"profile_id"`
	ProfileName   string   `json:"profile_name"`
	DeviceID      string   `json:"device_id"`
	DeviceName    string   `json:"device_name"`
	Platform      string   `json:"platform"`
	LastSeenAt    *Instant `json:"last_seen_at,omitempty" doc:"Last registry sync or download request"`
	Stale         bool     `json:"stale" doc:"Not seen within stale_device_days"`
	Copies        int      `json:"copies" minimum:"0" doc:"Downloads that are not revoked"`
	Finished      int      `json:"finished" minimum:"0"`
	Waiting       int      `json:"waiting" minimum:"0" doc:"Preparing, ready or downloading"`
	Failed        int      `json:"failed" minimum:"0"`
	Revoked       int      `json:"revoked" minimum:"0" doc:"Revoked and waiting for the device to delete its copy"`
	BytesOnDevice int64    `json:"bytes_on_device" minimum:"0" doc:"Size of the finished copies"`
	Monitors      int      `json:"monitors" minimum:"0" doc:"Active series monitors"`
}

type AdminDownloadDevicesInput struct {
	LimitParam
	Cursor    string `query:"cursor" maxLength:"8192"`
	Query     string `query:"q" maxLength:"200" doc:"Account, profile or device name"`
	StaleOnly bool   `query:"stale" doc:"Only devices not seen within stale_device_days"`
	Platform  string `query:"platform" maxLength:"64"`
	Sort      string `query:"sort" enum:"last_seen,size" default:"last_seen" doc:"last_seen: longest unseen first. size: most bytes on the device first."`
}

type AdminDownloadDevicesOutput struct {
	Body Collection[AdminDownloadDevice]
}

// AdminDownloadEntry is one managed download across accounts.
type AdminDownloadEntry struct {
	AdminUserDownload
	UserID        ID       `json:"user_id"`
	Username      string   `json:"username"`
	DeviceName    string   `json:"device_name"`
	RevokedAt     *Instant `json:"revoked_at,omitempty"`
	RevokedReason string   `json:"revoked_reason,omitempty"`
	Location      string   `json:"location,omitempty" doc:"Where its prepared file is: server or node:<id>; absent when it has none"`
	LocationName  string   `json:"location_name,omitempty"`
}

type AdminDownloadEntriesInput struct {
	LimitParam
	Cursor    string `query:"cursor" maxLength:"8192"`
	UserID    string `query:"user_id" pattern:"^([1-9][0-9]*)?$" maxLength:"10"`
	ProfileID string `query:"profile_id" maxLength:"1024"`
	DeviceID  string `query:"device_id" maxLength:"128"`
	Status    string `query:"status" maxLength:"32"`
}

type AdminDownloadEntriesOutput struct {
	Body Collection[AdminDownloadEntry]
}

type AdminDownloadRevokeInput struct {
	Body struct {
		IDs           []string `json:"ids,omitempty" maxItems:"500" doc:"Download ids to revoke. Leave empty with user_id, profile_id and device_id to revoke everything on one device."`
		UserID        string   `json:"user_id,omitempty" pattern:"^([1-9][0-9]*)?$" maxLength:"10"`
		ProfileID     string   `json:"profile_id,omitempty" maxLength:"1024"`
		DeviceID      string   `json:"device_id,omitempty" maxLength:"128"`
		PauseMonitors bool     `json:"pause_monitors,omitempty" doc:"Whole-device revoke only: also pause the device's series monitors"`
		Reason        string   `json:"reason,omitempty" maxLength:"500" doc:"Kept in the clean-up history"`
	}
}

type AdminDownloadRevokeOutput struct {
	Body struct {
		Revoked        int      `json:"revoked" minimum:"0" doc:"Downloads newly revoked; already revoked ones are not counted"`
		Bytes          int64    `json:"bytes" minimum:"0" doc:"Size of the revoked copies the device had finished downloading"`
		PausedMonitors int      `json:"paused_monitors" minimum:"0"`
		DownloadIDs    []string `json:"download_ids" doc:"The revoked downloads"`
	}
}

type AdminDownloadStorageCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminDownloadStorageCapabilitiesOutputBody
}

type AdminDownloadStorageCapabilitiesOutputBody struct {
	Capability
	Available       bool   `json:"available"`
	RealtimeChannel string `json:"realtime_channel" doc:"Admin realtime channel carrying download_storage.changed"`
	Devices         bool   `json:"devices" doc:"Device copies can be listed"`
	Revocation      bool   `json:"revocation" doc:"Device copies can be revoked"`
}

func (c AdminDownloadStorageCapabilitiesOutputBody) capabilityState() string {
	return configuredCapabilityState(c.Available)
}

// int32Param parses an optional decimal id the database keeps as int4. The
// schema's pattern admits ten digits, so a larger value is refused here rather
// than failing inside the query.
func int32Param(value, what string) (int, *Problem) {
	if value == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return 0, NewProblem(TypeValidationFailed, "Invalid "+what+".")
	}
	return int(id), nil
}

// storageLocationParam checks an optional location filter: server, node:<id>
// with an id the database can hold, or, where history allows it, device.
func storageLocationParam(location string, allowDevice bool) *Problem {
	if location == "" || (allowDevice && location == downloads.LocationDevice) {
		return nil
	}
	if _, ok := downloads.ParseLocationKey(location); !ok {
		return NewProblem(TypeValidationFailed, "Invalid storage location.")
	}
	return nil
}

func registerAdminDownloadStorage(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	get := func(path, id, summary string) Operation {
		return Operation{Operation: humaOp(http.MethodGet, Prefix+"/admin/downloads"+path, id, "admin", summary), Class: ClassActingAdmin, ServiceBacked: true}
	}
	post := func(path, id, summary string, retry RetrySafety) Operation {
		return Operation{Operation: humaOp(http.MethodPost, Prefix+"/admin/downloads"+path, id, "admin", summary), Class: ClassActingAdmin, ServiceBacked: true, DemoRestricted: true, RetrySafety: retry}
	}
	storage := func() (AdminDownloadStorageService, *Problem) {
		if reg.deps.AdminDownloadStorage == nil {
			return nil, unavailable("download storage")
		}
		return reg.deps.AdminDownloadStorage, nil
	}
	devices := func() (AdminDownloadDeviceService, *Problem) {
		if reg.deps.AdminDownloadDevices == nil {
			return nil, unavailable("download devices")
		}
		return reg.deps.AdminDownloadDevices, nil
	}

	Register(reg, get("/storage/capabilities", "getAdminDownloadStorageCapabilities", "Discover the admin view of prepared-download storage."), func(context.Context, *CapabilityInput) (*AdminDownloadStorageCapabilitiesOutput, error) {
		out := new(AdminDownloadStorageCapabilitiesOutput)
		out.Body.Available = reg.deps.AdminDownloadStorage != nil
		out.Body.RealtimeChannel = string(evt.ChannelDownloadPreparations)
		out.Body.Devices = reg.deps.AdminDownloadDevices != nil
		out.Body.Revocation = out.Body.Devices
		return out, nil
	})

	Register(reg, get("/storage", "getAdminDownloadStorage", "Read prepared-download storage at the server and every transcode node: what is on disk, what Silo's records say, budgets, and files nothing accounts for."), func(ctx context.Context, _ *struct{}) (*AdminDownloadStorageOutput, error) {
		svc, p := storage()
		if p != nil {
			return nil, p
		}
		overview, err := svc.StorageOverview(ctx)
		if err != nil {
			return nil, serviceProblem(err)
		}
		out := &AdminDownloadStorageOutput{Body: AdminDownloadStorage{
			Locations: make([]AdminDownloadStorageLocation, 0, len(overview.Locations)), CacheHours: overview.CacheHours,
			DiskCeilingPercent: overview.DiskCeilingPercent, DefaultBudgetBytes: max(overview.DefaultBudget, 0),
			StaleDeviceDays: overview.StaleDeviceDays, PreparingJobs: overview.PreparingJobs, FreedLast30Days: overview.FreedLast30Days,
		}}
		for _, loc := range overview.Locations {
			out.Body.Locations = append(out.Body.Locations, adminDownloadStorageLocationOf(loc))
		}
		return out, nil
	})

	files := get("/storage/files", "listAdminDownloadStorageFiles", "List prepared download files with what they are, where they are, and who they serve.")
	Register(reg, files, func(ctx context.Context, in *AdminDownloadStorageFilesInput) (*AdminDownloadStorageFilesOutput, error) {
		svc, p := storage()
		if p != nil {
			return nil, p
		}
		if p := storageLocationParam(in.Location, false); p != nil {
			return nil, p
		}
		filter := downloads.StorageFileFilter{Location: in.Location, State: in.State, Format: in.Format, Query: strings.TrimSpace(in.Query), Sort: in.Sort}
		if filter.LibraryID, p = int32Param(in.LibraryID, "library ID"); p != nil {
			return nil, p
		}
		scope := adminScope(ctx, "listAdminDownloadStorageFiles", fmt.Sprintf("%+v/%d", filter, in.Limit), in.Sort)
		var after *downloads.StorageFilePosition
		if in.Cursor != "" {
			after = new(downloads.StorageFilePosition)
			if p := cursors.Decode(scope, in.Cursor, after); p != nil {
				return nil, p
			}
		}
		rows, err := svc.StorageFilesPage(ctx, filter, after, in.Limit+1)
		if err != nil {
			return nil, serviceProblem(err)
		}
		next := ""
		if len(rows) > in.Limit {
			rows = rows[:in.Limit]
			last := rows[len(rows)-1]
			pos := downloads.StorageFilePosition{Bytes: last.Bytes, At: last.LastUsedAt, ID: last.ArtifactID}
			if in.Sort == downloads.StorageSortCreated {
				pos.At = last.CreatedAt
			}
			if next, err = cursors.Encode(scope, pos); err != nil {
				return nil, NewProblem(TypeInternalError, "Unable to encode cursor.")
			}
		}
		items := make([]AdminDownloadStorageFile, 0, len(rows))
		for _, row := range rows {
			items = append(items, adminDownloadStorageFileOf(row))
		}
		return &AdminDownloadStorageFilesOutput{Body: Paginated(items, next)}, nil
	})

	Register(reg, post("/storage/files/delete", "deleteAdminDownloadStorageFiles", "Delete prepared download files. Devices that finished keep their copies. A file a download is waiting on or fetching is refused unless include_in_use is set; then it is deleted and prepared again.", RetrySafetyNaturalIdempotent), func(ctx context.Context, in *AdminDownloadStorageDeleteInput) (*AdminDownloadStorageDeleteOutput, error) {
		svc, p := storage()
		if p != nil {
			return nil, p
		}
		for _, id := range in.Body.IDs {
			if !downloadprepare.ValidArtifactID(id) {
				return nil, NewProblem(TypeValidationFailed, "Invalid prepared file id.")
			}
		}
		results, err := svc.DeleteStorageFiles(ctx, in.Body.IDs, in.Body.IncludeInUse, claimsFrom(ctx).UserID)
		if err != nil {
			return nil, serviceProblem(err)
		}
		out := new(AdminDownloadStorageDeleteOutput)
		out.Body.Results = make([]AdminDownloadStorageDeleteResult, 0, len(results))
		for _, r := range results {
			out.Body.Results = append(out.Body.Results, AdminDownloadStorageDeleteResult{ID: r.ArtifactID, Outcome: r.Outcome, Bytes: max(r.Bytes, 0)})
		}
		return out, nil
	})

	cleanup := post("/storage/locations/{location}/cleanup", "cleanUpAdminDownloadStorageLocation", "Run prepared-file clean-up at one location now: expire files past their cache period and enforce the budget and disk ceiling.", RetrySafetyNaturalIdempotent)
	cleanup.Errors = []int{404}
	Register(reg, cleanup, func(ctx context.Context, in *AdminDownloadStorageLocationInput) (*AdminDownloadStorageCleanupOutput, error) {
		svc, p := storage()
		if p != nil {
			return nil, p
		}
		freed, err := svc.CleanupLocation(ctx, in.Location)
		if err != nil {
			if errors.Is(err, downloads.ErrStorageLocationNotFound) {
				return nil, NewProblem(TypeNotFound, "Storage location not found.")
			}
			return nil, serviceProblem(err)
		}
		out := new(AdminDownloadStorageCleanupOutput)
		out.Body.FreedBytes = max(freed, 0)
		return out, nil
	})

	untracked := post("/storage/locations/{location}/untracked/delete", "deleteAdminDownloadStorageUntrackedFiles", "List one location's prepared-file directory now and delete the files no prepared-file record accounts for and that nothing has written to for an hour. Only files named like Silo's prepared files are considered.", RetrySafetyNaturalIdempotent)
	untracked.Errors = []int{404, 503}
	Register(reg, untracked, func(ctx context.Context, in *AdminDownloadStorageLocationInput) (*AdminDownloadStorageUntrackedOutput, error) {
		svc, p := storage()
		if p != nil {
			return nil, p
		}
		result, err := svc.DeleteUntrackedFiles(ctx, in.Location, claimsFrom(ctx).UserID)
		if err != nil {
			switch {
			case errors.Is(err, downloads.ErrStorageLocationNotFound):
				return nil, NewProblem(TypeNotFound, "Storage location not found.")
			case errors.Is(err, downloads.ErrStorageListingUnavailable):
				return nil, NewProblem(TypeDependencyUnavailable, "The location's directory could not be listed.").WithRetryAfter(30)
			}
			return nil, serviceProblem(err)
		}
		out := new(AdminDownloadStorageUntrackedOutput)
		out.Body.Files, out.Body.Bytes, out.Body.FailedFiles = result.Files, max(result.Bytes, 0), result.Failed
		return out, nil
	})

	Register(reg, get("/storage/events", "listAdminDownloadStorageEvents", "Read the prepared-file clean-up and device revocation history, newest first. A row is one batch (a maintenance pass or an administrator action) at one location for one reason and account."), func(ctx context.Context, in *AdminDownloadStorageEventsInput) (*AdminDownloadStorageEventsOutput, error) {
		svc, p := storage()
		if p != nil {
			return nil, p
		}
		if p := storageLocationParam(in.Location, true); p != nil {
			return nil, p
		}
		filter := downloads.StorageEventFilter{Reason: in.Reason, Location: in.Location}
		if in.Days > 0 {
			since := time.Now().Add(-time.Duration(in.Days) * 24 * time.Hour)
			filter.Since = &since
		}
		scope := adminScope(ctx, "listAdminDownloadStorageEvents", fmt.Sprintf("%q/%q/%d/%d", in.Reason, in.Location, in.Days, in.Limit), "occurred_at:desc")
		var after *downloads.StorageEventPosition
		if in.Cursor != "" {
			after = new(downloads.StorageEventPosition)
			if p := cursors.Decode(scope, in.Cursor, after); p != nil {
				return nil, p
			}
		}
		rows, err := svc.StorageEventsPage(ctx, filter, after, in.Limit+1)
		if err != nil {
			return nil, serviceProblem(err)
		}
		next := ""
		if len(rows) > in.Limit {
			rows = rows[:in.Limit]
			last := rows[len(rows)-1]
			if next, err = cursors.Encode(scope, downloads.StorageEventPosition{At: last.OccurredAt, BatchID: last.BatchID}); err != nil {
				return nil, NewProblem(TypeInternalError, "Unable to encode cursor.")
			}
		}
		items := make([]AdminDownloadStorageEvent, 0, len(rows))
		for _, row := range rows {
			items = append(items, adminDownloadStorageEventOf(row))
		}
		return &AdminDownloadStorageEventsOutput{Body: Paginated(items, next)}, nil
	})

	Register(reg, get("/devices", "listAdminDownloadDevices", "List every device that holds managed downloads, across accounts."), func(ctx context.Context, in *AdminDownloadDevicesInput) (*AdminDownloadDevicesOutput, error) {
		svc, p := devices()
		if p != nil {
			return nil, p
		}
		filter := downloads.AdminDeviceFilter{Query: strings.TrimSpace(in.Query), StaleOnly: in.StaleOnly, Platform: strings.TrimSpace(in.Platform), Sort: in.Sort}
		scope := adminScope(ctx, "listAdminDownloadDevices", fmt.Sprintf("%q/%t/%q/%d", filter.Query, filter.StaleOnly, filter.Platform, in.Limit), in.Sort)
		var after *downloads.AdminDevicePosition
		if in.Cursor != "" {
			after = new(downloads.AdminDevicePosition)
			if p := cursors.Decode(scope, in.Cursor, after); p != nil {
				return nil, p
			}
		}
		rows, err := svc.AdminListDevicesPage(ctx, filter, after, in.Limit+1)
		if err != nil {
			return nil, serviceProblem(err)
		}
		next := ""
		if len(rows) > in.Limit {
			rows = rows[:in.Limit]
			last := rows[len(rows)-1]
			pos := downloads.AdminDevicePosition{Bytes: last.BytesOnDevice, UserID: last.UserID, ProfileID: last.ProfileID, DeviceID: last.DeviceID}
			if last.LastSeenAt != nil {
				pos.LastSeen = *last.LastSeenAt
			}
			if next, err = cursors.Encode(scope, pos); err != nil {
				return nil, NewProblem(TypeInternalError, "Unable to encode cursor.")
			}
		}
		items := make([]AdminDownloadDevice, 0, len(rows))
		for _, row := range rows {
			items = append(items, adminDownloadDeviceOf(row))
		}
		return &AdminDownloadDevicesOutput{Body: Paginated(items, next)}, nil
	})

	Register(reg, get("/entries", "listAdminDownloadEntries", "List managed device downloads across accounts, newest first, optionally for one account, profile, device or status."), func(ctx context.Context, in *AdminDownloadEntriesInput) (*AdminDownloadEntriesOutput, error) {
		svc, p := devices()
		if p != nil {
			return nil, p
		}
		filter := downloads.AdminEntryFilter{ProfileID: strings.TrimSpace(in.ProfileID), DeviceID: strings.TrimSpace(in.DeviceID), Status: strings.TrimSpace(in.Status)}
		if filter.UserID, p = int32Param(in.UserID, "account ID"); p != nil {
			return nil, p
		}
		scope := adminScope(ctx, "listAdminDownloadEntries", fmt.Sprintf("%d/%q/%q/%q/%d", filter.UserID, filter.ProfileID, filter.DeviceID, filter.Status, in.Limit), adminUserDownloadsSort)
		var after *downloads.RegistryPosition
		if in.Cursor != "" {
			after = new(downloads.RegistryPosition)
			if p := cursors.Decode(scope, in.Cursor, after); p != nil {
				return nil, p
			}
		}
		rows, err := svc.AdminListEntriesPage(ctx, filter, after, in.Limit+1)
		if err != nil {
			return nil, downloadProblem(err)
		}
		next := ""
		if len(rows) > in.Limit {
			rows = rows[:in.Limit]
			last := rows[len(rows)-1]
			if next, err = cursors.Encode(scope, downloads.RegistryPosition{CreatedAt: last.CreatedAt, ID: last.ID}); err != nil {
				return nil, NewProblem(TypeInternalError, "Unable to encode cursor.")
			}
		}
		items := make([]AdminDownloadEntry, 0, len(rows))
		for _, row := range rows {
			items = append(items, adminDownloadEntryOf(row))
		}
		return &AdminDownloadEntriesOutput{Body: Paginated(items, next)}, nil
	})

	Register(reg, post("/revoke", "revokeAdminDownloads", "Revoke device downloads. The Silo app deletes each copy the next time the device syncs and confirms by deleting the download. The server stops serving the file at once. A revoked episode is excluded from its series monitor; a preparation only revoked downloads waited on is canceled.", RetrySafetyNaturalIdempotent), func(ctx context.Context, in *AdminDownloadRevokeInput) (*AdminDownloadRevokeOutput, error) {
		svc, p := devices()
		if p != nil {
			return nil, p
		}
		req := downloads.RevokeRequest{IDs: in.Body.IDs, ProfileID: strings.TrimSpace(in.Body.ProfileID), DeviceID: strings.TrimSpace(in.Body.DeviceID),
			PauseMonitors: in.Body.PauseMonitors, Reason: in.Body.Reason, Actor: claimsFrom(ctx).UserID}
		if req.UserID, p = int32Param(in.Body.UserID, "account ID"); p != nil {
			return nil, p
		}
		if len(req.IDs) > 0 && (req.UserID != 0 || req.ProfileID != "" || req.DeviceID != "") {
			return nil, NewProblem(TypeValidationFailed, "Send download ids or one device, not both.")
		}
		result, err := svc.RevokeDeviceDownloads(ctx, req)
		if err != nil {
			if errors.Is(err, downloads.ErrInvalidRevoke) {
				return nil, NewProblem(TypeValidationFailed, "Send download ids, or user_id, profile_id and device_id.")
			}
			return nil, downloadProblem(err)
		}
		out := new(AdminDownloadRevokeOutput)
		out.Body.Revoked, out.Body.Bytes, out.Body.PausedMonitors = len(result.Revoked), max(result.Bytes, 0), result.PausedMonitors
		out.Body.DownloadIDs = make([]string, 0, len(result.Revoked))
		for _, d := range result.Revoked {
			out.Body.DownloadIDs = append(out.Body.DownloadIDs, d.ID)
		}
		return out, nil
	})
}

// adminScope binds a cursor to the operation, the acting administrator, the
// filter and the order.
func adminScope(ctx context.Context, operation, filter, sort string) CursorScope {
	return CursorScope{
		OperationID: operation,
		Security:    strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx),
		Filter:      filter,
		Sort:        sort,
		Tiebreaker:  "id",
	}
}

func adminDownloadStorageUsageOf(u *downloadstorage.Usage) *AdminDownloadStorageUsage {
	if u == nil {
		return nil
	}
	return &AdminDownloadStorageUsage{
		MeasuredAt: NewInstant(u.MeasuredAt), Files: max(u.Files, 0), Bytes: max(u.Bytes, 0),
		PartialFiles: max(u.PartialFiles, 0), PartialBytes: max(u.PartialBytes, 0), OtherBytes: max(u.OtherBytes, 0),
		FSUsedBytes: max(u.FSUsedBytes, 0), FSTotalBytes: max(u.FSTotalBytes, 0), FSType: u.FSType,
		SharesScratch: u.SharesScratch, Ephemeral: u.Ephemeral, Stale: u.Stale, Error: u.Error,
	}
}

func adminDownloadStorageLocationOf(v downloads.StorageLocationView) AdminDownloadStorageLocation {
	out := AdminDownloadStorageLocation{
		Key: v.Key, Kind: "server", Name: v.Name, Enabled: v.Enabled, Online: v.Online,
		LastHealthCheck: instantPtr(v.LastHealthCheck), Dir: v.Dir, DirSource: v.DirSource, PendingDir: v.PendingDir,
		Usage:      adminDownloadStorageUsageOf(v.Usage),
		InUseFiles: v.InUseFiles, InUseBytes: max(v.InUseBytes, 0), CachedFiles: v.CachedFiles, CachedBytes: max(v.CachedBytes, 0),
		WaitingDownloads: v.WaitingDownloads, StaleWaitingBytes: max(v.StaleWaitingBytes, 0),
		UntrackedFiles: v.UntrackedFiles, UntrackedBytes: max(v.UntrackedBytes, 0), ReconciledAt: instantPtr(v.ReconciledAt),
		BudgetBytes: max(v.Budget, 0), BudgetSource: v.BudgetSource, CleanupBacklog: v.CleanupBacklog,
		StorageFull: v.StorageFull, ReplicasDisagree: v.ReplicasDisagree,
	}
	if v.NodeID > 0 {
		out.Kind = "node"
		out.NodeID = new(IDFromInt(int64(v.NodeID)))
	}
	return out
}

func adminDownloadStorageFileOf(f downloads.StorageFile) AdminDownloadStorageFile {
	out := AdminDownloadStorageFile{
		ID: ID(f.ArtifactID), State: f.State, Format: f.Format, Location: f.Location, LocationName: f.LocationName,
		MediaFileID: IDFromInt(int64(f.MediaFileID)), ContentID: f.ContentID, EpisodeID: f.EpisodeID,
		Title: f.Title, MediaType: f.MediaType, Year: f.Year, SeasonNumber: f.SeasonNumber, EpisodeNumber: f.EpisodeNum, EpisodeTitle: f.EpisodeTitle,
		Container: f.Container, VideoCodec: f.VideoCodec, AudioCodec: f.AudioCodec, Resolution: f.Resolution, BitrateKbps: positiveInt(f.BitrateKbps),
		Bytes: max(f.Bytes, 0), CreatedAt: NewInstant(f.CreatedAt), LastUsedAt: NewInstant(f.LastUsedAt), ExpiresAt: instantPtr(f.ExpiresAt),
		Waiting: f.Waiting, Downloading: f.Downloading, Finished: f.Finished, StaleWaiting: f.StaleWaiting,
	}
	if f.LibraryID > 0 {
		out.LibraryID = new(IDFromInt(int64(f.LibraryID)))
	}
	if w := f.OldestWaiting; w != nil {
		out.OldestWaiting = &AdminDownloadWaitingDevice{DeviceName: w.DeviceName, Username: w.Username, LastSeenAt: instantPtr(w.LastSeenAt)}
	}
	return out
}

func adminDownloadStorageEventOf(b downloads.StorageEventBatch) AdminDownloadStorageEvent {
	out := AdminDownloadStorageEvent{
		ID: b.BatchID, Reason: b.Reason, Location: b.Location, LocationName: b.LocationName, OccurredAt: NewInstant(b.OccurredAt),
		Count: b.Count, Bytes: max(b.Bytes, 0), Titles: NonNil(b.Titles), Detail: b.Detail,
	}
	if b.ActorUserID != nil {
		out.Actor = &AdminDownloadStorageUser{ID: IDFromInt(int64(*b.ActorUserID)), Username: b.ActorName}
	}
	if b.AccountUserID != nil {
		out.Account = &AdminDownloadStorageUser{ID: IDFromInt(int64(*b.AccountUserID)), Username: b.AccountName}
	}
	return out
}

func adminDownloadDeviceOf(r downloads.AdminDeviceRow) AdminDownloadDevice {
	return AdminDownloadDevice{
		UserID: IDFromInt(int64(r.UserID)), Username: r.Username, ProfileID: ID(r.ProfileID), ProfileName: r.ProfileName,
		DeviceID: r.DeviceID, DeviceName: r.DeviceName, Platform: r.Platform, LastSeenAt: instantPtr(r.LastSeenAt), Stale: r.Stale,
		Copies: r.Copies, Finished: r.Finished, Waiting: r.Waiting, Failed: r.Failed, Revoked: r.Revoked,
		BytesOnDevice: max(r.BytesOnDevice, 0), Monitors: r.Monitors,
	}
}

func adminDownloadEntryOf(r downloads.AdminEntryRow) AdminDownloadEntry {
	return AdminDownloadEntry{
		AdminUserDownload: adminUserDownloadOf(r.AdminDownloadRow),
		UserID:            IDFromInt(int64(r.UserID)), Username: r.Username, DeviceName: r.DeviceName,
		RevokedAt: instantPtr(r.RevokedAt), RevokedReason: r.RevokedReason, Location: r.Location, LocationName: r.LocationName,
	}
}
