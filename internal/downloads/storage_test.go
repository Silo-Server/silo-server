package downloads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/downloadstorage"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

type fakeStorageNodes struct{ nodes []*nodepool.Node }

func (f fakeStorageNodes) List(context.Context) ([]*nodepool.Node, error) { return f.nodes, nil }

// remoteReadyArtifact stores a ready prepared file on nodeID, last used age ago.
func remoteReadyArtifact(t *testing.T, repo *ArtifactRepository, pool *pgxpool.Pool, fileID, nodeID int, size int64, age time.Duration) *Artifact {
	t.Helper()
	ctx := context.Background()
	a := newArtifact(t, fileID, fmt.Sprintf("hash-storage-%d-%d", time.Now().UnixNano(), rand.Int()))
	row, _, err := repo.EnsureQueued(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE download_artifacts SET status = 'ready', origin_node_id = $2, origin_node_url = 'http://storage-test-node',
		     origin_artifact_id = $1 || '-attempt', file_size = $3, completed_at = now(),
		     last_used_at = now() - make_interval(secs => $4)
		 WHERE id = $1`, row.ID, nodeID, size, age.Seconds()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM download_artifact_orphans WHERE download_artifact_id = $1`, row.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM download_storage_events WHERE artifact_id = $1`, row.ID)
	})
	got, err := repo.GetByID(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func storageTestManager(repo *ArtifactRepository, cfg *config.Config, nodes ...*nodepool.Node) *ArtifactManager {
	m := NewArtifactManager(repo, nil, nil, nil, "storage-test", func() *config.Config { return cfg }, nil)
	m.SetStorageNodes(fakeStorageNodes{nodes: nodes})
	return m
}

func artifactStatus(t *testing.T, repo *ArtifactRepository, id string) string {
	t.Helper()
	a, err := repo.GetByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return a.Status
}

func eventReasons(t *testing.T, pool *pgxpool.Pool, artifactID string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT reason FROM download_storage_events WHERE artifact_id = $1 ORDER BY id`, artifactID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestEnforceStorageExpiresCachedFilesThenEnforcesBudget(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	nodeID := 700000 + rand.IntN(100000)
	old := remoteReadyArtifact(t, repo, pool, fileID, nodeID, 100, 5*24*time.Hour)
	finished := remoteReadyArtifact(t, repo, pool, fileID, nodeID, 100, 2*time.Hour)
	unlinked := remoteReadyArtifact(t, repo, pool, fileID, nodeID, 100, time.Hour)
	inUse := remoteReadyArtifact(t, repo, pool, fileID, nodeID, 100, 4*24*time.Hour)
	linkRecoveryDownload(t, pool, fileID, finished.ID, StatusCompleted)
	linkRecoveryDownload(t, pool, fileID, inUse.ID, StatusReady)

	budget := int64(150)
	cfg := &config.Config{}
	cfg.Download.ArtifactCacheHours = 72
	cfg.Download.ArtifactDiskCeilingPercent = 85
	m := storageTestManager(repo, cfg, &nodepool.Node{ID: nodeID, Name: "storage-test", Type: "transcode", Enabled: true, DownloadArtifactMaxBytesOverride: &budget})

	freed := m.enforceStorage(ctx, NodeLocationKey(nodeID))
	if freed != 300 {
		t.Fatalf("freed = %d, want 300 (one past its cache period, two to meet the budget)", freed)
	}
	for _, a := range []*Artifact{old, finished, unlinked} {
		if got := artifactStatus(t, repo, a.ID); got != ArtifactExpired {
			t.Fatalf("artifact %s status = %s, want expired", a.ID, got)
		}
	}
	if got := artifactStatus(t, repo, inUse.ID); got != ArtifactReady {
		t.Fatalf("a file a download waits on was removed: %s", got)
	}
	if got := eventReasons(t, pool, old.ID); len(got) != 1 || got[0] != StorageReasonCacheExpired {
		t.Fatalf("old file history = %v, want cache_expired", got)
	}
	for _, a := range []*Artifact{finished, unlinked} {
		if got := eventReasons(t, pool, a.ID); len(got) != 1 || got[0] != StorageReasonBudget {
			t.Fatalf("file %s history = %v, want budget", a.ID, got)
		}
	}
	orphans, err := repo.ListRemoteOrphansDue(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Artifact{old, finished, unlinked} {
		if len(remoteOrphansForArtifact(orphans, a.ID)) != 1 {
			t.Fatalf("expired node file %s was not queued for deletion", a.ID)
		}
	}
	m.refreshStorageFull(ctx)
	if m.NodeStorageFull(nodeID) {
		t.Fatal("node under its budget after clean-up must accept work")
	}

	// A budget below what downloads still need cannot be met: the node is
	// marked full so placement stops sending it work.
	tight := int64(50)
	m.SetStorageNodes(fakeStorageNodes{nodes: []*nodepool.Node{{ID: nodeID, Name: "storage-test", Type: "transcode", Enabled: true, DownloadArtifactMaxBytesOverride: &tight}}})
	if freed := m.enforceStorage(ctx, NodeLocationKey(nodeID)); freed != 0 {
		t.Fatalf("freed = %d with only in-use files left", freed)
	}
	m.refreshStorageFull(ctx)
	if !m.NodeStorageFull(nodeID) {
		t.Fatal("node over budget with nothing to free must be marked full")
	}
}

func TestOverCeilingIgnoresAMeasurementOlderThanTheLastEviction(t *testing.T) {
	m := &ArtifactManager{}
	now := time.Now()
	measured := now.Add(-time.Minute)
	loc := storageLocation{NodeID: 3, Usage: &downloadstorage.Usage{MeasuredAt: measured, FSUsedBytes: 900, FSTotalBytes: 1000}}
	if got := m.overCeiling(loc, 0, time.Time{}, now); got != 50 {
		t.Fatalf("over ceiling = %d, want 50 above 85%%", got)
	}
	if got := m.overCeiling(loc, 100, time.Time{}, now); got != -50 {
		t.Fatalf("over ceiling after freeing 100 = %d, want -50", got)
	}
	if got := m.overCeiling(loc, 0, now, now); got != 0 {
		t.Fatalf("a measurement taken before the last eviction was acted on again: %d", got)
	}
	stale := storageLocation{NodeID: 4, Usage: &downloadstorage.Usage{MeasuredAt: now, FSUsedBytes: 999, FSTotalBytes: 1000, Stale: true}}
	if got := m.overCeiling(stale, 0, time.Time{}, now); got != 0 {
		t.Fatalf("a stale measurement was acted on: %d", got)
	}
	// A node that stopped reporting may have freed space since.
	if got := m.overCeiling(loc, 0, time.Time{}, now.Add(ceilingMeasurementMaxAge)); got != 0 {
		t.Fatalf("an old measurement was acted on: %d", got)
	}
}

func TestFindUntracked(t *testing.T) {
	now := time.Now()
	old, fresh := now.Add(-2*time.Hour), now.Add(-time.Minute)
	const (
		a = "11-00000000-0000-4000-8000-00000000000a"
		b = "12-00000000-0000-4000-8000-00000000000b"
		c = "13-00000000-0000-4000-8000-00000000000c"
		d = "14-00000000-0000-4000-8000-00000000000d"
		e = "15-00000000-0000-4000-8000-00000000000e"
	)
	files := []downloadstorage.File{
		{Name: a + ".mp4", Kind: downloadstorage.KindComplete, Bytes: 10, ModTime: old},              // tracked
		{Name: a + ".mp4.receipt.json", Kind: downloadstorage.KindOther, Bytes: 1, ModTime: old},     // belongs to tracked
		{Name: b + ".mp4", Kind: downloadstorage.KindComplete, Bytes: 20, ModTime: old},              // untracked
		{Name: b + ".mp4.receipt.json", Kind: downloadstorage.KindOther, Bytes: 2, ModTime: old},     // untracked
		{Name: c + ".mp4", Kind: downloadstorage.KindComplete, Bytes: 30, ModTime: fresh},            // too new to judge
		{Name: d + ".mp4.part", Kind: downloadstorage.KindPartial, Bytes: 40, ModTime: old},          // dead partial
		{Name: e + ".mp4.part", Kind: downloadstorage.KindPartial, Bytes: 50, ModTime: fresh},        // encode in progress
		{Name: a + ".mp4.receipt.json.123", Kind: downloadstorage.KindOther, Bytes: 3, ModTime: old}, // temp of tracked
		// A dead partial beside a tracked file: deleting it would delete the
		// whole artifact on the node, finished file included.
		{Name: a + ".mp4.part", Kind: downloadstorage.KindPartial, Bytes: 4, ModTime: old},
		// Not named like a prepared file: never Silo's to delete.
		{Name: "Movie (2010).mp4", Kind: downloadstorage.KindComplete, Bytes: 70, ModTime: old},
		{Name: "README", Kind: downloadstorage.KindOther, Bytes: 80, ModTime: old},
		{Name: "12_remux_abc_34.mp4", Kind: downloadstorage.KindComplete, Bytes: 90, ModTime: old}, // a server file
	}
	got := findUntracked(files, nodeArtifactFileName, map[string]bool{a + ".mp4": true}, now)
	names := map[string]bool{}
	for _, f := range got {
		names[f.Name] = true
	}
	want := []string{b + ".mp4", b + ".mp4.receipt.json", d + ".mp4.part"}
	if len(got) != len(want) {
		t.Fatalf("untracked = %v, want %v", names, want)
	}
	for _, n := range want {
		if !names[n] {
			t.Fatalf("untracked = %v, missing %s", names, n)
		}
	}
	if s := sumUntracked(got); s.Files != 3 || s.Bytes != 62 {
		t.Fatalf("summary = %+v", s)
	}
}

func TestServerArtifactFileName(t *testing.T) {
	for name, want := range map[string]bool{
		"12_transcode_0a1b2c3d4e5f6a7b_146672991.mp4":     true,
		"12_remux_legacy-parameter_146672991.mp4.part":    true,
		"12_remux_abc_34.mp4.receipt.json":                false,
		"Movie (2010).mp4":                                false,
		"11-00000000-0000-4000-8000-00000000000a.mp4":     false,
		"12_transcode_0a1b2c3d4e5f6a7b_146672991.mp4.bak": false,
	} {
		if got := serverArtifactFileName.MatchString(name); got != want {
			t.Errorf("serverArtifactFileName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestNodeArtifactIDFromFile(t *testing.T) {
	for name, want := range map[string]string{
		"abc-123.mp4": "abc-123", "abc-123.mp4.part": "abc-123",
		"abc-123.mp4.receipt.json": "abc-123", "abc-123.mp4.receipt.json.99": "abc-123",
	} {
		if got := nodeArtifactIDFromFile(name); got != want {
			t.Errorf("nodeArtifactIDFromFile(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestParseLocationKey(t *testing.T) {
	for key, want := range map[string]int{"server": 0, "node:7": 7} {
		if got, ok := ParseLocationKey(key); !ok || got != want {
			t.Errorf("ParseLocationKey(%q) = (%d, %v)", key, got, ok)
		}
	}
	for _, key := range []string{"", "node:", "node:0", "node:-1", "node:07", "device", "node:x"} {
		if _, ok := ParseLocationKey(key); ok {
			t.Errorf("ParseLocationKey(%q) accepted", key)
		}
	}
}

func TestPrepareDownloadAgainRequeuesAnExpiredFile(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	a := remoteReadyArtifact(t, repo, pool, fileID, 700000+rand.IntN(100000), 100, 4*24*time.Hour)
	linkRecoveryDownload(t, pool, fileID, a.ID, StatusCompleted)
	if applied, err := repo.ExpireReady(ctx, a, missingArtifactRetireGrace); err != nil || !applied {
		t.Fatalf("ExpireReady = (%v, %v)", applied, err)
	}
	var d Download
	if err := scanInto(pool.QueryRow(ctx, `SELECT `+downloadColumns+` FROM downloads WHERE artifact_id = $1`, a.ID), &d); err != nil {
		t.Fatal(err)
	}
	updated, requeued, err := repo.PrepareDownloadAgain(ctx, &d)
	if err != nil || !requeued || updated.Status != StatusPreparing || updated.Revision != d.Revision {
		t.Fatalf("PrepareDownloadAgain = (%+v, %v, %v)", updated, requeued, err)
	}
	if got := artifactStatus(t, repo, a.ID); got != ArtifactQueued {
		t.Fatalf("artifact after prepare again = %s, want queued", got)
	}
	// Asking again while it prepares changes nothing.
	again, requeued, err := repo.PrepareDownloadAgain(ctx, updated)
	if err != nil || requeued || again.Status != StatusPreparing {
		t.Fatalf("second PrepareDownloadAgain = (%+v, %v, %v)", again, requeued, err)
	}
}

func TestRevokeManagedExcludesMonitoredEpisodesAndRecordsHistory(t *testing.T) {
	f := seedManagedFixture(t)
	ctx := context.Background()
	subID := fmt.Sprintf("sub-%d", time.Now().UnixNano())
	if _, err := f.pool.Exec(ctx, `INSERT INTO download_subscriptions (id, user_id, profile_id, device_id, series_id, mode, active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'all', true, now(), now())`, subID, f.userID, f.profileA, f.deviceA, f.contentID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(ctx, `DELETE FROM download_subscriptions WHERE id = $1`, subID) })
	now := time.Now()
	episodeID := fmt.Sprintf("ep-%d", now.UnixNano())
	ids := []string{fmt.Sprintf("dl-rev-a-%d", now.UnixNano()), fmt.Sprintf("dl-rev-b-%d", now.UnixNano())}
	// The first copy is on the device; the second is still waiting to be
	// fetched, so it frees nothing there.
	for i, id := range ids {
		ep, status, completedAt := "", StatusReady, (*time.Time)(nil)
		if i == 0 {
			ep, status, completedAt = episodeID, StatusCompleted, &now
		}
		if err := f.repo.Create(ctx, &Download{
			ID: id, UserID: f.userID, ProfileID: f.profileA, DeviceID: f.deviceA, MediaFileID: f.fileID,
			ContentID: f.contentID, EpisodeID: ep, Kind: KindQueued, Status: status,
			Format: FormatOriginal, FileSize: 500, CreatedAt: now, UpdatedAt: now, CompletedAt: completedAt,
		}); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM download_storage_events WHERE user_id = $1`, f.userID)
		_, _ = f.pool.Exec(ctx, `DELETE FROM download_subscription_exclusions WHERE subscription_id = $1`, subID)
	})
	var actor int
	if err := f.pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'admin') RETURNING id`, fmt.Sprintf("revoker-%d", now.UnixNano())).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, actor) })

	result, err := f.repo.RevokeManaged(ctx, RevokeRequest{
		UserID: f.userID, ProfileID: f.profileA, DeviceID: f.deviceA, PauseMonitors: true, Reason: "lost phone", Actor: actor,
	}, newStorageBatchID())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Revoked) != 2 || result.Bytes != 500 || result.PausedMonitors != 1 {
		t.Fatalf("revoke result = %+v", result)
	}
	for _, d := range result.Revoked {
		if d.Status != StatusRevoked {
			t.Fatalf("revoked row status = %s", d.Status)
		}
	}
	var excluded, events int
	var reason string
	var revokedBy *int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM download_subscription_exclusions WHERE subscription_id = $1 AND episode_id = $2`, subID, episodeID).Scan(&excluded); err != nil || excluded != 1 {
		t.Fatalf("monitor exclusions = %d (%v), want the revoked episode", excluded, err)
	}
	var eventBytes int64
	if err := f.pool.QueryRow(ctx, `SELECT count(*), COALESCE(sum(bytes), 0) FROM download_storage_events WHERE user_id = $1 AND reason = 'revoked' AND actor_user_id = $2`, f.userID, actor).Scan(&events, &eventBytes); err != nil || events != 2 || eventBytes != 500 {
		t.Fatalf("revoke history = %d rows, %d bytes (%v), want one per row and only the copy on the device", events, eventBytes, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT revoked_reason, revoked_by FROM downloads WHERE id = $1`, ids[0]).Scan(&reason, &revokedBy); err != nil || reason != "lost phone" || revokedBy == nil || *revokedBy != actor {
		t.Fatalf("revoked row = (%q, %v, %v)", reason, revokedBy, err)
	}
	var active bool
	if err := f.pool.QueryRow(ctx, `SELECT active FROM download_subscriptions WHERE id = $1`, subID).Scan(&active); err != nil || active {
		t.Fatalf("monitor active = %v (%v), want paused", active, err)
	}
	// Revoking again changes nothing.
	again, err := f.repo.RevokeManaged(ctx, RevokeRequest{IDs: ids, Actor: actor}, newStorageBatchID())
	if err != nil || len(again.Revoked) != 0 {
		t.Fatalf("second revoke = (%+v, %v)", again, err)
	}
	// The device confirming its local copy is gone deletes the row and is
	// recorded as removed from the device; only the finished copy took space.
	for i, want := range []int64{500, 0} {
		if err := f.repo.DeleteManaged(ctx, ids[i], f.userID, f.profileA, f.deviceA); err != nil {
			t.Fatal(err)
		}
		var removed int
		var bytes int64
		if err := f.pool.QueryRow(ctx, `SELECT count(*), COALESCE(sum(bytes), 0) FROM download_storage_events WHERE download_id = $1 AND reason = 'device_removed'`, ids[i]).Scan(&removed, &bytes); err != nil || removed != 1 || bytes != want {
			t.Fatalf("device removal history for %s = %d rows, %d bytes (%v), want 1 row, %d bytes", ids[i], removed, bytes, err, want)
		}
	}
}

func TestTouchDeviceSeenWritesAtMostHourly(t *testing.T) {
	f := seedManagedFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE user_devices SET last_seen_at = now() - interval '3 days' WHERE user_id = $1 AND device_id = $2`, f.userID, f.deviceA); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.TouchDeviceSeen(ctx, f.userID, f.profileA, f.deviceA); err != nil {
		t.Fatal(err)
	}
	var seen time.Time
	if err := f.pool.QueryRow(ctx, `SELECT last_seen_at FROM user_devices WHERE user_id = $1 AND device_id = $2`, f.userID, f.deviceA).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if time.Since(seen) > time.Minute {
		t.Fatalf("last seen = %v, want just now", seen)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE user_devices SET last_seen_at = now() - interval '10 minutes' WHERE user_id = $1 AND device_id = $2`, f.userID, f.deviceA); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.TouchDeviceSeen(ctx, f.userID, f.profileA, f.deviceA); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT last_seen_at FROM user_devices WHERE user_id = $1 AND device_id = $2`, f.userID, f.deviceA).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if time.Since(seen) < 9*time.Minute {
		t.Fatal("a sync within the hour rewrote last seen")
	}
}

func TestStorageReadModelsReportFilesDevicesAndHistory(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	nodeID := 700000 + rand.IntN(100000)
	cached := remoteReadyArtifact(t, repo, pool, fileID, nodeID, 300, 2*time.Hour)
	inUse := remoteReadyArtifact(t, repo, pool, fileID, nodeID, 700, time.Hour)
	linkRecoveryDownload(t, pool, fileID, inUse.ID, StatusReady)
	cfg := &config.Config{}
	cfg.Download.ArtifactCacheHours = 72
	cfg.Download.ArtifactDiskCeilingPercent = 85
	cfg.Download.ArtifactMaxBytes = 5000
	node := &nodepool.Node{ID: nodeID, Name: "read-model-node", Type: "transcode", Enabled: true, Healthy: true,
		LastStats: []byte(`{"artifacts":{"measured_at":"2026-10-08T10:00:00Z","files":2,"bytes":1000,"fs_used_bytes":5000,"fs_total_bytes":10000,"shares_scratch":true}}`)}
	m := storageTestManager(repo, cfg, node)

	overview, err := m.StorageOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var view *StorageLocationView
	for i := range overview.Locations {
		if overview.Locations[i].NodeID == nodeID {
			view = &overview.Locations[i]
		}
	}
	if overview.Locations[0].Key != LocationServer || view == nil {
		t.Fatalf("locations = %+v", overview.Locations)
	}
	if view.InUseBytes != 700 || view.CachedBytes != 300 || view.WaitingDownloads != 1 || view.Budget != 5000 || view.BudgetSource != StorageSourceSetting {
		t.Fatalf("node view = %+v", view)
	}
	if view.Usage == nil || !view.Usage.SharesScratch || view.Usage.FSTotalBytes != 10000 {
		t.Fatalf("node usage = %+v", view.Usage)
	}
	if overview.CacheHours != 72 || overview.DiskCeilingPercent != 85 {
		t.Fatalf("overview settings = %+v", overview)
	}

	for _, sortBy := range []string{StorageSortSize, StorageSortLastUsed, StorageSortCreated} {
		page, err := m.StorageFilesPage(ctx, StorageFileFilter{Location: NodeLocationKey(nodeID), Sort: sortBy}, nil, 1)
		if err != nil || len(page) != 1 {
			t.Fatalf("files page (%s) = (%+v, %v)", sortBy, page, err)
		}
		after := &StorageFilePosition{Bytes: page[0].Bytes, At: page[0].LastUsedAt, ID: page[0].ArtifactID}
		if sortBy == StorageSortCreated {
			after.At = page[0].CreatedAt
		}
		rest, err := m.StorageFilesPage(ctx, StorageFileFilter{Location: NodeLocationKey(nodeID), Sort: sortBy}, after, 10)
		if err != nil || len(rest) != 1 || rest[0].ArtifactID == page[0].ArtifactID {
			t.Fatalf("second files page (%s) = (%+v, %v)", sortBy, rest, err)
		}
	}
	inUsePage, err := m.StorageFilesPage(ctx, StorageFileFilter{Location: NodeLocationKey(nodeID), State: StorageFileInUse}, nil, 10)
	if err != nil || len(inUsePage) != 1 || inUsePage[0].ArtifactID != inUse.ID || inUsePage[0].Waiting != 1 || inUsePage[0].StaleWaiting != 0 {
		t.Fatalf("in-use files = (%+v, %v)", inUsePage, err)
	}
	cachedPage, err := m.StorageFilesPage(ctx, StorageFileFilter{Location: NodeLocationKey(nodeID), State: StorageFileCached}, nil, 10)
	if err != nil || len(cachedPage) != 1 || cachedPage[0].ArtifactID != cached.ID || cachedPage[0].ExpiresAt == nil {
		t.Fatalf("cached files = (%+v, %v)", cachedPage, err)
	}

	results, err := m.DeleteStorageFiles(ctx, []string{cached.ID, inUse.ID, "missing-id"}, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	outcomes := map[string]string{}
	for _, r := range results {
		outcomes[r.ArtifactID] = r.Outcome
	}
	if outcomes[cached.ID] != StorageDeleteDeleted || outcomes[inUse.ID] != StorageDeleteInUse || outcomes["missing-id"] != StorageDeleteNotFound {
		t.Fatalf("delete outcomes = %v", outcomes)
	}
	history, err := m.StorageEventsPage(ctx, StorageEventFilter{Location: NodeLocationKey(nodeID)}, nil, 10)
	if err != nil || len(history) != 1 || history[0].Reason != StorageReasonAdminDelete || history[0].Bytes != 300 || history[0].Count != 1 {
		t.Fatalf("history = (%+v, %v)", history, err)
	}
	more, err := m.StorageEventsPage(ctx, StorageEventFilter{Location: NodeLocationKey(nodeID)}, &StorageEventPosition{At: history[0].OccurredAt, BatchID: history[0].BatchID}, 10)
	if err != nil || len(more) != 0 {
		t.Fatalf("history after the last batch = (%+v, %v)", more, err)
	}
}

func TestAdminDeviceAndEntryPages(t *testing.T) {
	f := seedManagedFixture(t)
	ctx := context.Background()
	f.createManagedEntry(t)
	svc := &Service{repo: f.repo}
	for _, sortBy := range []string{DeviceSortLastSeen, DeviceSortSize} {
		rows, err := svc.AdminListDevicesPage(ctx, AdminDeviceFilter{Query: "Phone A", Sort: sortBy}, nil, 50)
		if err != nil {
			t.Fatalf("devices (%s): %v", sortBy, err)
		}
		var found *AdminDeviceRow
		for i := range rows {
			if rows[i].DeviceID == f.deviceA {
				found = &rows[i]
			}
		}
		if found == nil || found.Copies != 1 || found.Waiting != 1 || found.DeviceName != "Phone A" || found.ProfileName != "A" {
			t.Fatalf("device row (%s) = %+v", sortBy, found)
		}
		after := &AdminDevicePosition{UserID: found.UserID, ProfileID: found.ProfileID, DeviceID: found.DeviceID, Bytes: found.BytesOnDevice}
		if found.LastSeenAt != nil {
			after.LastSeen = *found.LastSeenAt
		}
		if _, err := svc.AdminListDevicesPage(ctx, AdminDeviceFilter{Sort: sortBy}, after, 50); err != nil {
			t.Fatalf("devices after a position (%s): %v", sortBy, err)
		}
	}
	entries, err := svc.AdminListEntriesPage(ctx, AdminEntryFilter{UserID: f.userID, DeviceID: f.deviceA, Status: StatusReady}, nil, 10)
	if err != nil || len(entries) != 1 || entries[0].DeviceName != "Phone A" {
		t.Fatalf("entries = (%+v, %v)", entries, err)
	}
}

func TestUpsertStorageSampleKeepsLargeCountsAndLastReconciliation(t *testing.T) {
	repo, _, _ := newArtifactTestRepo(t)
	ctx := context.Background()
	reporter := fmt.Sprintf("sample-test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(ctx, `DELETE FROM download_storage_samples WHERE reporter = $1`, reporter)
	})
	at := time.Now().Truncate(time.Second)
	usage := downloadstorage.Usage{Dir: "/srv/a", MeasuredAt: at, Bytes: 5_400_000_000, FSTotalBytes: 2_000_000_000_000}
	untracked := storageSampleUntracked{Files: 6, Bytes: 5_399_999_999, At: at}
	if err := repo.UpsertStorageSample(ctx, 0, reporter, usage, &untracked); err != nil {
		t.Fatalf("record sample with untracked bytes past int4: %v", err)
	}
	// A measurement without reconciliation keeps the last untracked counts.
	usage.Bytes = 6_000_000_000
	if err := repo.UpsertStorageSample(ctx, 0, reporter, usage, nil); err != nil {
		t.Fatal(err)
	}
	samples, err := repo.storageSamples(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range samples[0] {
		if s.reporter != reporter {
			continue
		}
		if s.usage.Bytes != 6_000_000_000 || s.untrackedFiles != 6 || s.untrackedBytes != 5_399_999_999 || s.reconciledAt == nil {
			t.Fatalf("sample = %+v", s)
		}
		return
	}
	t.Fatal("sample not stored")
}

func TestExpiredFilesAreNotPreparations(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	a := remoteReadyArtifact(t, repo, pool, fileID, 1, 100, 5*24*time.Hour)
	linkRecoveryDownload(t, pool, fileID, a.ID, StatusCompleted)
	if _, err := pool.Exec(ctx, `UPDATE download_artifacts SET `+expireArtifactAssignment+` WHERE id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if got := artifactStatus(t, repo, a.ID); got != ArtifactExpired {
		t.Fatalf("status = %s, want expired", got)
	}
	list, err := NewPreparationReader(pool, nil).List(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list.Items {
		if item.ArtifactID == a.ID {
			t.Fatal("an expired file is listed as a preparation")
		}
	}
	// Canceling it as a preparation must not delete the recipe a finished
	// download still reads.
	if _, _, err := repo.CancelPreparations(ctx, []string{a.ID}, "canceled"); err != nil {
		t.Fatal(err)
	}
	if got := artifactStatus(t, repo, a.ID); got != ArtifactExpired {
		t.Fatalf("status after cancel = %s, want expired", got)
	}
}

func TestFailedSweepKeepsARecipeAFinishedDownloadReads(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	referenced := remoteReadyArtifact(t, repo, pool, fileID, 1, 100, 5*24*time.Hour)
	unreferenced := remoteReadyArtifact(t, repo, pool, fileID, 1, 100, 5*24*time.Hour)
	linkRecoveryDownload(t, pool, fileID, referenced.ID, StatusCompleted)
	if _, err := pool.Exec(ctx, `UPDATE download_artifacts SET status = 'failed' WHERE id = ANY($1)`,
		[]string{referenced.ID, unreferenced.ID}); err != nil {
		t.Fatal(err)
	}
	failed, err := repo.ListFailedBefore(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, a := range failed {
		listed[a.ID] = true
	}
	if listed[referenced.ID] {
		t.Fatal("a failed re-preparation a finished download refers to would be deleted")
	}
	if !listed[unreferenced.ID] {
		t.Fatal("an unreferenced failed preparation is not swept")
	}
}

// localReadyArtifact turns a stored artifact into a ready server file at path.
func localReadyArtifact(t *testing.T, repo *ArtifactRepository, pool *pgxpool.Pool, fileID int, path string, age time.Duration) *Artifact {
	t.Helper()
	a := remoteReadyArtifact(t, repo, pool, fileID, 1, 100, age)
	if _, err := pool.Exec(context.Background(),
		`UPDATE download_artifacts SET origin_node_id = 0, origin_node_url = '', origin_artifact_id = '', output_path = $2 WHERE id = $1`,
		a.ID, path); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByID(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// An administrator deleting a node file downloads still wait on requeues it;
// history shows the delete, not a missing file as well.
func TestAdminDeleteOfAnInUseNodeFileRecordsOnlyTheDelete(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	nodeID := 700000 + rand.IntN(100000)
	a := remoteReadyArtifact(t, repo, pool, fileID, nodeID, 100, time.Hour)
	linkRecoveryDownload(t, pool, fileID, a.ID, StatusReady)
	m := storageTestManager(repo, &config.Config{})

	results, err := m.DeleteStorageFiles(context.Background(), []string{a.ID}, true, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Outcome != StorageDeleteRequeued {
		t.Fatalf("results = %+v, want one requeued", results)
	}
	if got := eventReasons(t, pool, a.ID); len(got) != 1 || got[0] != StorageReasonAdminDelete {
		t.Fatalf("history = %v, want only admin_delete", got)
	}
}

// Deleting an in-use server file moves the row first and then removes the
// file, so the row never claims bytes that are gone.
func TestAdminDeleteOfAnInUseServerFileRequeuesThenRemovesIt(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	path := filepath.Join(t.TempDir(), "prepared.mp4")
	if err := os.WriteFile(path, []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := localReadyArtifact(t, repo, pool, fileID, path, time.Hour)
	linkRecoveryDownload(t, pool, fileID, a.ID, StatusReady)
	m := storageTestManager(repo, &config.Config{})

	results, err := m.DeleteStorageFiles(context.Background(), []string{a.ID}, true, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Outcome != StorageDeleteRequeued {
		t.Fatalf("results = %+v, want one requeued", results)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepared file still on disk: %v", err)
	}
	if got := artifactStatus(t, repo, a.ID); got != ArtifactQueued {
		t.Fatalf("status = %s, want queued", got)
	}
}

// Expiry counts bytes as freed only once they are gone. A server file that
// cannot be removed is not recorded as freed; reconciliation reports it.
func TestExpiryDoesNotCountAServerFileItCannotRemove(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	// A non-empty directory where the file should be makes os.Remove fail.
	path := filepath.Join(t.TempDir(), "prepared.mp4")
	if err := os.MkdirAll(filepath.Join(path, "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := localReadyArtifact(t, repo, pool, fileID, path, 5*24*time.Hour)
	m := storageTestManager(repo, &config.Config{})

	if m.expireArtifact(context.Background(), a, StorageReasonCacheExpired, newStorageBatchID(), nil) {
		t.Fatal("expiry reported a file it could not remove as freed")
	}
	if got := eventReasons(t, pool, a.ID); len(got) != 0 {
		t.Fatalf("history = %v, want nothing recorded", got)
	}
}

// A revoke cancels only preparations no live download refers to, checked in
// the statement that deletes them, so a download that linked the job after
// the revoke looked keeps it.
func TestCancelAbandonedPreparationsKeepsAJobADownloadStillNeeds(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	needed := remoteReadyArtifact(t, repo, pool, fileID, 1, 100, time.Hour)
	abandoned := remoteReadyArtifact(t, repo, pool, fileID, 1, 100, time.Hour)
	if _, err := pool.Exec(ctx, `UPDATE download_artifacts SET status = 'queued' WHERE id = ANY($1)`,
		[]string{needed.ID, abandoned.ID}); err != nil {
		t.Fatal(err)
	}
	linkRecoveryDownload(t, pool, fileID, needed.ID, StatusPreparing)
	linkRecoveryDownload(t, pool, fileID, abandoned.ID, StatusRevoked)

	canceled, _, err := repo.CancelAbandonedPreparations(ctx, []string{needed.ID, abandoned.ID}, "canceled")
	if err != nil {
		t.Fatal(err)
	}
	if len(canceled) != 1 || canceled[0].ID != abandoned.ID {
		t.Fatalf("canceled = %+v, want only the abandoned job", canceled)
	}
	if got := artifactStatus(t, repo, needed.ID); got != ArtifactQueued {
		t.Fatalf("job a download waits on: status = %s, want queued", got)
	}
}

// nodeOnDisk is a transcode node whose last health check measured a disk.
func nodeOnDisk(t *testing.T, id int, used, total int64, measured time.Time) *nodepool.Node {
	t.Helper()
	stats, err := json.Marshal(map[string]any{"artifacts": downloadstorage.Usage{
		MeasuredAt: measured, FSUsedBytes: used, FSTotalBytes: total, FSType: "xfs",
	}})
	if err != nil {
		t.Fatal(err)
	}
	none := int64(0)
	return &nodepool.Node{ID: id, Name: fmt.Sprintf("node-%d", id), Type: "transcode", Enabled: true,
		LastStats: stats, DownloadArtifactMaxBytesOverride: &none}
}

// Two nodes on one volume report the same disk. Over the ceiling, the pass
// frees the overage once, not once per node.
func TestDiskCeilingFreesASharedDiskOnce(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	first, second := 700000+rand.IntN(50000), 750000+rand.IntN(50000)
	var files []*Artifact
	for _, node := range []int{first, second} {
		for range 3 {
			files = append(files, remoteReadyArtifact(t, repo, pool, fileID, node, 100, 2*time.Hour))
		}
	}
	cfg := &config.Config{}
	cfg.Download.ArtifactCacheHours = 72
	cfg.Download.ArtifactDiskCeilingPercent = 85
	// 950 of 1000 used: 100 over the 85% ceiling.
	measured := time.Now()
	m := storageTestManager(repo, cfg, nodeOnDisk(t, first, 950, 1000, measured), nodeOnDisk(t, second, 950, 1000, measured))

	if freed := m.enforceStorage(ctx, ""); freed != 100 {
		t.Fatalf("freed = %d, want 100: the overage once for the shared disk", freed)
	}
	expired := 0
	for _, a := range files {
		if artifactStatus(t, repo, a.ID) == ArtifactExpired {
			expired++
		}
	}
	if expired != 1 {
		t.Fatalf("expired %d files, want 1", expired)
	}

	// The next pass reads the same measurement, taken before that removal:
	// it still counts the removed bytes, so it is not acted on again.
	if freed := m.enforceStorage(ctx, ""); freed != 0 {
		t.Fatalf("freed = %d on a measurement older than the last removal", freed)
	}
}

// Two downloads can read the same expired row. The first requeues it; the
// second must not reset the job if it is already running or ready.
func TestRequeueOnlyTakesAFailedOrExpiredRow(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	a := remoteReadyArtifact(t, repo, pool, fileID, 1, 100, time.Hour)
	if err := repo.Requeue(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Requeue of a ready row = %v, want ErrNotFound", err)
	}
	if got := artifactStatus(t, repo, a.ID); got != ArtifactReady {
		t.Fatalf("status = %s, want ready untouched", got)
	}
	if _, err := pool.Exec(ctx, `UPDATE download_artifacts SET status = 'expired' WHERE id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.Requeue(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.Requeue(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Requeue = %v, want ErrNotFound", err)
	}
}

// A node keeps the directory it started with until it restarts. The overview
// names that one, and the edited directory as pending.
func TestStorageOverviewNamesTheDirectoryANodeStillUses(t *testing.T) {
	repo, pool, _ := newArtifactTestRepo(t)
	ctx := context.Background()
	var nodeID int
	if err := pool.QueryRow(ctx, `INSERT INTO stream_nodes (name, type, url, enabled) VALUES ($1, 'transcode', 'http://moved-node', true) RETURNING id`,
		fmt.Sprintf("moved-node-%d", time.Now().UnixNano())).Scan(&nodeID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM stream_nodes WHERE id = $1`, nodeID) })
	if err := repo.UpsertStorageSample(ctx, nodeID, "", downloadstorage.Usage{Dir: "/old/downloads", MeasuredAt: time.Now()}, &storageSampleUntracked{}); err != nil {
		t.Fatal(err)
	}
	moved := "/new/downloads"
	node := &nodepool.Node{ID: nodeID, Name: "moved-node", Type: "transcode", Enabled: true, DownloadArtifactDirOverride: &moved}
	m := storageTestManager(repo, &config.Config{}, node)

	overview, err := m.StorageOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range overview.Locations {
		if v.NodeID != nodeID {
			continue
		}
		if v.Dir != "/old/downloads" || v.PendingDir != moved || v.DirSource != StorageSourceOverride {
			t.Fatalf("node directory = (%q, pending %q, %s), want the listed one with the edit pending", v.Dir, v.PendingDir, v.DirSource)
		}
		return
	}
	t.Fatal("node missing from the overview")
}

// A full server is a location like any other: over its budget with nothing
// left to free, it is marked full.
func TestRefreshStorageFullIncludesTheServer(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	a := localReadyArtifact(t, repo, pool, fileID, filepath.Join(t.TempDir(), "prepared.mp4"), time.Hour)
	linkRecoveryDownload(t, pool, fileID, a.ID, StatusReady)
	cfg := &config.Config{}
	cfg.Download.ArtifactMaxBytes = 50
	m := storageTestManager(repo, cfg)
	m.refreshStorageFull(context.Background())
	if !m.NodeStorageFull(0) {
		t.Fatal("a server over its budget with only in-use files left must be marked full")
	}
}

// A job that would be prepared on a full server waits without spending an
// attempt, and the encoder is never started.
func TestJobWaitsWhileTheServerIsFull(t *testing.T) {
	repo, _, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	row, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, fmt.Sprintf("hash-server-full-%d", time.Now().UnixNano())))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimNext(ctx, "worker", time.Minute)
	if err != nil || claimed.ID != row.ID {
		t.Fatalf("claim = (%+v, %v), want %s", claimed, err, row.ID)
	}
	preparer := &recordingEncodePreparer{}
	m := &ArtifactManager{
		repo: repo, owner: "worker", preparer: preparer,
		fileRepo:    fakeFileResolver{file: &models.MediaFile{ID: fileID, FilePath: "/media/movie.mkv"}},
		storageFull: map[int]bool{0: true},
	}
	m.encodeOne(ctx, claimed)

	if preparer.calls != 0 {
		t.Fatal("the encoder ran on a full server")
	}
	waiting, err := repo.GetByID(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != ArtifactQueued || waiting.Attempts != claimed.Attempts-1 || waiting.NextRetryAt == nil || !waiting.NextRetryAt.After(time.Now()) {
		t.Fatalf("job = status %s, attempts %d (claimed at %d), retry %v; want queued later with the attempt given back",
			waiting.Status, waiting.Attempts, claimed.Attempts, waiting.NextRetryAt)
	}
}

// Expiry clears a node file's live locator, but the inventory still lists the
// expired file under the node it was on.
func TestExpiredNodeFileKeepsItsLocation(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	nodeID := 700000 + rand.IntN(100000)
	a := remoteReadyArtifact(t, repo, pool, fileID, nodeID, 100, 5*24*time.Hour)
	if applied, err := repo.ExpireReady(ctx, a, missingArtifactRetireGrace); err != nil || !applied {
		t.Fatalf("ExpireReady = (%v, %v)", applied, err)
	}
	m := storageTestManager(repo, &config.Config{})
	page, err := m.StorageFilesPage(ctx, StorageFileFilter{Location: NodeLocationKey(nodeID), State: StorageFileExpired, Sort: StorageSortSize}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].ArtifactID != a.ID || page[0].Location != NodeLocationKey(nodeID) {
		t.Fatalf("expired files at the node = %+v", page)
	}
}

// An error deleting one file is reported for that file; the rest of the
// batch still goes ahead, and the results say which deletes took effect.
func TestDeleteStorageFilesReportsAFailureAndGoesOn(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	stuck := filepath.Join(t.TempDir(), "prepared.mp4")
	if err := os.MkdirAll(filepath.Join(stuck, "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	failing := localReadyArtifact(t, repo, pool, fileID, stuck, 5*24*time.Hour)
	cached := remoteReadyArtifact(t, repo, pool, fileID, 700000+rand.IntN(100000), 100, 5*24*time.Hour)
	m := storageTestManager(repo, &config.Config{})

	results, err := m.DeleteStorageFiles(context.Background(), []string{failing.ID, cached.ID}, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Outcome != StorageDeleteFailed || results[1].Outcome != StorageDeleteDeleted {
		t.Fatalf("results = %+v, want failed then deleted", results)
	}
}

// Prepare-again counts toward the concurrent cap only when it sends the entry
// back to preparing: a finished entry whose file is gone. A file still on the
// server is served as it is, and an entry already preparing counts already.
func TestPrepareAgainActivatesOnlyWhenTheFileIsGone(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	a := remoteReadyArtifact(t, repo, pool, fileID, 1, 100, time.Hour)
	m := storageTestManager(repo, &config.Config{})
	for _, tc := range []struct {
		download, artifact string
		want               bool
	}{
		{StatusCompleted, ArtifactReady, false},
		{StatusCompleted, ArtifactExpired, true},
		{StatusReady, ArtifactExpired, true},
		{StatusPreparing, ArtifactExpired, false},
	} {
		if _, err := pool.Exec(ctx, `UPDATE download_artifacts SET status = $2 WHERE id = $1`, a.ID, tc.artifact); err != nil {
			t.Fatal(err)
		}
		got, err := m.prepareAgainActivates(ctx, &Download{Status: tc.download, ArtifactID: a.ID})
		if err != nil || got != tc.want {
			t.Fatalf("%s entry, %s file: activates = (%v, %v), want %v", tc.download, tc.artifact, got, err, tc.want)
		}
	}
}

// The maintenance lock is held on its own session: work inside it can use the
// pool, and a second caller is turned away while it is held.
func TestMaintenanceLockHoldsItsOwnSession(t *testing.T) {
	repo, _, _ := newArtifactTestRepo(t)
	ctx := context.Background()
	ran, err := repo.WithMaintenanceLock(ctx, func(ctx context.Context) error {
		inner, err := repo.WithMaintenanceLock(ctx, func(context.Context) error { return nil })
		if err != nil || inner {
			t.Errorf("second holder = (%v, %v), want turned away", inner, err)
		}
		_, err = repo.ReadyBytesByLocation(ctx)
		return err
	})
	if err != nil || !ran {
		t.Fatalf("WithMaintenanceLock = (%v, %v)", ran, err)
	}
	if again, err := repo.WithMaintenanceLock(ctx, func(context.Context) error { return nil }); err != nil || !again {
		t.Fatalf("lock after release = (%v, %v), want free", again, err)
	}
}

// One administrator action can remove files at several locations; history
// lists each location's share as its own row instead of folding them into one.
func TestStorageHistorySplitsABatchByLocation(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	first, second := 700000+rand.IntN(50000), 750000+rand.IntN(50000)
	a := remoteReadyArtifact(t, repo, pool, fileID, first, 100, time.Hour)
	b := remoteReadyArtifact(t, repo, pool, fileID, second, 300, time.Hour)
	batch := newStorageBatchID()
	for _, f := range []*Artifact{a, b} {
		if err := repo.RecordArtifactEvent(ctx, batch, StorageReasonAdminDelete, NodeLocationKey(f.OriginNodeID), f.ID, f.FileSize, nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	m := storageTestManager(repo, &config.Config{})
	since := time.Now().Add(-time.Minute)
	page, err := m.StorageEventsPage(ctx, StorageEventFilter{Since: &since}, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	bytes := map[string]int64{}
	for _, row := range page {
		if strings.HasPrefix(row.BatchID, batch+"|") {
			bytes[row.Location] = row.Bytes
		}
	}
	if len(bytes) != 2 || bytes[NodeLocationKey(first)] != 100 || bytes[NodeLocationKey(second)] != 300 {
		t.Fatalf("history rows for the batch = %v, want one per location", bytes)
	}
}

// An untracked file the server cannot delete is reported as failed, not
// passed off as "nothing left to delete".
func TestDeleteUntrackedFilesReportsFilesItCannotRemove(t *testing.T) {
	repo, _, _ := newArtifactTestRepo(t)
	dir := t.TempDir()
	name := fmt.Sprintf("%d_transcode_abc123_%d.mp4", 900000+rand.IntN(1000), 900000+rand.IntN(1000))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	// A read-only directory refuses the unlink.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	cfg := &config.Config{}
	cfg.Download.ArtifactDir = dir
	m := storageTestManager(repo, cfg)

	result, err := m.DeleteUntrackedFiles(context.Background(), LocationServer, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Files != 0 || result.Failed != 1 {
		t.Fatalf("result = %+v, want 0 deleted and 1 failed", result)
	}
}

// A revoked finished copy is still on the device until it confirms the
// delete, so the device's size keeps counting it.
func TestDeviceSizeKeepsRevokedCopiesUntilConfirmed(t *testing.T) {
	f := seedManagedFixture(t)
	ctx := context.Background()
	now := time.Now()
	id := fmt.Sprintf("dl-held-%d", now.UnixNano())
	if err := f.repo.Create(ctx, &Download{
		ID: id, UserID: f.userID, ProfileID: f.profileA, DeviceID: f.deviceA, MediaFileID: f.fileID,
		ContentID: f.contentID, Kind: KindQueued, Status: StatusCompleted, Format: FormatOriginal,
		FileSize: 700, CreatedAt: now, UpdatedAt: now, CompletedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(ctx, `DELETE FROM download_storage_events WHERE download_id = $1`, id) })
	if _, err := f.repo.RevokeManaged(ctx, RevokeRequest{IDs: []string{id}}, newStorageBatchID()); err != nil {
		t.Fatal(err)
	}
	svc := &Service{repo: f.repo}
	rows, err := svc.AdminListDevicesPage(ctx, AdminDeviceFilter{Query: "Phone A"}, nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.DeviceID == f.deviceA {
			if row.BytesOnDevice != 700 {
				t.Fatalf("bytes on device = %d, want the revoked copy still counted", row.BytesOnDevice)
			}
			return
		}
	}
	t.Fatal("device missing")
}

// Right after a restart this replica has not measured the server's directory
// yet; the newest stored measurement stands in, so a server already over its
// disk ceiling is known to be full before the first job is claimed.
func TestStorageFullUsesTheStoredServerMeasurementAfterARestart(t *testing.T) {
	repo, pool, _ := newArtifactTestRepo(t)
	ctx := context.Background()
	reporter := fmt.Sprintf("restart-test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM download_storage_samples WHERE node_id IS NULL AND reporter = $1`, reporter)
	})
	full := downloadstorage.Usage{MeasuredAt: time.Now(), FSUsedBytes: 990, FSTotalBytes: 1000, FSType: "restart-test"}
	if err := repo.UpsertStorageSample(ctx, 0, reporter, full, nil); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Download.ArtifactDiskCeilingPercent = 85
	cfg.Download.ArtifactDir = t.TempDir()
	m := storageTestManager(repo, cfg)
	m.refreshStorageFull(ctx)
	if !m.NodeStorageFull(0) {
		t.Fatal("a server over its disk ceiling was not known to be full before its first local measurement")
	}
}

// A pass that frees part of an overage leaves a measurement that still counts
// the removed bytes. Until a newer one arrives, a full node stays full rather
// than reopening to new work.
func TestFullNodeStaysFullUntilANewerMeasurement(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	nodeID := 700000 + rand.IntN(100000)
	a := remoteReadyArtifact(t, repo, pool, fileID, nodeID, 100, time.Hour)
	measured := time.Now().Add(-time.Minute)
	cfg := &config.Config{}
	cfg.Download.ArtifactDiskCeilingPercent = 85
	m := storageTestManager(repo, cfg, nodeOnDisk(t, nodeID, 990, 1000, measured))
	m.storageFull = map[int]bool{nodeID: true}
	if err := repo.RecordArtifactEvent(ctx, newStorageBatchID(), StorageReasonDiskCeiling, NodeLocationKey(nodeID), a.ID, 100, nil, ""); err != nil {
		t.Fatal(err)
	}
	m.refreshStorageFull(ctx)
	if !m.NodeStorageFull(nodeID) {
		t.Fatal("a full node reopened on a measurement taken before the last removal")
	}
}
