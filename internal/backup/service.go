package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"maps"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/buildinfo"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

var (
	// ErrSQLiteUserStore refuses a backup that could not include user data.
	ErrSQLiteUserStore = errors.New("Backups need the PostgreSQL user store. Import your SQLite user store first.") //nolint:staticcheck // shown to admins as written
	// ErrNoDestination reports that the default destination does not exist:
	// backups are written to the operational store on this server's local
	// storage, and an S3 artwork backend has none.
	ErrNoDestination = errors.New("this server has no local storage for backups; backups need local artwork storage")
	// ErrNotEnoughSpace refuses a local backup that could fill the disk the
	// database shares.
	ErrNotEnoughSpace = errors.New("not enough free space at the backup destination")
)

const (
	// lastSuccessWarnAge is when the Backups page warns that no backup has
	// completed recently.
	lastSuccessWarnAge = 48 * time.Hour
	// freeSpaceMargin is added to 1.5 times the previous backup's size.
	freeSpaceMargin = 1 << 30
	// stderrLimit bounds the pg_dump and pg_restore output kept for errors.
	stderrLimit = 8 << 10
)

// SettingsReader reads server_settings, decrypted.
type SettingsReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// TaskStarter starts the backup task on this process.
type TaskStarter interface {
	StartTask(key string) (taskmanager.TaskInfo, error)
}

// Options configure a Service.
type Options struct {
	Pool *pgxpool.Pool
	// DatabaseURL is the DATABASE_URL pg_dump connects with.
	DatabaseURL string
	Settings    SettingsReader
	// Stores are the blob stores backups copy blobs from. Destination is
	// the default destination, the operational store on local storage; nil
	// when none exists.
	Stores      blobstore.Stores
	Destination Destination
	Locker      Locker
	ServerID    func(context.Context) (string, error)
	// KeyCheck is secret.KeyCheckValue of this server's SECRET_KEY.
	KeyCheck string
	// Cipher is this server's data cipher. A restore decrypts a backup's
	// encrypted settings with it when the key check values match.
	Cipher *secret.Cipher
	// UserStoreBackend reports userdb.backend; backups refuse "sqlite".
	UserStoreBackend func() string
	// Status publishes the active operation and the latest run for every
	// API server. Nil publishes nothing.
	Status StatusStore
	// FindTools overrides tool discovery for tests.
	FindTools func(context.Context) (Tools, error)
	Now       func() time.Time
}

// Service owns taking, listing, and deleting backups.
type Service struct {
	opts    Options
	stores  blobStores
	starter TaskStarter

	toolsMu sync.Mutex
	tools   *Tools

	pendingMu sync.Mutex
	pending   *pendingRun

	targets RestoreTargets
	// restoreSets shares validated restore sets among previews.
	restoreSets restoreSetCache
}

// pendingRun hands a lock taken by StartManual to the task run it starts.
type pendingRun struct {
	id   string
	kind Kind
	held Held
}

// New builds a Service.
func New(opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.FindTools == nil {
		opts.FindTools = FindTools
	}
	stores := blobStores{}
	if opts.Stores.Assets != nil {
		stores[storeAssets] = opts.Stores.Assets
	}
	if opts.Stores.Operational != nil {
		stores[storeOperational] = opts.Stores.Operational
	}
	return &Service{opts: opts, stores: stores}
}

// Configured reports whether the destination exists: this server has an
// operational store on local storage. It probes nothing.
func (s *Service) Configured() bool { return s.opts.Destination != nil }

// Destinations lists the destinations this server can write to.
func (s *Service) Destinations() []string {
	if s.opts.Destination == nil {
		return nil
	}
	return []string{DestinationDefault}
}

// SetTaskStarter wires "Back up now" to the task manager, which is built
// after the service.
func (s *Service) SetTaskStarter(starter TaskStarter) { s.starter = starter }

// RunResult is a finished run, recorded as the task's result data.
type RunResult struct {
	ID           string        `json:"id,omitempty"`
	Kind         Kind          `json:"kind,omitempty"`
	Status       Status        `json:"status,omitempty"`
	SizeBytes    int64         `json:"size_bytes,omitempty"`
	DurationMs   int64         `json:"duration_ms,omitempty"`
	MissingBlobs []MissingBlob `json:"missing_blobs,omitempty"`
	Rotated      []string      `json:"rotated,omitempty"`
	Swept        []string      `json:"swept,omitempty"`
	Skipped      string        `json:"skipped,omitempty"`
	Warnings     []string      `json:"warnings,omitempty"`
}

