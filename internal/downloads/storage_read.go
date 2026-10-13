package downloads

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/downloadstorage"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

// Where a location's directory or budget comes from.
const (
	StorageSourceDefault  = "default"  // the built-in default (no setting)
	StorageSourceSetting  = "setting"  // the cluster download.* setting
	StorageSourceOverride = "override" // this node's own override
	StorageSourceNone     = "none"     // no budget
)

// StorageLocationView is one location as the admin storage view shows it.
type StorageLocationView struct {
	Key     string
	Name    string
	NodeID  int // 0 for the API server
	Enabled bool
	Online  bool
	// LastHealthCheck is when the API last heard from a node.
	LastHealthCheck *time.Time
	// Dir is the directory, when known: always for the server, and for a node
	// the one it was last listed in, or before its first listing the
	// configured one. DirSource says where the configured directory comes
	// from. PendingDir is a configured node directory that differs from the
	// listed one; the node moves to it when it restarts.
	Dir        string
	DirSource  string
	PendingDir string
	// Usage is the latest measurement, nil until one has been reported.
	Usage *downloadstorage.Usage
	// Bytes and files Silo's records hold here: in use (a download is waiting
	// on or fetching the file) and cached (nothing is).
	InUseFiles  int
	InUseBytes  int64
	CachedFiles int
	CachedBytes int64
	// WaitingDownloads counts downloads waiting on or fetching files here;
	// StaleWaitingBytes is in-use bytes kept only for devices not seen recently.
	WaitingDownloads  int
	StaleWaitingBytes int64
	UntrackedFiles    int
	UntrackedBytes    int64
	ReconciledAt      *time.Time
	Budget            int64
	BudgetSource      string
	// CleanupBacklog counts node files queued for deletion and not yet deleted.
	CleanupBacklog int
	// StorageFull is set when the location is over its budget or the disk
	// ceiling with nothing left to free, so placement skips it.
	StorageFull bool
	// ReplicasDisagree is set on the server when API replicas report different
	// directories or filesystems: they are meant to share one volume.
	ReplicasDisagree bool
}

// IndexedBytes is what Silo's records say the location holds.
func (v StorageLocationView) IndexedBytes() int64 { return v.InUseBytes + v.CachedBytes }

// StorageOverview is the server-wide storage summary.
type StorageOverview struct {
	Locations          []StorageLocationView
	CacheHours         int
	DiskCeilingPercent int
	DefaultBudget      int64
	StaleDeviceDays    int
	PreparingJobs      int
	FreedLast30Days    int64
}

type locationTotals struct {
	inUseFiles, cachedFiles, waiting int
	inUseBytes, cachedBytes, stale   int64
}

