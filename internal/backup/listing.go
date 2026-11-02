package backup

// Listing and deleting a server's backups, with the warnings the Backups
// page shows.

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/config"
)

// Delete removes one backup of this server under the subsystem lock.
func (s *Service) Delete(ctx context.Context, id string) error {
	if _, _, ok := ParseFolderName(id); !ok {
		return ErrNotFound
	}
	t, err := s.target(ctx)
	if err != nil {
		return err
	}
	serverID, err := s.serverID(ctx)
	if err != nil {
		return err
	}
	held, err := s.opts.Locker.TryLock(ctx)
	if err != nil {
		return err
	}
	defer held.Release(context.WithoutCancel(ctx))
	ctx, cancel := withLost(ctx, held)
	defer cancel()
	op := s.beginOperation(ctx, held, ActiveOperation{Operation: OperationDelete, BackupID: id, StartedAt: s.opts.Now()})
	defer op.end(ctx, nil)
	objects, err := t.List(ctx, folderPrefix(serverID, id))
	if err != nil {
		return err
	}
	if len(objects) == 0 {
		return ErrNotFound
	}
	return t.DeletePrefix(ctx, folderPrefix(serverID, id))
}

// Summary describes one backup folder of this server.
type Summary struct {
	ID          string     `json:"id"`
	Kind        Kind       `json:"kind"`
	Status      Status     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	SizeBytes   int64      `json:"size_bytes"`
	SiloVersion string     `json:"silo_version,omitempty"`
	// Location is where the folder is: a path on a local store.
	Location     string        `json:"location"`
	MissingBlobs []MissingBlob `json:"missing_blobs"`
	BlobCount    int           `json:"blob_count"`
	Objects      []ObjectEntry `json:"objects"`
	// KeyCheck is the check value of the SECRET_KEY the backup was taken
	// with, and KeyMatches whether it is this server's.
	KeyCheck   string `json:"key_check,omitempty"`
	KeyMatches bool   `json:"key_matches"`
}

// Warning is one condition the dashboard shows about backups.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Warning codes. Clients switch on these; messages are for display.
const (
	WarningDisabled               = "disabled"
	WarningNoRecentBackup         = "no_recent_backup"
	WarningDestinationUnavailable = "destination_unavailable"
	WarningSQLiteUserStore        = "sqlite_user_store"
	WarningToolsMissing           = "pg_dump_missing"
	WarningToolsTooOld            = "pg_dump_too_old"
)

// Listing is the Backups page's whole state.
type Listing struct {
	ServerID             string `json:"server_id"`
	Destination          string `json:"destination"`
	DestinationAvailable bool   `json:"destination_available"`
	Enabled              bool   `json:"enabled"`
	Busy                 bool   `json:"busy"`
	// Active is the operation holding the lock, on whichever server runs it.
	Active *ActiveOperation `json:"active,omitempty"`
	// LastRun is the latest backup run's outcome, including one that died.
	LastRun *LastRun `json:"last_run,omitempty"`
	// KeyCheck is this server's SECRET_KEY check value, for display.
	KeyCheck string `json:"key_check"`
	Keep     int    `json:"keep"`
	// LastSuccessAt is when the latest successful backup finished: the
	// published status, or the newest complete backup when that is later
	// (the status row is not in a dump, so a full restore starts it empty).
	LastSuccessAt  *time.Time `json:"last_success_at,omitempty"`
	Backups        []Summary  `json:"backups"`
	OtherServerIDs []string   `json:"other_server_ids"`
	Warnings       []Warning  `json:"warnings"`
}

// List reports this server's backups, the other server ids at the
// destination, and the warnings the dashboard shows.
func (s *Service) List(ctx context.Context) (Listing, error) {
	listing, err := s.list(ctx)
	if err == nil && listing.Enabled && (listing.LastSuccessAt == nil || s.opts.Now().Sub(*listing.LastSuccessAt) > lastSuccessWarnAge) {
		listing.Warnings = append(listing.Warnings, Warning{Code: WarningNoRecentBackup, Message: "No backup has completed in the last 48 hours."})
	}
	return listing, err
}