// SkippedBusy is the reason a scheduled run records when the lock is held.
const SkippedBusy = "skipped: another backup or restore is running"

// StartManual takes the lock and starts the backup task on this process with
// it, so "Back up now" either starts a backup or answers ErrBusy: there is no
// window in which another server could take the lock after the check.
func (s *Service) StartManual(ctx context.Context) (string, error) {
	if s.starter == nil {
		return "", errors.New("backup task is not registered")
	}
	if _, err := s.preflight(ctx); err != nil {
		return "", err
	}
	held, err := s.opts.Locker.TryLock(ctx)
	if err != nil {
		return "", err
	}
	// The id is reserved under the lock the run inherits, so the id
	// returned is the folder the backup writes.
	prep, err := s.prepareBackup(ctx, KindManual, "", s.opts.Now())
	if err != nil {
		held.Release(ctx)
		return "", err
	}
	id := prep.id
	s.pendingMu.Lock()
	s.pending = &pendingRun{id: id, kind: KindManual, held: held}
	s.pendingMu.Unlock()
	if _, err := s.starter.StartTask(TaskKey); err != nil {
		if run := s.takePending(); run != nil {
			run.held.Release(ctx)
			if errors.Is(err, taskmanager.ErrTaskAlreadyRunning) {
				return "", ErrBusy
			}
			return "", err
		}
		// A run that had already reserved the worker took the lock and is
		// backing up under this id.
	}
	return id, nil
}

func (s *Service) takePending() *pendingRun {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	run := s.pending
	s.pending = nil
	return run
}

// RunTask is the task body: a manual run uses the lock StartManual handed
// over; a scheduled run takes the lock itself and records a skip when another
// backup or restore holds it.
func (s *Service) RunTask(ctx context.Context, progress func(float64, string)) (RunResult, error) {
	run := s.takePending()
	if run == nil {
		kind := KindNightly
		if taskmanager.StartedManually(ctx) {
			kind = KindManual
		}
		held, err := s.opts.Locker.TryLock(ctx)
		if errors.Is(err, ErrBusy) {
			return RunResult{Skipped: SkippedBusy}, nil
		}
		if err != nil {
			return RunResult{}, err
		}
		run = &pendingRun{held: held, kind: kind}
	}
	held, id, kind := run.held, run.id, run.kind
	defer held.Release(context.WithoutCancel(ctx))
	ctx, cancel := withLost(ctx, held)
	defer cancel()
	return s.Backup(ctx, held, kind, id, progress)
}

// Backup takes one backup under held, the subsystem lock the caller holds; a
// restore calls this directly for its before-restore copy. id may be empty.
// On success rotation and the sweep run; a failed backup deletes nothing. The
// run is published as the active operation and then as the latest run.
func (s *Service) Backup(ctx context.Context, held Held, kind Kind, id string, progress func(float64, string)) (RunResult, error) {
	return s.backupWith(ctx, held, kind, id, progress, nil)
}

// backupWith is Backup with ids its rotation must keep: a restore pins its
// source, so the before-restore backup it takes cannot rotate it away.
func (s *Service) backupWith(ctx context.Context, held Held, kind Kind, id string, progress func(float64, string), pinned map[string]bool) (RunResult, error) {
	if progress == nil {
		progress = func(float64, string) {}
	}
	started := s.opts.Now()
	prep, prepErr := s.prepareBackup(ctx, kind, id, started)
	if prepErr == nil {
		id = prep.id
	} else if id == "" {
		id = FolderName(started, kind)
	}
	op := s.beginOperation(ctx, held, ActiveOperation{Operation: OperationBackup, BackupID: id, Kind: kind, StartedAt: started})
	report := func(percent float64, message string) {
		progress(percent, message)
		op.progress(ctx, percent, message)
	}
	result, err := RunResult{}, prepErr
	if err == nil {
		result, err = s.backup(ctx, prep, kind, started, report, pinned)
	}
	last := &LastRun{ID: id, Kind: kind, StartedAt: started, FinishedAt: s.opts.Now(), Status: string(result.Status)}
	if err != nil {
		last.Status, last.Error = RunFailed, err.Error()
	}
	op.end(ctx, last)
	return result, err
}