// StorageOverview reads every location's records, measurements, and settings.
func (m *ArtifactManager) StorageOverview(ctx context.Context) (*StorageOverview, error) {
	if m == nil || m.repo == nil {
		return nil, ErrFormatUnavailable
	}
	cfg := m.downloadConfig()
	out := &StorageOverview{
		CacheHours:         m.cacheHours(),
		DiskCeilingPercent: m.diskCeilingPercent(),
		DefaultBudget:      cfg.ArtifactMaxBytes,
		StaleDeviceDays:    int(staleDeviceAge / (24 * time.Hour)),
	}
	totals, err := m.repo.storageTotalsByLocation(ctx, staleDeviceAge)
	if err != nil {
		return nil, err
	}
	samples, err := m.repo.storageSamples(ctx)
	if err != nil {
		return nil, err
	}
	backlog, err := m.repo.cleanupBacklogByNode(ctx)
	if err != nil {
		return nil, err
	}
	if err := m.repo.pool.QueryRow(ctx,
		`SELECT
			(SELECT count(*) FROM download_artifacts WHERE status IN ('queued', 'running', 'tone_map_queued', 'tone_map_running',
				'audio_v2_queued', 'audio_v2_running', 'tracks_v1_queued', 'tracks_v1_running')),
			(SELECT COALESCE(SUM(bytes), 0)::bigint FROM download_storage_events
			 WHERE occurred_at > now() - interval '30 days' AND location_key <> 'device'
			   -- A missing file's bytes were already gone; nothing freed them.
			   AND reason <> 'missing')`,
	).Scan(&out.PreparingJobs, &out.FreedLast30Days); err != nil {
		return nil, fmt.Errorf("reading storage activity: %w", err)
	}

	server := StorageLocationView{Key: LocationServer, Name: serverLocationName, Enabled: true, Online: true, Dir: m.artifactDir(), DirSource: StorageSourceDefault,
		StorageFull: m.NodeStorageFull(0)}
	if strings.TrimSpace(cfg.ArtifactDir) != "" {
		server.DirSource = StorageSourceSetting
	}
	server.Budget, server.BudgetSource = budgetView(cfg.ArtifactMaxBytes, StorageSourceSetting)
	applyTotals(&server, totals[0])
	applyServerSamples(&server, samples[0])
	if usage, ok := m.ServerStorageUsage(); ok && (server.Usage == nil || usage.MeasuredAt.After(server.Usage.MeasuredAt)) {
		server.Usage = &usage
	}
	out.Locations = append(out.Locations, server)

	var nodes []*nodepool.Node
	if source := m.nodeSource(); source != nil {
		if nodes, err = source.List(ctx); err != nil {
			return nil, err
		}
	}
	for _, n := range nodes {
		t := totals[n.ID]
		if n.Type != nodepool.NodeTypeTranscode && t.inUseFiles+t.cachedFiles == 0 {
			continue
		}
		v := StorageLocationView{
			Key: NodeLocationKey(n.ID), Name: n.Name, NodeID: n.ID,
			Enabled: n.Enabled, Online: n.Enabled && n.Healthy, LastHealthCheck: n.LastHealthCheck,
			CleanupBacklog: backlog[n.ID], StorageFull: m.NodeStorageFull(n.ID),
		}
		var configured string
		switch {
		case n.DownloadArtifactDirOverride != nil:
			configured, v.DirSource = *n.DownloadArtifactDirOverride, StorageSourceOverride
		case strings.TrimSpace(cfg.ArtifactDir) != "":
			configured, v.DirSource = cfg.ArtifactDir, StorageSourceSetting
		default:
			v.DirSource = StorageSourceDefault
		}
		v.Dir = configured
		if n.DownloadArtifactMaxBytesOverride != nil {
			v.Budget, v.BudgetSource = budgetView(*n.DownloadArtifactMaxBytesOverride, StorageSourceOverride)
		} else {
			v.Budget, v.BudgetSource = budgetView(cfg.ArtifactMaxBytes, StorageSourceSetting)
		}
		applyTotals(&v, t)
		if rows := samples[n.ID]; len(rows) > 0 {
			sample := rows[0]
			usage := sample.usage
			v.Usage, v.UntrackedFiles, v.UntrackedBytes, v.ReconciledAt = &usage, sample.untrackedFiles, sample.untrackedBytes, sample.reconciledAt
			// A node fixes its directory at startup: the listing names the one
			// it uses, which can still be the old one after an edit.
			if usage.Dir != "" {
				if configured != "" && configured != usage.Dir {
					v.PendingDir = configured
				}
				v.Dir = usage.Dir
			}
		}
		// The health check carries a fresher measurement than the listing.
		// Health is path-free, so the listing's directory is kept.
		if live := nodeArtifactUsage(n); live != nil && (v.Usage == nil || live.MeasuredAt.After(v.Usage.MeasuredAt)) {
			if v.Usage != nil && live.Dir == "" {
				live.Dir = v.Usage.Dir
			}
			v.Usage = live
		}
		out.Locations = append(out.Locations, v)
	}
	return out, nil
}