func (s *Service) list(ctx context.Context) (Listing, error) {
	listing := Listing{Backups: []Summary{}, OtherServerIDs: []string{}, Warnings: []Warning{}, KeyCheck: s.opts.KeyCheck, Destination: DestinationDefault}
	var err error
	if listing.Enabled, err = s.settingBool(ctx, config.BackupEnabledSettingKey, true); err != nil {
		return listing, err
	}
	if !listing.Enabled {
		listing.Warnings = append(listing.Warnings, Warning{Code: WarningDisabled, Message: "Nightly backups are off."})
	}
	if s.sqliteUserStore() {
		listing.Warnings = append(listing.Warnings, Warning{Code: WarningSQLiteUserStore, Message: ErrSQLiteUserStore.Error()})
	}
	s.toolWarnings(ctx, &listing)
	if listing.Keep, err = s.settingKeep(ctx); err != nil {
		return listing, err
	}
	if err := s.readStatus(ctx, &listing); err != nil {
		return listing, err
	}
	serverID, err := s.serverID(ctx)
	if err != nil {
		return listing, err
	}
	listing.ServerID = serverID
	t, err := s.target(ctx)
	if err != nil {
		// A destination that is not set up is the page's warning.
		listing.Warnings = append(listing.Warnings, Warning{Code: WarningDestinationUnavailable, Message: err.Error()})
		return listing, nil //nolint:nilerr // reported as the warning above
	}
	// An unreachable destination is a warning on the page, not a failed read.
	if !reachable(ctx, t) {
		listing.Warnings = append(listing.Warnings, Warning{Code: WarningDestinationUnavailable, Message: "The backup destination cannot be reached."})
		return listing, nil
	}
	listing.DestinationAvailable = true
	objects, err := t.List(ctx, RootPrefix)
	if err != nil {
		return listing, err
	}
	listing.OtherServerIDs = otherServerIDs(objects, serverID)
	folders, err := s.folders(ctx, t, serverID)
	if err != nil {
		return listing, err
	}
	for _, f := range folders {
		summary := Summary{ID: f.id, Kind: f.kind, CreatedAt: f.at, SizeBytes: f.size, MissingBlobs: []MissingBlob{}, Objects: []ObjectEntry{},
			Location: t.Location(folderPrefix(serverID, f.id))}
		switch {
		case f.manifest != nil:
			s.fromManifest(&summary, f.manifest)
			completed := f.manifest.CompletedAt
			if listing.LastSuccessAt == nil || completed.After(*listing.LastSuccessAt) {
				listing.LastSuccessAt = &completed
			}
		case listing.Active != nil && listing.Active.Operation == OperationBackup && listing.Active.BackupID == f.id:
			summary.Status = StatusInProgress
		default:
			summary.Status = StatusIncomplete
		}
		listing.Backups = append(listing.Backups, summary)
	}
	return listing, nil
}

// otherServerIDs lists the server ids other than serverID with backups at
// a destination, from a listing of RootPrefix.
func otherServerIDs(objects []blobstore.ObjectInfo, serverID string) []string {
	others := map[string]bool{}
	for _, obj := range objects {
		sid, _, ok := strings.Cut(strings.TrimPrefix(obj.Key, RootPrefix), "/")
		if ok && sid != serverID && validServerID(sid) {
			others[sid] = true
		}
	}
	out := make([]string, 0, len(others))
	for sid := range others {
		out = append(out, sid)
	}
	sort.Strings(out)
	return out
}

// fromManifest fills a summary from its folder's manifest.
func (s *Service) fromManifest(summary *Summary, m *Manifest) {
	completed := m.CompletedAt
	summary.Status, summary.CompletedAt, summary.SiloVersion = m.Status(), &completed, m.SiloVersion
	summary.SizeBytes, summary.MissingBlobs, summary.Objects, summary.BlobCount = m.Size(), m.MissingBlobs, m.Objects, m.Blobs.Count
	summary.KeyCheck, summary.KeyMatches = m.KeyCheck, m.KeyCheck != "" && m.KeyCheck == s.opts.KeyCheck
	if summary.MissingBlobs == nil {
		summary.MissingBlobs = []MissingBlob{}
	}
}

// readStatus fills busy, the active operation, and the latest run. The row is
// read before the lock's holder, and its active operation is believed only
// while the session it names still holds the lock: a row left by a holder
// that died, even while an unrelated operation holds the lock now, describes
// an interrupted run. The next holder records it as one (StatusStore.Begin).
func (s *Service) readStatus(ctx context.Context, listing *Listing) error {
	var rec StatusRecord
	if s.opts.Status != nil {
		var err error
		if rec, err = s.opts.Status.Read(ctx); err != nil {
			return fmt.Errorf("reading backup status: %w", err)
		}
	}
	var holder LockSession
	if s.opts.Locker != nil {
		session, held, err := s.opts.Locker.Holder(ctx)
		if err != nil {
			return err
		}
		listing.Busy, holder = held, session
	}
	listing.LastRun, listing.LastSuccessAt = rec.Last, rec.LastSuccessAt
	switch a := rec.Active; {
	case a == nil:
	case listing.Busy && a.Lock.Same(holder):
		listing.Active = a
	case a.Operation == OperationBackup:
		listing.LastRun = &LastRun{ID: a.BackupID, Kind: a.Kind, Status: RunInterrupted, StartedAt: a.StartedAt, Error: interruptedRunError}
	}
	return nil
}

func reachable(ctx context.Context, t target) bool {
	err := t.Probe(ctx)
	if err != nil {
		slog.WarnContext(ctx, "backup destination unreachable", "component", "backup", "error", err)
	}
	return err == nil
}

func (s *Service) toolWarnings(ctx context.Context, listing *Listing) {
	tools, err := s.findTools(ctx)
	if err != nil {
		listing.Warnings = append(listing.Warnings, Warning{Code: WarningToolsMissing, Message: "pg_dump is not installed on this server."})
		return
	}
	if s.opts.Pool == nil {
		return
	}
	var versionNum int
	if err := s.opts.Pool.QueryRow(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&versionNum); err != nil {
		return
	}
	if err := CheckToolsFor(tools, versionNum/10000); err != nil {
		listing.Warnings = append(listing.Warnings, Warning{Code: WarningToolsTooOld, Message: err.Error()})
	}
}