// backupPrep is what a backup resolves before it writes: the destination,
// this server's folder there, the folders already in it, and the reserved
// id of the new one.
type backupPrep struct {
	t        target
	serverID string
	folders  []folder
	id       string
}

// prepareBackup resolves the destination and reserves the backup's id under
// the subsystem lock the caller holds. A requested id (a manual run's,
// reserved when it was queued) is kept unless a folder took its time since,
// which only a change of destination could do; then another is reserved.
func (s *Service) prepareBackup(ctx context.Context, kind Kind, id string, started time.Time) (backupPrep, error) {
	t, err := s.preflight(ctx)
	if err != nil {
		return backupPrep{}, err
	}
	serverID, err := s.serverID(ctx)
	if err != nil {
		return backupPrep{}, err
	}
	folders, err := s.folders(ctx, t, serverID)
	if err != nil {
		return backupPrep{}, fmt.Errorf("listing existing backups: %w", err)
	}
	at, requestedKind, ok := ParseFolderName(id)
	if !ok || requestedKind != kind {
		at = started
	}
	reserved := reserveID(folders, at, kind)
	if ok && reserved != id {
		slog.WarnContext(ctx, "the reserved backup id is taken at the destination; using another", "component", "backup", "id", id, "now", reserved)
	}
	return backupPrep{t: t, serverID: serverID, folders: folders, id: reserved}, nil
}

// publishedOperation is one operation's status writes. It writes only while
// its lock is held, and every write after Begin carries the operation's
// token, so a holder that lost the lock cannot touch what the next holder
// published. A failed write is logged: the status reports the work, it is
// never a reason to stop it.
type publishedOperation struct {
	store StatusStore
	held  Held
	token string
}

// beginOperation publishes op for the holder of held. Begin also records an
// active operation left by a holder that died as an interrupted run.
func (s *Service) beginOperation(ctx context.Context, held Held, op ActiveOperation) *publishedOperation {
	p := &publishedOperation{store: s.opts.Status, held: held}
	if p.store == nil || held == nil || p.lost(ctx) {
		return p
	}
	op.Lock = held.Session()
	token, err := p.store.Begin(ctx, held, op)
	if err != nil {
		slog.WarnContext(ctx, "publishing backup status failed", "component", "backup", "error", err)
		return p
	}
	p.token = token
	return p
}

func (p *publishedOperation) lost(ctx context.Context) bool {
	if lockLost(ctx) {
		return true
	}
	select {
	case <-p.held.Lost():
		return true
	default:
		return false
	}
}

func (p *publishedOperation) progress(ctx context.Context, percent float64, message string) {
	if p.token == "" || p.lost(ctx) {
		return
	}
	if err := p.store.Progress(ctx, p.held, p.token, percent, message); err != nil {
		slog.WarnContext(ctx, "publishing backup progress failed", "component", "backup", "error", err)
	}
}

func (p *publishedOperation) end(ctx context.Context, last *LastRun) {
	if p.token == "" || p.lost(ctx) {
		return
	}
	if err := p.store.End(context.WithoutCancel(ctx), p.held, p.token, last); err != nil {
		slog.WarnContext(ctx, "publishing the backup outcome failed", "component", "backup", "error", err)
	}
}