func budgetView(bytes int64, source string) (int64, string) {
	if bytes <= 0 {
		return 0, StorageSourceNone
	}
	return bytes, source
}

func applyTotals(v *StorageLocationView, t locationTotals) {
	v.InUseFiles, v.InUseBytes = t.inUseFiles, t.inUseBytes
	v.CachedFiles, v.CachedBytes = t.cachedFiles, t.cachedBytes
	v.WaitingDownloads, v.StaleWaitingBytes = t.waiting, t.stale
}

// applyServerSamples picks the freshest replica report for the server and
// flags replicas that disagree about the volume they share.
func applyServerSamples(v *StorageLocationView, rows []storageSample) {
	if len(rows) == 0 {
		return
	}
	latest := rows[0]
	for _, r := range rows[1:] {
		if r.usage.MeasuredAt.After(latest.usage.MeasuredAt) {
			latest = r
		}
	}
	usage := latest.usage
	v.Usage = &usage
	for _, r := range rows {
		if r.reconciledAt != nil && (v.ReconciledAt == nil || r.reconciledAt.After(*v.ReconciledAt)) {
			v.UntrackedFiles, v.UntrackedBytes, v.ReconciledAt = r.untrackedFiles, r.untrackedBytes, r.reconciledAt
		}
		recent := time.Since(r.updatedAt) < 2*storageReconcileInterval
		if recent && r.reporter != latest.reporter && (r.usage.Dir != latest.usage.Dir || r.usage.FSTotalBytes != latest.usage.FSTotalBytes) {
			v.ReplicasDisagree = true
		}
	}
}

type storageSample struct {
	reporter       string
	usage          downloadstorage.Usage
	untrackedFiles int
	untrackedBytes int64
	reconciledAt   *time.Time
	updatedAt      time.Time
}

// storageSamples returns stored samples by node id (0 is the server), newest first.
func (r *ArtifactRepository) storageSamples(ctx context.Context) (map[int][]storageSample, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT COALESCE(node_id, 0), reporter, usage, untracked_files, untracked_bytes, reconciled_at, updated_at
		 FROM download_storage_samples ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("reading storage samples: %w", err)
	}
	defer rows.Close()
	out := make(map[int][]storageSample)
	for rows.Next() {
		var nodeID int
		var raw []byte
		var s storageSample
		if err := rows.Scan(&nodeID, &s.reporter, &raw, &s.untrackedFiles, &s.untrackedBytes, &s.reconciledAt, &s.updatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &s.usage); err != nil {
			continue
		}
		out[nodeID] = append(out[nodeID], s)
	}
	return out, rows.Err()
}

// storageTotalsByLocation sums ready files per location, split into in use and
// cached, and the in-use bytes kept only for devices not seen within stale.
func (r *ArtifactRepository) storageTotalsByLocation(ctx context.Context, stale time.Duration) (map[int]locationTotals, error) {
	rows, err := r.pool.Query(ctx,
		`WITH files AS (
			SELECT a.origin_node_id, a.file_size,
			       (SELECT count(*) FROM downloads d WHERE d.artifact_id = a.id AND `+inFlightLinkPredicate+`) AS waiting,
			       EXISTS (SELECT 1 FROM downloads d
			               LEFT JOIN user_devices u ON u.user_id = d.user_id AND u.profile_id = d.profile_id AND u.device_id = d.device_id
			               WHERE d.artifact_id = a.id AND `+inFlightLinkPredicate+`
			                 AND (d.device_id IS NULL OR u.last_seen_at >= now() - make_interval(secs => $1))) AS fresh_waiter
			FROM download_artifacts a
			WHERE a.status IN ('ready', 'tone_map_ready', 'audio_v2_ready', 'tracks_v1_ready')
		 )
		 SELECT origin_node_id,
		        count(*) FILTER (WHERE waiting > 0), COALESCE(SUM(file_size) FILTER (WHERE waiting > 0), 0)::bigint,
		        count(*) FILTER (WHERE waiting = 0), COALESCE(SUM(file_size) FILTER (WHERE waiting = 0), 0)::bigint,
		        COALESCE(SUM(waiting), 0)::int,
		        COALESCE(SUM(file_size) FILTER (WHERE waiting > 0 AND NOT fresh_waiter), 0)::bigint
		 FROM files GROUP BY origin_node_id`, stale.Seconds())
	if err != nil {
		return nil, fmt.Errorf("summing storage by location: %w", err)
	}
	defer rows.Close()
	out := make(map[int]locationTotals)
	for rows.Next() {
		var nodeID int
		var t locationTotals
		if err := rows.Scan(&nodeID, &t.inUseFiles, &t.inUseBytes, &t.cachedFiles, &t.cachedBytes, &t.waiting, &t.stale); err != nil {
			return nil, err
		}
		out[nodeID] = t
	}
	return out, rows.Err()
}

func (r *ArtifactRepository) cleanupBacklogByNode(ctx context.Context) (map[int]int, error) {
	rows, err := r.pool.Query(ctx, `SELECT origin_node_id, count(*)::int FROM download_artifact_orphans GROUP BY origin_node_id`)
	if err != nil {
		return nil, fmt.Errorf("counting node clean-up backlog: %w", err)
	}
	defer rows.Close()
	out := make(map[int]int)
	for rows.Next() {
		var nodeID, n int
		if err := rows.Scan(&nodeID, &n); err != nil {
			return nil, err
		}
		out[nodeID] = n
	}
	return out, rows.Err()
}

// Prepared-file states in the admin listing.
const (
	StorageFileInUse   = "in_use"
	StorageFileCached  = "cached"
	StorageFileExpired = "expired"
)

// Prepared-file listing orders.
const (
	StorageSortSize     = "size"      // largest first
	StorageSortLastUsed = "last_used" // least recently used first
	StorageSortCreated  = "created"   // newest first
)

// StorageFileFilter narrows the prepared-file listing. Empty fields do not filter.
type StorageFileFilter struct {
	Location  string
	State     string
	Format    string
	LibraryID int
	Query     string
	Sort      string
}

// StorageFilePosition is a keyset position in the prepared-file listing.
type StorageFilePosition struct {
	Bytes int64
	At    time.Time
	ID    string
}

// StorageFile is one prepared file with what it is and who it serves.
type StorageFile struct {
	ArtifactID   string
	State        string
	Format       string
	Location     string
	LocationName string
	MediaFileID  int
	LibraryID    int
	ContentID    string
	EpisodeID    string
	Title        string
	MediaType    string
	Year         *int
	SeasonNumber *int
	EpisodeNum   *int
	EpisodeTitle string
	Container    string
	VideoCodec   string
	AudioCodec   string
	Resolution   string
	BitrateKbps  int
	Bytes        int64
	CreatedAt    time.Time
	LastUsedAt   time.Time
	// ExpiresAt is when a cached file's cache period ends.
	ExpiresAt *time.Time
	// Download rows linked to the file, by state.
	Waiting, Downloading, Finished int
	// StaleWaiting counts waiting downloads on devices not seen recently;
	// OldestWaiting describes the longest-unseen of them.
	StaleWaiting  int
	OldestWaiting *WaitingDevice
}

// WaitingDevice names a device a prepared file is waiting on.
type WaitingDevice struct {
	DeviceName string
	Username   string
	LastSeenAt *time.Time
}

// storageFileLocationSQL is the location a prepared file (alias a) is listed
// under: where it is, or for an expired file, where it was.
const storageFileLocationSQL = `CASE WHEN a.status = 'expired' THEN COALESCE(a.expired_from_node_id, 0) ELSE a.origin_node_id END`