func (s *Service) backup(ctx context.Context, prep backupPrep, kind Kind, started time.Time, progress func(float64, string), pinned map[string]bool) (RunResult, error) {
	serverID, id := prep.serverID, prep.id
	tools, err := s.findTools(ctx)
	if err != nil {
		return RunResult{}, err
	}
	t := prep.t
	if err := checkFreeSpace(ctx, t, prep.folders); err != nil {
		return RunResult{}, err
	}
	includeFingerprints, includeSubtitles, err := s.contentSettings(ctx)
	if err != nil {
		return RunResult{}, err
	}
	excluded := excludedTableData(includeFingerprints)

	progress(5, "Opening a database snapshot")
	conn, err := s.opts.Pool.Acquire(ctx)
	if err != nil {
		return RunResult{}, fmt.Errorf("opening a database connection: %w", err)
	}
	defer conn.Release()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return RunResult{}, fmt.Errorf("opening a snapshot transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var snapshot string
	if err := tx.QueryRow(ctx, `SELECT pg_export_snapshot()`).Scan(&snapshot); err != nil {
		return RunResult{}, fmt.Errorf("exporting the snapshot: %w", err)
	}
	facts, err := readSnapshot(ctx, tx, includeSubtitles)
	if err != nil {
		return RunResult{}, err
	}
	if err := CheckToolsFor(tools, facts.serverMajor); err != nil {
		return RunResult{}, err
	}

	progress(10, "Dumping the database")
	dumpEntry, tocMode, err := s.dump(ctx, t, tools, snapshot, excluded, objectKey(serverID, id, DumpName))
	if err != nil {
		return RunResult{}, err
	}
	created := folderTime(id, started)
	closeSnapshot := sync.OnceFunc(func() { _ = tx.Rollback(ctx); conn.Release() })
	objects, restoreSet, err := s.restoreSetFor(ctx, t, tx, closeSnapshot, serverID, id, created, progress)
	if err != nil {
		return RunResult{}, err
	}
	objects = append([]ObjectEntry{dumpEntry}, objects...)

	progress(70, fmt.Sprintf("Copying %d referenced files", len(facts.blobs)))
	blobEntry, copied, err := s.writeBlobs(ctx, t, objectKey(serverID, id, BlobsName), facts.blobs)
	if err != nil {
		return RunResult{}, err
	}

	manifest := &Manifest{
		Format: manifestFormat, Version: manifestVersion,
		ID: id, Kind: kind, ServerID: serverID,
		CreatedAt: created, CompletedAt: s.opts.Now().UTC(),
		SiloVersion: buildinfo.Current().Display, GooseVersion: facts.gooseVersion,
		AppliedMigrations: facts.applied,
		PostgresVersion:   facts.postgresVersion, PgDumpVersion: tools.Version,
		ExcludedTableData: excluded,
		Objects:           append(objects, blobEntry),
		RestoreSet:        restoreSet,
		KeyCheck:          s.opts.KeyCheck,
		TOCCheck:          tocMode,
		Blobs:             BlobSummary{Count: copied.count, Bytes: copied.bytes, IncludesSubtitles: includeSubtitles},
		MissingBlobs:      copied.missing,
		Tables:            facts.tables,
	}
	progress(95, "Writing the manifest")
	if err := writeManifest(ctx, t, manifest); err != nil {
		return RunResult{}, err
	}
	result := RunResult{ID: id, Kind: kind, Status: manifest.Status(), SizeBytes: manifest.Size(), MissingBlobs: manifest.MissingBlobs}
	result.Rotated, result.Swept, result.Warnings = s.rotate(ctx, t, serverID, pinned)
	result.DurationMs = s.opts.Now().Sub(started).Milliseconds()
	progress(100, "Backed up "+id)
	return result, nil
}

// writeManifest writes m last of a backup's objects: its presence completes
// the backup.
func writeManifest(ctx context.Context, t target, m *Manifest) error {
	if m.MissingBlobs == nil {
		m.MissingBlobs = []MissingBlob{}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := t.Put(ctx, objectKey(m.ServerID, m.ID, ManifestName), bytes.NewReader(data)); err != nil {
		return fmt.Errorf("writing the manifest: %w", err)
	}
	return nil
}

// preflight refuses what no backup on this server could survive, and
// returns the destination a backup starting now writes to.
func (s *Service) preflight(ctx context.Context) (target, error) {
	if s.sqliteUserStore() {
		return target{}, ErrSQLiteUserStore
	}
	return s.target(ctx)
}

func (s *Service) sqliteUserStore() bool {
	return s.opts.UserStoreBackend != nil && strings.EqualFold(strings.TrimSpace(s.opts.UserStoreBackend()), "sqlite")
}

func (s *Service) serverID(ctx context.Context) (string, error) {
	if s.opts.ServerID == nil {
		return "", errors.New("server identity unavailable")
	}
	id, err := s.opts.ServerID(ctx)
	if err != nil {
		return "", fmt.Errorf("reading the server identity: %w", err)
	}
	if !validServerID(id) {
		return "", fmt.Errorf("server identity %q cannot name a backup folder", id)
	}
	return id, nil
}

// findTools caches a successful discovery for the process.
func (s *Service) findTools(ctx context.Context) (Tools, error) {
	s.toolsMu.Lock()
	defer s.toolsMu.Unlock()
	if s.tools != nil {
		return *s.tools, nil
	}
	tools, err := s.opts.FindTools(ctx)
	if err != nil {
		return Tools{}, err
	}
	s.tools = &tools
	return tools, nil
}

// checkFreeSpace requires 1.5 times the previous backup plus 1 GB free on a
// local destination. The default destination shares a disk with the
// database, and filling it would take Postgres down.
func checkFreeSpace(ctx context.Context, t target, folders []folder) error {
	free, ok, err := t.FreeBytes(ctx)
	if err != nil || !ok {
		return err
	}
	var previous int64
	var newest time.Time
	for _, f := range folders {
		if f.manifest != nil && f.at.After(newest) {
			previous, newest = f.manifest.Size(), f.at
		}
	}
	need := uint64(previous)*3/2 + freeSpaceMargin
	if free < need {
		return fmt.Errorf("%w: %s free, %s needed (1.5 times the previous backup plus 1 GB)", ErrNotEnoughSpace, formatBytes(free), formatBytes(need))
	}
	return nil
}

// writeBlobs streams blobs.tar into the destination while hashing it.
func (s *Service) writeBlobs(ctx context.Context, t target, key string, refs []blobRef) (ObjectEntry, blobCopyResult, error) {
	pr, pw := io.Pipe()
	var copied blobCopyResult
	done := make(chan error, 1)
	go func() {
		result, err := writeBlobTar(ctx, pw, s.stores, refs)
		copied = result
		_ = pw.CloseWithError(err)
		done <- err
	}()
	entry, putErr := t.put(ctx, key, pr)
	_ = pr.CloseWithError(errors.New("blob upload ended"))
	copyErr := <-done
	if copyErr != nil {
		return ObjectEntry{}, copied, fmt.Errorf("copying blobs: %w", copyErr)
	}
	if putErr != nil {
		return ObjectEntry{}, copied, fmt.Errorf("writing blobs.tar: %w", putErr)
	}
	return entry, copied, nil
}

// folderTime is the backup's creation time: the time its id names.
func folderTime(id string, started time.Time) time.Time {
	if at, _, ok := ParseFolderName(id); ok {
		return at
	}
	return started.UTC()
}

// restoreSetFor writes restore-set.tar from the snapshot and then calls
// closeSnapshot: the dump has imported the snapshot, and the blob copies run
// outside it.
func (s *Service) restoreSetFor(ctx context.Context, t target, tx pgx.Tx, closeSnapshot func(), serverID, id string, created time.Time, progress func(float64, string)) ([]ObjectEntry, *RestoreSetSummary, error) {
	progress(60, "Writing the restore set")
	entry, summary, err := s.writeRestoreSet(ctx, t, tx, closeSnapshot, objectKey(serverID, id, RestoreSetName),
		restoreSetMeta{serverID: serverID, keyCheck: s.opts.KeyCheck, createdAt: created, version: buildinfo.Current().Display})
	closeSnapshot()
	if err != nil {
		return nil, nil, err
	}
	return []ObjectEntry{entry}, summary, nil
}

// writeRestoreSet streams restore-set.tar into the destination while
// hashing it. Its documents and plugin archives come from the snapshot
// transaction; closeSnapshot then ends it, and the section blobs are copied
// after, as blobs.tar's are.
func (s *Service) writeRestoreSet(ctx context.Context, t target, tx pgx.Tx, closeSnapshot func(), key string, meta restoreSetMeta) (ObjectEntry, *RestoreSetSummary, error) {
	build, err := buildRestoreSet(ctx, tx, meta)
	if err != nil {
		return ObjectEntry{}, nil, err
	}
	pr, pw := io.Pipe()
	var entry ObjectEntry
	putDone := make(chan error, 1)
	go func() {
		var err error
		entry, err = t.put(ctx, key, pr)
		_ = pr.CloseWithError(errors.New("restore set upload ended"))
		putDone <- err
	}()
	tw := tar.NewWriter(pw)
	err = build.writeSnapshotPart(ctx, tw, tx)
	closeSnapshot()
	if err == nil {
		err = build.writeBlobPart(ctx, tw, s.stores)
	}
	if err == nil {
		err = tw.Close()
	}
	_ = pw.CloseWithError(err)
	putErr := <-putDone
	if err != nil {
		return ObjectEntry{}, nil, fmt.Errorf("writing the restore set: %w", err)
	}
	if putErr != nil {
		return ObjectEntry{}, nil, fmt.Errorf("writing %s: %w", RestoreSetName, putErr)
	}
	return entry, build.summary(), nil
}

// rotate runs rotation and the incomplete-folder sweep after a manifest
// landed, under the subsystem lock.
// pinned and the source of a queued or running restore are never rotated
// away; when that restore cannot be read, nothing is. Failures are warnings:
// the backup itself succeeded.
func (s *Service) rotate(ctx context.Context, t target, serverID string, pinned map[string]bool) (rotated, swept, warnings []string) {
	folders, err := s.folders(ctx, t, serverID)
	if err != nil {
		return nil, nil, []string{"rotation skipped: " + err.Error()}
	}
	now := s.opts.Now()
	keep, err := s.settingKeep(ctx)
	if err != nil {
		return nil, nil, []string{"rotation skipped: " + err.Error()}
	}
	// A queued restore's source is kept like the running one's: restores are
	// queued under the lock this rotation holds.
	active, err := s.activeRestoreSource(ctx)
	if err != nil {
		return nil, nil, []string{"rotation skipped: " + err.Error()}
	}
	if active.backupID != "" {
		pinned = maps.Clone(pinned)
		if pinned == nil {
			pinned = map[string]bool{}
		}
		pinned[active.backupID] = true
	}
	for _, id := range planRotation(folders, keep, now, pinned) {
		if err := t.DeletePrefix(ctx, folderPrefix(serverID, id)); err != nil {
			warnings = append(warnings, fmt.Sprintf("could not delete %s: %v", id, err))
			continue
		}
		rotated = append(rotated, id)
	}
	for _, id := range planSweep(folders, now) {
		if err := t.DeletePrefix(ctx, folderPrefix(serverID, id)); err != nil {
			warnings = append(warnings, fmt.Sprintf("could not delete incomplete %s: %v", id, err))
			continue
		}
		swept = append(swept, id)
	}
	for _, w := range warnings {
		slog.WarnContext(ctx, "backup rotation", "component", "backup", "warning", w)
	}
	return rotated, swept, warnings
}

// folders lists this server's backup folders at t, reading each manifest.
func (s *Service) folders(ctx context.Context, t target, serverID string) ([]folder, error) {
	objects, err := t.List(ctx, serverPrefix(serverID))
	if err != nil {
		return nil, err
	}
	byID := map[string]*folder{}
	var order []string
	for _, obj := range objects {
		rest := strings.TrimPrefix(obj.Key, serverPrefix(serverID))
		id, name, ok := strings.Cut(rest, "/")
		if !ok {
			continue
		}
		at, kind, ok := ParseFolderName(id)
		if !ok {
			continue
		}
		f := byID[id]
		if f == nil {
			f = &folder{id: id, kind: kind, at: at}
			byID[id] = f
			order = append(order, id)
		}
		f.size += obj.Size
		if obj.ModTime.After(f.newest) {
			f.newest = obj.ModTime
		}
		if name == ManifestName {
			m, err := readManifest(ctx, t, obj.Key)
			if err != nil {
				slog.WarnContext(ctx, "unreadable backup manifest", "component", "backup", "id", id, "error", err)
				continue
			}
			f.manifest = m
		}
	}
	out := make([]folder, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].at.After(out[j].at) })
	return out, nil
}

func readManifest(ctx context.Context, t target, key string) (*Manifest, error) {
	body, _, err := t.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, 16<<20))
	if err != nil {
		return nil, err
	}
	return ParseManifest(data)
}

// target is the destination one operation uses.
type target struct {
	Destination
}

// put streams r into key and returns the stored object's entry: its name,
// size, and SHA-256.
func (t target) put(ctx context.Context, key string, r io.Reader) (ObjectEntry, error) {
	sum := newSum()
	if err := t.Put(ctx, key, io.TeeReader(r, sum)); err != nil {
		return ObjectEntry{}, err
	}
	return sum.entry(path.Base(key)), nil
}

// sum hashes and counts the bytes written through it.
type sum struct {
	h hash.Hash
	n int64
}

func newSum() *sum { return &sum{h: sha256.New()} }

func (s *sum) Write(p []byte) (int, error) {
	s.n += int64(len(p))
	return s.h.Write(p)
}

func (s *sum) entry(name string) ObjectEntry {
	return ObjectEntry{Name: name, Size: s.n, SHA256: hex.EncodeToString(s.h.Sum(nil))}
}

func formatBytes(n uint64) string {
	const unit = 1 << 30
	return fmt.Sprintf("%.1f GB", float64(n)/unit)
}