// StorageFilesPage lists prepared files at every location (or one), one keyset
// page at a time. It returns limit rows at most; callers ask for one more than
// they show to learn whether more follow.
func (m *ArtifactManager) StorageFilesPage(ctx context.Context, f StorageFileFilter, after *StorageFilePosition, limit int) ([]StorageFile, error) {
	if m == nil || m.repo == nil {
		return nil, ErrFormatUnavailable
	}
	if limit < 1 || limit > adminPageMaxLimit {
		return nil, fmt.Errorf("storage file page limit must be 1 to %d", adminPageMaxLimit)
	}
	nodeID := -1
	if f.Location != "" {
		id, ok := ParseLocationKey(f.Location)
		if !ok {
			return nil, fmt.Errorf("unknown storage location %q", f.Location)
		}
		nodeID = id
	}
	statuses := `('ready', 'tone_map_ready', 'audio_v2_ready', 'tracks_v1_ready')`
	if f.State == StorageFileExpired {
		statuses = `('expired')`
	}
	// Each order reads one cursor value; the other is always NULL there, and
	// naming it keeps every parameter typed.
	order, cursor := "a.file_size DESC, a.id DESC", "$9::timestamptz IS NULL AND ($8::bigint IS NULL OR (a.file_size, a.id) < ($8, $10))"
	switch f.Sort {
	case StorageSortLastUsed:
		order, cursor = "a.last_used_at ASC, a.id ASC", "$8::bigint IS NULL AND ($9::timestamptz IS NULL OR (a.last_used_at, a.id) > ($9, $10))"
	case StorageSortCreated:
		order, cursor = "a.created_at DESC, a.id DESC", "$8::bigint IS NULL AND ($9::timestamptz IS NULL OR (a.created_at, a.id) < ($9, $10))"
	}
	var afterBytes *int64
	var afterAt *time.Time
	afterID := ""
	if after != nil {
		afterID = after.ID
		if f.Sort == StorageSortLastUsed || f.Sort == StorageSortCreated {
			afterAt = &after.At
		} else {
			afterBytes = &after.Bytes
		}
	}
	query := `WITH links AS (
		SELECT d.artifact_id,
		       count(*) FILTER (WHERE d.status IN ('queued', 'preparing', 'ready')) AS waiting,
		       count(*) FILTER (WHERE d.status = 'downloading') AS downloading,
		       count(*) FILTER (WHERE d.status = 'completed') AS finished,
		       count(*) FILTER (WHERE d.status IN ('queued', 'preparing', 'ready') AND d.device_id IS NOT NULL
		                          AND (u.last_seen_at IS NULL OR u.last_seen_at < now() - make_interval(secs => $7))) AS stale_waiting
		FROM downloads d
		LEFT JOIN user_devices u ON u.user_id = d.user_id AND u.profile_id = d.profile_id AND u.device_id = d.device_id
		WHERE d.artifact_id IS NOT NULL
		GROUP BY d.artifact_id
	)
	SELECT a.id, a.status, a.format, ` + storageFileLocationSQL + `, COALESCE(n.name, ''),
	       a.media_file_id, COALESCE(f.media_folder_id, 0), COALESCE(f.content_id, ''), COALESCE(f.episode_id, ''),
	       COALESCE(mi.title, ''), COALESCE(mi.type, ''), mi.year, ep.season_number, ep.episode_number, COALESCE(ep.title, ''),
	       a.container, a.codec_video, a.codec_audio, a.resolution, a.target_bitrate_kbps, a.file_size,
	       a.created_at, a.last_used_at,
	       COALESCE(l.waiting, 0), COALESCE(l.downloading, 0), COALESCE(l.finished, 0), COALESCE(l.stale_waiting, 0)
	FROM download_artifacts a
	LEFT JOIN links l ON l.artifact_id = a.id
	LEFT JOIN stream_nodes n ON n.id = ` + storageFileLocationSQL + `
	LEFT JOIN media_files f ON f.id = a.media_file_id
	LEFT JOIN episodes ep ON ep.content_id = f.episode_id
	LEFT JOIN media_items mi ON mi.content_id = COALESCE(ep.series_id, f.content_id)
	WHERE a.status IN ` + statuses + `
	  AND ($1::int < 0 OR ` + storageFileLocationSQL + ` = $1)
	  AND ($2 = '' OR ($2 = 'in_use') = (COALESCE(l.waiting, 0) + COALESCE(l.downloading, 0) > 0) OR $2 = 'expired')
	  AND ($3 = '' OR a.format = $3)
	  AND ($4 = 0 OR f.media_folder_id = $4)
	  AND ($5 = '' OR mi.title ILIKE '%' || $5 || '%' OR ep.title ILIKE '%' || $5 || '%')
	  AND ` + cursor + `
	ORDER BY ` + order + `
	LIMIT $6`
	state := f.State
	if state != StorageFileInUse && state != StorageFileCached && state != StorageFileExpired {
		state = ""
	}
	rows, err := m.repo.pool.Query(ctx, query,
		nodeID, state, f.Format, f.LibraryID, escapeLike(strings.TrimSpace(f.Query)), limit, staleDeviceAge.Seconds(),
		afterBytes, afterAt, afterID)
	if err != nil {
		return nil, fmt.Errorf("listing prepared files: %w", err)
	}
	defer rows.Close()
	ttl := m.cacheTTL()
	var out []StorageFile
	for rows.Next() {
		var sf StorageFile
		var status string
		var nodeID int
		if err := rows.Scan(&sf.ArtifactID, &status, &sf.Format, &nodeID, &sf.LocationName,
			&sf.MediaFileID, &sf.LibraryID, &sf.ContentID, &sf.EpisodeID,
			&sf.Title, &sf.MediaType, &sf.Year, &sf.SeasonNumber, &sf.EpisodeNum, &sf.EpisodeTitle,
			&sf.Container, &sf.VideoCodec, &sf.AudioCodec, &sf.Resolution, &sf.BitrateKbps, &sf.Bytes,
			&sf.CreatedAt, &sf.LastUsedAt,
			&sf.Waiting, &sf.Downloading, &sf.Finished, &sf.StaleWaiting); err != nil {
			return nil, fmt.Errorf("scanning prepared file: %w", err)
		}
		sf.Location = locationKeyForNode(nodeID)
		if nodeID == 0 {
			sf.LocationName = serverLocationName
		}
		switch {
		case status == ArtifactExpired:
			sf.State = StorageFileExpired
		case sf.Waiting+sf.Downloading > 0:
			sf.State = StorageFileInUse
		default:
			sf.State = StorageFileCached
			expires := sf.LastUsedAt.Add(ttl)
			sf.ExpiresAt = &expires
		}
		out = append(out, sf)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, m.repo.attachOldestWaiting(ctx, out)
}

// attachOldestWaiting names the longest-unseen device each file with stale
// waiting downloads is kept for.
func (r *ArtifactRepository) attachOldestWaiting(ctx context.Context, files []StorageFile) error {
	ids := make([]string, 0)
	index := make(map[string]int)
	for i, f := range files {
		if f.StaleWaiting > 0 {
			ids = append(ids, f.ArtifactID)
			index[f.ArtifactID] = i
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := r.pool.Query(ctx,
		`SELECT DISTINCT ON (d.artifact_id) d.artifact_id, COALESCE(u.device_name, ''), COALESCE(us.username, ''), u.last_seen_at
		 FROM downloads d
		 LEFT JOIN user_devices u ON u.user_id = d.user_id AND u.profile_id = d.profile_id AND u.device_id = d.device_id
		 LEFT JOIN users us ON us.id = d.user_id
		 WHERE d.artifact_id = ANY($1) AND d.device_id IS NOT NULL AND d.status IN ('queued', 'preparing', 'ready')
		 ORDER BY d.artifact_id, u.last_seen_at ASC NULLS FIRST`, ids)
	if err != nil {
		return fmt.Errorf("reading waiting devices: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var w WaitingDevice
		if err := rows.Scan(&id, &w.DeviceName, &w.Username, &w.LastSeenAt); err != nil {
			return err
		}
		if i, ok := index[id]; ok {
			files[i].OldestWaiting = &w
		}
	}
	return rows.Err()
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// StorageEventFilter narrows the clean-up history. Empty fields do not filter.
type StorageEventFilter struct {
	Reason   string
	Location string
	Since    *time.Time
}

// StorageEventPosition is a keyset position in the history.
type StorageEventPosition struct {
	At      time.Time
	BatchID string
}

// StorageEventBatch is one clean-up pass or administrator action.
type StorageEventBatch struct {
	BatchID      string
	Reason       string
	Location     string
	LocationName string
	OccurredAt   time.Time
	Count        int
	Bytes        int64
	// Titles are the first few catalog titles the batch touched.
	Titles      []string
	Detail      string
	ActorUserID *int
	ActorName   string
	// Account is the person whose device a revocation targeted.
	AccountUserID *int
	AccountName   string
}

// StorageEventsPage lists clean-up history newest first. A row is one batch
// (a pass or an administrator action) at one location for one reason and
// account, so a batch that spans several is never shown under just one.
func (m *ArtifactManager) StorageEventsPage(ctx context.Context, f StorageEventFilter, after *StorageEventPosition, limit int) ([]StorageEventBatch, error) {
	if m == nil || m.repo == nil {
		return nil, ErrFormatUnavailable
	}
	if limit < 1 || limit > adminPageMaxLimit {
		return nil, fmt.Errorf("storage event page limit must be 1 to %d", adminPageMaxLimit)
	}
	var afterAt *time.Time
	afterID := ""
	if after != nil {
		afterAt, afterID = &after.At, after.BatchID
	}
	rows, err := m.repo.pool.Query(ctx,
		`WITH keyed AS (
		     SELECT e.*, e.batch_id || '|' || e.reason || '|' || e.location_key || '|' || COALESCE(e.user_id::text, '') AS group_key
		     FROM download_storage_events e
		     WHERE ($1 = '' OR e.reason = $1)
		       AND ($2 = '' OR e.location_key = $2)
		       AND ($3::timestamptz IS NULL OR e.occurred_at >= $3)
		 )
		 SELECT k.group_key, k.reason, k.location_key, max(k.location_name), max(k.occurred_at),
		        count(*)::int, COALESCE(SUM(k.bytes), 0)::bigint,
		        (array_agg(k.title ORDER BY k.id) FILTER (WHERE k.title <> ''))[1:3],
		        max(k.detail), max(k.actor_user_id), COALESCE(max(a.username), ''),
		        k.user_id, COALESCE(max(acct.username), '')
		 FROM keyed k
		 LEFT JOIN users a ON a.id = k.actor_user_id
		 LEFT JOIN users acct ON acct.id = k.user_id
		 GROUP BY k.group_key, k.reason, k.location_key, k.user_id
		 HAVING ($4::timestamptz IS NULL OR (max(k.occurred_at), k.group_key) < ($4, $5))
		 ORDER BY max(k.occurred_at) DESC, k.group_key DESC
		 LIMIT $6`,
		f.Reason, f.Location, f.Since, afterAt, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing storage history: %w", err)
	}
	defer rows.Close()
	var out []StorageEventBatch
	for rows.Next() {
		var b StorageEventBatch
		var titles []string
		if err := rows.Scan(&b.BatchID, &b.Reason, &b.Location, &b.LocationName, &b.OccurredAt,
			&b.Count, &b.Bytes, &titles, &b.Detail, &b.ActorUserID, &b.ActorName,
			&b.AccountUserID, &b.AccountName); err != nil {
			return nil, fmt.Errorf("scanning storage history: %w", err)
		}
		b.Titles = titles
		if b.Location == LocationServer && b.LocationName == "" {
			b.LocationName = serverLocationName
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
