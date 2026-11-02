package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// ExecuteRestore runs one backup_restore job:
//
//  1. take the subsystem lock and hold it for the whole run;
//  2. read the source's restore set (a backup's, or an upload's) and
//     refuse it when its digest is not the one the preview returned;
//  3. take a before-restore backup by calling Backup under the same lock,
//     keeping the source out of its rotation, and record its id; if it
//     fails nothing is restored;
//  4. apply the chosen items section by section (libraries, plugins,
//     settings, collections, home sections, users), each in its own fenced
//     transaction that also records the item's outcome, publishing events
//     after each commit;
//  5. stop between items when the job is canceled or its claim is lost.
//
// Every item is matched again when it is applied, so a restore run twice,
// or again after an interruption, reports what already committed as
// unchanged. The returned report is the job's result.
func (s *Service) ExecuteRestore(ctx context.Context, run RestoreRun) (any, error) {
	var req RestoreRequest
	report := &SelectiveReport{LibraryMap: map[string]int{}, Items: []ReportItem{}, Counts: map[string]int{}, StartedAt: s.opts.Now().UTC()}
	fail := func(err error) (any, error) {
		report.Error, report.ErrorCode = err.Error(), restoreErrorCode(err)
		return report, err
	}
	if err := json.Unmarshal(run.Payload, &req); err != nil {
		return fail(fmt.Errorf("reading the restore request: %w", err))
	}
	report.BackupID, report.UploadID, report.Digest = req.BackupID, req.UploadID, req.Digest
	if s.opts.Locker == nil {
		return fail(errors.New("backups are not available on this server"))
	}
	held, err := s.opts.Locker.TryLock(ctx)
	if err != nil {
		if errors.Is(err, ErrBusy) {
			return fail(fmt.Errorf("%w; start the restore again when it finishes", ErrBusy))
		}
		return fail(err)
	}
	defer held.Release(context.WithoutCancel(ctx))
	// Items run on a context the job's cancellation does not reach, so a
	// cancel stops the restore between items, never inside one; a lost lock
	// still stops everything.
	work, cancelWork := withLost(context.WithoutCancel(ctx), held)
	defer cancelWork()

	rs, err := s.openRestoreSet(work, req.source(), true)
	if err != nil {
		return fail(err)
	}
	defer rs.Cleanup()
	if rs.Digest != req.Digest {
		return fail(ErrDigestMismatch)
	}

	a := &applier{s: s, rs: rs, run: run, report: report, total: 1 + len(req.Users), users: map[string]UserSelection{}, staged: map[string]error{}}
	for _, ids := range req.Selection {
		a.total += len(ids)
	}
	for _, u := range req.Users {
		a.users[u.Account] = u
	}
	if len(req.Users) > 0 || len(req.Selection[SectionHomeSections]) > 0 {
		if a.serverID, err = s.serverID(work); err != nil {
			return fail(err)
		}
	}
	// An uploaded source is not a backup of this server: rotation never
	// sees it, and its delete waits for the lock this restore holds.
	pinned := map[string]bool{}
	if req.BackupID != "" {
		pinned[req.BackupID] = true
	}
	result, err := s.backupWith(work, held, KindBeforeRestore, "", nil, pinned)
	if err != nil {
		return fail(fmt.Errorf("%w: %w", errBeforeRestoreBackup, err))
	}
	// Follow-ups an earlier restore committed but never finished run first.
	a.s.CompleteFollowUps(work)
	if err := a.seedLibraryMap(work); err != nil {
		return fail(err)
	}
	a.current = 1
	next := a.clone()
	next.BeforeRestoreBackupID = result.ID
	if err := a.commit(work, next, "Backed up before restoring", nil); err != nil {
		return fail(err)
	}
	op := s.beginOperation(work, held, ActiveOperation{Operation: OperationRestore, BackupID: req.BackupID, UploadID: req.UploadID, StartedAt: s.opts.Now()})
	defer op.end(work, nil)

	for _, spec := range restoreSections {
		ids := req.Selection[spec.id]
		if spec.id == SectionUsers {
			ids = nil
			for _, u := range req.Users {
				ids = append(ids, u.Account)
			}
		}
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return report, err
			}
			if err := work.Err(); err != nil {
				return fail(context.Cause(work))
			}
			if err := a.apply(work, spec.id, id); err != nil {
				return fail(err)
			}
			op.progress(work, float64(a.current)*100/float64(a.total), "Restoring "+spec.id)
		}
	}
	finished := s.opts.Now().UTC()
	report.FinishedAt = &finished
	return report, nil
}

// applier applies items, one fenced transaction each.
type applier struct {
	s  *Service
	rs *RestoreSet
	// serverID is this server's identity; users match by id only when the
	// backup is this server's.
	serverID string
	// users are the chosen accounts by backup account id.
	users  map[string]UserSelection
	run    RestoreRun
	report *SelectiveReport
	// current and total count items for the job's progress; the
	// before-restore backup is the first.
	current, total int
	// claimed maps "kind:local id" to the item that wrote it, so two items
	// never write the same section or collection here.
	claimed map[string]string
	// adminCollections caches how the accounts' data maps admin collection
	// ids (collectionMap).
	adminCollections map[string]string
	// allowUnrated is access.unrated_content, read before each account's
	// transaction: the policy reader takes a pool connection of its own,
	// which the transaction may hold the last of.
	allowUnrated bool
	// staged holds, per artwork item, whether stageArtwork wrote its
	// objects (nil) or why it could not; an item it skipped is absent.
	staged map[string]error
}

// applied is one item's outcome before its transaction commits.
type applied struct {
	item  ReportItem
	after func(context.Context)
	// mapLibrary records a backup library id and the library it became.
	mapLibrary [2]int
}

func (a *applier) clone() *SelectiveReport {
	next := *a.report
	next.Items = slices.Clone(a.report.Items)
	next.Counts = maps.Clone(a.report.Counts)
	next.LibraryMap = maps.Clone(a.report.LibraryMap)
	return &next
}

// commit records next as the job's result in a fenced transaction of its
// own, with fn's writes when fn is set.
func (a *applier) commit(ctx context.Context, next *SelectiveReport, message string, fn func(pgx.Tx) error) error {
	err := a.run.Fenced(ctx, func(tx pgx.Tx) (FencedUpdate, error) {
		if fn != nil {
			if err := fn(tx); err != nil {
				return FencedUpdate{}, err
			}
		}
		return FencedUpdate{Result: next, Current: a.current, Total: a.total, Message: message}, nil
	})
	if err != nil {
		return err
	}
	*a.report = *next
	if a.run.Published != nil {
		a.run.Published(ctx)
	}
	return nil
}

// apply runs one item in a savepoint of its fenced transaction. A failed
// item rolls back to the savepoint and is recorded as failed in the same
// transaction; the restore continues with the next item.
func (a *applier) apply(ctx context.Context, section, id string) error {
	a.current++
	switch {
	case section == SectionUsers:
		a.allowUnrated = a.s.opts.AllowUnratedContent != nil && a.s.opts.AllowUnratedContent(ctx)
	case section == SectionCollections && strings.HasPrefix(id, "artwork:"):
		a.staged[id] = a.stageArtwork(ctx, id)
	}
	var result applied
	next := a.clone()
	err := a.commit(ctx, next, "Restoring "+section, func(tx pgx.Tx) error {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		result, err = a.applyItem(ctx, sp, section, id)
		result.item.Section, result.item.ID = section, id
		if err != nil {
			_ = sp.Rollback(ctx)
			result.after, result.mapLibrary = nil, [2]int{}
			result.item.Outcome, result.item.Error = OutcomeFailed, err.Error()
		} else if err := sp.Commit(ctx); err != nil {
			return err
		}
		next.Items = append(next.Items, result.item)
		next.Counts[result.item.Outcome]++
		if result.mapLibrary[0] > 0 {
			next.LibraryMap[strconv.Itoa(result.mapLibrary[0])] = result.mapLibrary[1]
		}
		return nil
	})
	if err != nil {
		return err
	}
	if result.after != nil {
		result.after(ctx)
	}
	return nil
}

func (a *applier) applyItem(ctx context.Context, tx pgx.Tx, section, id string) (applied, error) {
	switch section {
	case SectionLibraries:
		return a.applyLibrary(ctx, tx, id)
	case SectionPlugins:
		return a.applyPlugin(ctx, tx, id)
	case SectionSettings:
		return a.applySettingsPage(ctx, tx, id)
	case SectionUsers:
		return a.applyAccount(ctx, tx, a.users[id])
	case SectionCollections:
		switch {
		case strings.HasPrefix(id, "collection:"):
			return a.applyCollection(ctx, tx, id)
		case strings.HasPrefix(id, "artwork:"):
			return a.applyArtwork(ctx, tx, id)
		case strings.HasPrefix(id, "branding:"):
			return a.applyBranding(ctx, tx, id)
		case strings.HasPrefix(id, "template:"):
			return a.applyTemplateCollection(ctx, tx, id)
		}
	case SectionHomeSections:
		return a.applyHomeSection(ctx, tx, id)
	}
	return leftOut(ReportItem{}, ReasonNotInBackup, "The backup has no such item."), nil
}

func leftOut(item ReportItem, reason, message string) applied {
	item.Outcome, item.Reason, item.Message = OutcomeLeftOut, reason, message
	return applied{item: item}
}

func unchanged(item ReportItem) applied {
	item.Outcome = OutcomeUnchanged
	return applied{item: item}
}

// leftOutFor turns a plan that cannot be applied into the report's reason.
func leftOutFor(item ReportItem, plan PreviewItem) applied {
	item.MissingBlobs = plan.MissingBlobs
	if plan.Status == ItemOnThisServer {
		return unchanged(item)
	}
	reason := plan.Reason
	if reason == "" {
		reason = ReasonChanged
	}
	message := plan.Message
	if message == "" {
		message = "It cannot be restored on this server."
	}
	return leftOut(item, reason, message)
}

// seedLibraryMap records every backup library that matches one here, so
// collections map to libraries that already exist even when the library
// itself was not chosen.
func (a *applier) seedLibraryMap(ctx context.Context) error {
	plans, err := a.s.newPlanner(a.rs, a.s.opts.Pool).planLibraries(ctx)
	if err != nil {
		return err
	}
	for _, p := range plans {
		if p.localID > 0 {
			a.report.LibraryMap[strconv.Itoa(p.entry.ID)] = p.localID
		}
	}
	return nil
}

func (a *applier) libraryMap() map[int]int {
	out := make(map[int]int, len(a.report.LibraryMap))
	for k, v := range a.report.LibraryMap {
		if id, err := strconv.Atoi(k); err == nil {
			out[id] = v
		}
	}
	return out
}

// ErrBlobConflict refuses a restore that would replace an existing object
// with other bytes.
var ErrBlobConflict = errors.New("a file already exists on this server with other content; nothing was overwritten")

// ErrBlobNotExclusive fails an item whose files the store cannot create
// without risking replacing one written meanwhile: a bucket that rejects
// conditional writes (blobstore.ErrExclusiveUnsupported). Nothing is
// written; other items go on.
var ErrBlobNotExclusive = errors.New("this storage can't guarantee existing files are kept; restore this item manually")

// writeBlob copies an extracted blob to the store its key belongs in. It
// never replaces an existing object: the write is an exclusive create
// (blobstore.CreateExclusive: a hard link on a local store, If-None-Match on
// S3), so a concurrent upload of the same key cannot be overwritten either.
// A store that cannot create exclusively fails the item (ErrBlobNotExclusive)
// rather than check and then write.
// When an object is there, one with the same bytes is left as it is and one
// with other bytes refuses the item (ErrBlobConflict). The keys a restore
// writes are content-addressed revisions (uploaded artwork, branding) or
// belong to an id the restore just took (library posters, collection
// images), so a legitimate restore finds them absent or equal; an archive
// naming a live asset cannot replace it, whether or not the item's
// transaction later commits.
func (a *applier) writeBlob(ctx context.Context, from, to string) error {
	return a.writeBlobTo(ctx, storeAssets, from, to)
}

// writeBlobTo is writeBlob into the named store.
func (a *applier) writeBlobTo(ctx context.Context, storeName, from, to string) error {
	store := a.s.stores[storeName]
	if store == nil {
		return fmt.Errorf("this server has no %s storage", storeName)
	}
	f, size, err := a.rs.openBlob(from)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	err = blobstore.CreateExclusive(ctx, store, to, f, blobstore.MediaType(to))
	switch {
	case err == nil:
		return nil
	case errors.Is(err, blobstore.ErrExclusiveUnsupported):
		return ErrBlobNotExclusive
	case !errors.Is(err, blobstore.ErrExists):
		return fmt.Errorf("writing %s: %w", to, err)
	}
	same, err := sameObject(ctx, store, to, f, size)
	if err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("%s: %w", to, ErrBlobConflict)
	}
	return nil
}

// sameObject compares a stored object with an extracted file by size and
// SHA-256.
func sameObject(ctx context.Context, store blobstore.Store, key string, f *os.File, size int64) (bool, error) {
	body, info, err := store.Get(ctx, key)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", key, err)
	}
	defer func() { _ = body.Close() }()
	if info.Size != size {
		return false, nil
	}
	stored, local := sha256.New(), sha256.New()
	if _, err := io.Copy(stored, body); err != nil {
		return false, fmt.Errorf("reading %s: %w", key, err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	if _, err := io.Copy(local, f); err != nil {
		return false, err
	}
	return bytes.Equal(stored.Sum(nil), local.Sum(nil)), nil
}

// ---- Follow-ups ----

// Follow-up kinds, as backup_restore_followups records them.
const (
	followUpLibraryCreated  = "library_created"
	followUpPluginInstalled = "plugin_installed"
	// followUpCollectionCreated stores a restored template collection's
	// template poster and syncs it; target_id is the collection id.
	followUpCollectionCreated = "collection_created"
)

// recordFollowUp notes, in the item's transaction, work owed after commit.
func recordFollowUp(ctx context.Context, tx pgx.Tx, kind string, id int) error {
	if _, err := tx.Exec(ctx, `INSERT INTO backup_restore_followups (kind, target_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, kind, id); err != nil {
		return fmt.Errorf("recording follow-up work: %w", err)
	}
	return nil
}

// CompleteFollowUps runs every follow-up a restore recorded and has not
// finished, this one's or an earlier one that stopped between an item's
// commit and its follow-up, deleting each row only once its work succeeded.
// Only work a restore recorded runs: a library or plugin that exists for any
// other reason is never set up again. A follow-up whose target is gone is
// dropped; one that fails stays for the next run. A restore job calls it
// before its first item and after each item that recorded one.
func (s *Service) CompleteFollowUps(ctx context.Context) {
	rows, err := s.opts.Pool.Query(ctx, `
		SELECT f.kind, f.target_id,
			CASE f.kind
				WHEN 'library_created' THEN EXISTS (SELECT 1 FROM media_folders m WHERE m.id = f.target_id)
				WHEN 'collection_created' THEN EXISTS (SELECT 1 FROM library_collections c WHERE c.id = f.target_id::text)
				ELSE EXISTS (SELECT 1 FROM plugin_installations p WHERE p.id = f.target_id)
			END
		FROM backup_restore_followups f ORDER BY f.created_at, f.kind, f.target_id`)
	if err != nil {
		slog.WarnContext(ctx, "reading restore follow-ups failed", "component", "backup", "error", err)
		return
	}
	type followUp struct {
		kind   string
		id     int
		exists bool
	}
	var pending []followUp
	for rows.Next() {
		var f followUp
		if err := rows.Scan(&f.kind, &f.id, &f.exists); err != nil {
			rows.Close()
			slog.WarnContext(ctx, "reading restore follow-ups failed", "component", "backup", "error", err)
			return
		}
		pending = append(pending, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.WarnContext(ctx, "reading restore follow-ups failed", "component", "backup", "error", err)
		return
	}
	for _, f := range pending {
		if f.exists {
			var err error
			switch f.kind {
			case followUpLibraryCreated:
				err = callFollowUp(ctx, s.targets.LibraryCreated, f.id)
			case followUpPluginInstalled:
				err = callFollowUp(ctx, s.targets.PluginInstalled, f.id)
			case followUpCollectionCreated:
				err = callFollowUp(ctx, func(ctx context.Context, id int) error {
					if s.targets.TemplateCollectionCreated == nil {
						return errors.New("this server cannot finish a restored template collection")
					}
					return s.targets.TemplateCollectionCreated(ctx, strconv.Itoa(id))
				}, f.id)
			}
			if err != nil {
				slog.WarnContext(ctx, "restore follow-up failed; the next restore retries it", "component", "backup", "kind", f.kind, "id", f.id, "error", err)
				continue
			}
		}
		if _, err := s.opts.Pool.Exec(ctx, `DELETE FROM backup_restore_followups WHERE kind = $1 AND target_id = $2`, f.kind, f.id); err != nil {
			slog.WarnContext(ctx, "clearing a restore follow-up failed", "component", "backup", "kind", f.kind, "id", f.id, "error", err)
		}
	}
}

func callFollowUp(ctx context.Context, fn func(context.Context, int) error, id int) error {
	if fn == nil {
		return errors.New("this server cannot run restore follow-ups")
	}
	return fn(ctx, id)
}

// ---- Libraries ----

func (a *applier) applyLibrary(ctx context.Context, tx pgx.Tx, id string) (applied, error) {
	backupID, _ := strconv.Atoi(id)
	plans, err := a.s.newPlanner(a.rs, tx).planLibraries(ctx)
	if err != nil {
		return applied{}, err
	}
	var plan *libraryPlan
	for i := range plans {
		if plans[i].entry.ID == backupID {
			plan = &plans[i]
		}
	}
	item := ReportItem{Kind: KindLibrary, Label: id}
	if plan == nil {
		return leftOut(item, ReasonNotInBackup, "The backup has no such library."), nil
	}
	e := plan.entry
	item.Label = e.Name
	if plan.localID > 0 {
		out := unchanged(item)
		out.mapLibrary = [2]int{e.ID, plan.localID}
		return out, nil
	}
	if !plan.item.Selectable {
		return leftOutFor(item, plan.item), nil
	}
	var newID int
	if err := tx.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('public.media_folders', 'id'))`).Scan(&newID); err != nil {
		return applied{}, fmt.Errorf("reserving a library id: %w", err)
	}
	poster, err := a.restoreLibraryPoster(ctx, e.Poster, newID, &item)
	if err != nil {
		return applied{}, err
	}
	realtime, ungrouped := e.RealtimeMonitoring, e.CollectionUngroupedSortOrder
	folder, err := catalog.CreateFolderInTx(ctx, tx, catalog.CreateFolderInput{
		ID: newID, Paths: e.Paths, Type: e.Type, Name: e.Name, MetadataLanguage: e.MetadataLanguage,
		ChapterThumbnailsEnabled: e.ChapterThumbnailsEnabled, IntroDetectionEnabled: e.IntroDetectionEnabled,
		TrickplayEnabled: e.TrickplayEnabled, TrailerKinds: e.TrailerKinds, RealtimeMonitoring: &realtime,
		Disabled: !e.Enabled, AutoTranslateMetadata: e.AutoTranslateMetadata, PosterPath: poster,
		CollectionUngroupedSortOrder: &ungrouped,
		BoxSetsEnabled:               e.BoxSetsEnabled, BoxSetMinMovies: e.BoxSetMinMovies, BoxSetsInSearch: e.BoxSetsInSearch,
	})
	if err != nil {
		return applied{}, err
	}
	item.Outcome = OutcomeRestored
	item.Message = "Created; its first scan is queued."
	// The editor's follow-ups run after commit; the row records that they
	// are owed, so a crash before they finish leaves them for the next run.
	if err := recordFollowUp(ctx, tx, followUpLibraryCreated, folder.ID); err != nil {
		return applied{}, err
	}
	return applied{item: item, mapLibrary: [2]int{e.ID, folder.ID}, after: a.s.CompleteFollowUps}, nil
}

// restoreLibraryPoster writes the poster under the new library's key before
// the row that references it; a rollback leaves an orphan for the orphan
// sweeps. It returns the key to store, or "" when there is none.
func (a *applier) restoreLibraryPoster(ctx context.Context, key string, libraryID int, item *ReportItem) (string, error) {
	if key == "" {
		return "", nil
	}
	if !a.rs.HasBlob(key) {
		item.MissingBlobs = []string{key}
		item.Notes = append(item.Notes, "Its poster was missing from the backup.")
		return "", nil
	}
	poster := fmt.Sprintf("library-posters/%d%s", libraryID, path.Ext(key))
	if err := a.writeBlob(ctx, key, poster); err != nil {
		return "", err
	}
	return poster, nil
}

// ---- Plugins ----

func (a *applier) applyPlugin(ctx context.Context, tx pgx.Tx, id string) (applied, error) {
	item := ReportItem{Kind: KindPlugin, Label: id}
	var doc *PluginDocument
	for i := range a.rs.Plugins {
		if a.rs.Plugins[i].PluginID == id {
			doc = &a.rs.Plugins[i]
		}
	}
	if doc == nil {
		return leftOut(item, ReasonNotInBackup, "The backup has no such plugin."), nil
	}
	item.Label = doc.PluginID + " " + doc.Version
	if a.s.targets.Plugins == nil {
		return applied{}, plugins.ErrRestoreUnavailable
	}
	archive, err := a.rs.pluginArchive(doc.PluginID)
	if err != nil {
		return applied{}, fmt.Errorf("reading the plugin archive: %w", err)
	}
	in := plugins.RestoreInstallationInput{PluginID: doc.PluginID, Version: doc.Version, Enabled: doc.Enabled, Archive: archive,
		SourceInstallationID: doc.InstallationID, ConfigUnreadable: !a.s.keyMatches(a.rs)}
	for _, c := range doc.Configs {
		in.Configs = append(in.Configs, plugins.RestoreConfig{Key: c.Key, Stored: c.Value})
	}
	res, after, err := a.s.targets.Plugins.RestorePlugin(ctx, tx, in)
	if err != nil {
		return applied{}, err
	}
	// A new installation's lifecycle event and provider-chain sync are
	// owed durably, like a new library's follow-ups. Reconfiguring an
	// installed plugin advanced its runtime generation in this transaction,
	// which replaces stale processes even if the event after commit is lost.
	if res.Installed {
		if err := recordFollowUp(ctx, tx, followUpPluginInstalled, res.InstallationID); err != nil {
			return applied{}, err
		}
	}
	for _, r := range res.Rejected {
		item.Notes = append(item.Notes, fmt.Sprintf("Configuration %q was not restored: %s.", r.Key, r.Reason))
	}
	switch {
	case res.Installed && !res.Enabled && doc.Enabled:
		item.Outcome = OutcomeRestored
		item.Message = "Installed and left disabled, because its configuration could not be restored."
		after = a.s.CompleteFollowUps
	case res.Installed:
		item.Outcome, item.Message = OutcomeRestored, "Installed."
		after = a.s.CompleteFollowUps
	case len(res.Written) > 0 && res.SettingsOnly:
		item.Outcome = OutcomeRestored
		item.Message = fmt.Sprintf("Settings only: version %s is installed here.", res.InstalledHere)
	case len(res.Written) > 0:
		item.Outcome, item.Message = OutcomeRestored, "Configuration restored."
	case len(res.Rejected) > 0:
		return leftOut(item, ReasonNeedsAnotherKey, "None of its configuration could be restored."), nil
	default:
		return unchanged(item), nil
	}
	return applied{item: item, after: after}, nil
}

// ---- Settings ----

func (a *applier) applySettingsPage(ctx context.Context, tx pgx.Tx, id string) (applied, error) {
	page, known := settingsPageByID(id)
	item := ReportItem{Kind: KindSettingsPage, Label: id}
	if !known {
		return leftOut(item, ReasonNotInBackup, "There is no such settings page."), nil
	}
	item.Label = page.Title
	plans, err := a.s.newPlanner(a.rs, tx).planSettings(ctx)
	if err != nil {
		return applied{}, err
	}
	values, needKey := map[string]string{}, 0
	for _, p := range plans {
		if p.page != id {
			continue
		}
		switch p.preview.Status {
		case ItemDiffers:
			values[p.preview.Key] = p.value
		case ItemNeedsAnotherKey:
			needKey++
		}
	}
	if needKey > 0 {
		item.Notes = append(item.Notes, fmt.Sprintf("%d settings encrypted with another SECRET_KEY were left out.", needKey))
	}
	if len(values) == 0 {
		if needKey > 0 {
			return leftOut(item, ReasonNeedsAnotherKey, "Every setting that differs was encrypted with another SECRET_KEY."), nil
		}
		return unchanged(item), nil
	}
	if a.s.targets.ApplySettings == nil {
		return applied{}, errors.New("settings restore is not available on this server")
	}
	restartKeys, after, err := a.s.targets.ApplySettings(ctx, tx, values)
	if err != nil {
		return applied{}, err
	}
	keys := slices.Sorted(maps.Keys(values))
	item.Outcome = OutcomeRestored
	item.Message = "Restored " + strings.Join(keys, ", ") + "."
	item.RestartKeys = restartKeys
	item.RestartRequired = len(restartKeys) > 0
	return applied{item: item, after: after}, nil
}

// ---- Collections and artwork ----

func (a *applier) applyCollection(ctx context.Context, tx pgx.Tx, id string) (applied, error) {
	item := ReportItem{Kind: KindCollection, Label: id}
	var c *CollectionEntry
	for i := range a.rs.Collections.Collections {
		if collectionItemID(a.rs.Collections.Collections[i].ID) == id {
			c = &a.rs.Collections.Collections[i]
		}
	}
	if c == nil {
		return leftOut(item, ReasonNotInBackup, "The backup has no such collection."), nil
	}
	item.Label = c.Title
	libMap := a.libraryMap()
	plan, err := a.planner(tx).planCollection(ctx, *c, libMap, nil)
	if err != nil {
		return applied{}, err
	}
	if plan.item.Reason == ReasonLibraryNotRestorable {
		return leftOut(item, ReasonLibraryNotMapped, "Its library is not on this server."), nil
	}
	if !plan.item.Selectable {
		return leftOutFor(item, plan.item), nil
	}
	if unknown := plan.item.Collection.UnknownItems; unknown > 0 {
		item.Notes = append(item.Notes, fmt.Sprintf("%d of its items are not in this server's catalog and were left out.", unknown))
	}
	if plan.localID != "" {
		added, err := catalog.AddMissingItemsInTx(ctx, tx, plan.localID, plan.missing)
		if err != nil {
			return applied{}, err
		}
		grouped, err := restoreGroups(ctx, tx, plan.localID, *c, libMap)
		if err != nil {
			return applied{}, err
		}
		a.claim(claimCollection, plan.localID, id)
		if len(added) == 0 && grouped == 0 {
			return unchanged(item), nil
		}
		item.Outcome = OutcomeRestored
		item.Message = fmt.Sprintf("Added %d items to the collection here and put it back in %d groups.", len(added), grouped)
		return applied{item: item}, nil
	}

	// A smart query or a sync source names libraries too; one that would
	// lose its whole scope is left out rather than widened to every library.
	query, queryOK := userdata.RemapQueryLibraries(c.QueryDefinition, mapIn(libMap))
	source, sourceOK := userdata.RemapQueryLibraries(c.SourceConfig, mapIn(libMap))
	if !queryOK || !sourceOK {
		return leftOut(item, ReasonLibraryNotMapped, "Its rules or source name only libraries that are not on this server."), nil
	}
	newID, err := freeCollectionID(ctx, tx, c.ID)
	if err != nil {
		return applied{}, err
	}
	// Uploaded artwork is copied under the new collection's keys before the
	// row that references it.
	poster, err := a.copyCollectionArt(ctx, c.ID, newID, c.Poster, &item)
	if err != nil {
		return applied{}, err
	}
	backdrop, err := a.copyCollectionArt(ctx, c.ID, newID, c.Backdrop, &item)
	if err != nil {
		return applied{}, err
	}
	primary := plan.scope[0]
	slug, err := freeSlug(ctx, tx, primary, c.Slug)
	if err != nil {
		return applied{}, err
	}
	if _, err := catalog.CreateLibraryCollectionInTx(ctx, tx, catalog.CreateLibraryCollectionInput{
		ID: newID, LibraryID: primary, LibraryIDs: plan.scope, Slug: slug, Title: c.Title, Description: c.Description,
		CollectionType: c.CollectionType, Visibility: c.Visibility, SortOrder: c.SortOrder, Featured: c.Featured,
		PosterURL: poster, BackdropURL: backdrop, PosterThumbhash: thumbFor(poster, c.PosterThumbhash), BackdropThumbhash: thumbFor(backdrop, c.BackdropThumbhash),
		PosterAutoGenerated: c.PosterAutoGenerated, SourceURL: c.SourceURL,
		QueryDefinition: query, SortConfig: c.SortConfig, SourceConfig: source,
		SyncSchedule: c.SyncSchedule,
	}); err != nil {
		return applied{}, err
	}
	if _, err := catalog.AddMissingItemsInTx(ctx, tx, newID, plan.missing); err != nil {
		return applied{}, err
	}
	if _, err := restoreGroups(ctx, tx, newID, *c, libMap); err != nil {
		return applied{}, err
	}
	a.claim(claimCollection, newID, id)
	item.Outcome, item.Message = OutcomeRestored, msgCreated
	return applied{item: item}, nil
}

// mapIn turns a library map into the lookup userdata takes.
func mapIn(m map[int]int) func(int) (int, bool) {
	return func(id int) (int, bool) {
		local, ok := m[id]
		return local, ok
	}
}

func thumbFor(key, thumb string) string {
	if key == "" {
		return ""
	}
	return thumb
}

// copyCollectionArt copies a collection's uploaded image, with its stored
// variants, from collection-images/<old id>/ to the same path under the new
// id. Any other reference (provider artwork) is kept as it is.
func (a *applier) copyCollectionArt(ctx context.Context, oldID, newID, key string, item *ReportItem) (string, error) {
	oldPrefix := "collection-images/" + oldID + "/"
	if !strings.HasPrefix(key, "collection-images/") {
		return key, nil
	}
	if !strings.HasPrefix(key, oldPrefix) || !a.rs.HasBlob(key) {
		item.MissingBlobs = append(item.MissingBlobs, key)
		item.Notes = append(item.Notes, "An image of the collection was missing from the backup.")
		return "", nil
	}
	newPrefix := "collection-images/" + newID + "/"
	for _, from := range a.rs.blobFamily(key) {
		if !strings.HasPrefix(from, oldPrefix) {
			continue
		}
		if err := a.writeBlob(ctx, from, newPrefix+strings.TrimPrefix(from, oldPrefix)); err != nil {
			return "", err
		}
	}
	return newPrefix + strings.TrimPrefix(key, oldPrefix), nil
}

// restoreGroups puts each of the collection's memberships here into the
// group its backup membership had, through the mapped library: the group of
// the same slug in that library, created when missing. Only ungrouped
// memberships change, so a group an admin chose since stays. It returns how
// many memberships it grouped.
func restoreGroups(ctx context.Context, tx pgx.Tx, collectionID string, c CollectionEntry, libMap map[int]int) (int, error) {
	grouped := 0
	for _, lib := range c.Libraries {
		local, ok := libMap[lib.LibraryID]
		if !ok || lib.GroupSlug == "" {
			continue
		}
		groupID, err := groupFor(ctx, tx, lib, local)
		if err != nil {
			return grouped, err
		}
		changed, err := catalog.GroupUngroupedMembershipInTx(ctx, tx, collectionID, local, groupID)
		if err != nil {
			return grouped, err
		}
		if changed {
			grouped++
		}
	}
	return grouped, nil
}

// groupFor finds the regular group of the backup membership's slug in a
// library here, creating it when missing.
func groupFor(ctx context.Context, tx pgx.Tx, lib CollectionLib, libraryID int) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM library_collection_groups WHERE library_id = $1 AND slug = $2 AND kind = 'regular'`, libraryID, lib.GroupSlug).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	name := lib.GroupName
	if name == "" {
		name = lib.GroupSlug
	}
	group, err := catalog.CreateLibraryCollectionGroupInTx(ctx, tx, catalog.CreateLibraryCollectionGroupInput{
		LibraryID: libraryID, Name: name, Slug: lib.GroupSlug, DefaultSortMode: models.GroupSortMode(groupSortMode(lib.GroupSort)),
	})
	if err != nil {
		return "", fmt.Errorf("creating the collection group: %w", err)
	}
	return group.ID, nil
}

// freeSlug keeps the backup's slug unless another collection of the library
// has it.
func freeSlug(ctx context.Context, tx pgx.Tx, libraryID int, slug string) (string, error) {
	candidate := slug
	for n := 2; n < 1000; n++ {
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM library_collections WHERE library_id = $1 AND slug = $2)`, libraryID, candidate).Scan(&taken); err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s-%d", slug, n)
	}
	return "", fmt.Errorf("no free slug for %q", slug)
}

func (a *applier) artworkEntry(id string) *ItemArtworkEntry {
	for i := range a.rs.Collections.ItemArtwork {
		if a.rs.Collections.ItemArtwork[i].itemID() == id {
			return &a.rs.Collections.ItemArtwork[i]
		}
	}
	return nil
}

// stageArtwork writes an artwork item's objects before its transaction,
// in the image cache's order: the revision is registered with the
// collector, its objects written, and then their exact manifest recorded,
// so the collector never reclaims objects a restore is about to publish.
// It runs outside the item's transaction because the tracker writes on a
// pool connection of its own, which must commit before the collector can
// see it. Objects of an item the transaction then leaves out stay
// registered, and the collector reclaims them.
func (a *applier) stageArtwork(ctx context.Context, id string) error {
	entry := a.artworkEntry(id)
	if entry == nil {
		return nil
	}
	plan, err := a.s.newPlanner(a.rs, a.s.opts.Pool).planArtwork(ctx, *entry)
	if err != nil || !plan.Selectable {
		return err
	}
	track := a.s.targets.TrackArtwork
	if track != nil {
		if err := track(ctx, entry.Path, entry.ImageType, nil); err != nil {
			return err
		}
	}
	// Content ids, and so the image's keys, are the same on every server.
	keys := a.rs.blobFamily(entry.Path)
	for _, key := range keys {
		if err := a.writeBlob(ctx, key, key); err != nil {
			return err
		}
	}
	if track != nil {
		return track(ctx, entry.Path, entry.ImageType, keys)
	}
	return nil
}

func (a *applier) applyArtwork(ctx context.Context, tx pgx.Tx, id string) (applied, error) {
	item := ReportItem{Kind: KindItemArtwork, Label: id}
	entry := a.artworkEntry(id)
	if entry == nil {
		return leftOut(item, ReasonNotInBackup, "The backup has no such image."), nil
	}
	plan, err := a.s.newPlanner(a.rs, tx).planArtwork(ctx, *entry)
	if err != nil {
		return applied{}, err
	}
	item.Label = plan.Label
	if !plan.Selectable {
		return leftOutFor(item, plan), nil
	}
	stageErr, staged := a.staged[id]
	if !staged {
		return leftOut(item, ReasonChanged, "The image changed while the restore ran. Open the backup again."), nil
	}
	if stageErr != nil {
		return applied{}, stageErr
	}
	if err := catalog.PublishArtworkSelectionInTx(ctx, tx, catalog.ArtworkSelection{
		TargetType: entry.TargetType, TargetContentID: entry.ContentID, ParentContentID: entry.ParentContentID,
		ImageType: entry.ImageType, StoredPath: entry.Path, SourcePath: entry.Source, Thumbhash: entry.Thumbhash,
		LockField: int(metadata.FieldImages),
	}); err != nil {
		return applied{}, err
	}
	item.Outcome = OutcomeRestored
	announce, content, parent := a.s.targets.ArtworkChanged, entry.ContentID, entry.ParentContentID
	return applied{item: item, after: func(ctx context.Context) {
		if announce != nil {
			announce(ctx, content, parent)
		}
	}}, nil
}

func (a *applier) applyBranding(ctx context.Context, tx pgx.Tx, id string) (applied, error) {
	item := ReportItem{Kind: KindBranding, Label: id}
	var entry *BrandingEntry
	for i := range a.rs.Collections.Branding {
		if a.rs.Collections.Branding[i].itemID() == id {
			entry = &a.rs.Collections.Branding[i]
		}
	}
	if entry == nil {
		return leftOut(item, ReasonNotInBackup, "The backup has no such branding asset."), nil
	}
	item.Label = entry.Key
	// The parser admitted it, but the setting this writes is decided here,
	// so the check runs again where it matters.
	if err := validBrandingEntry(*entry); err != nil {
		return applied{}, err
	}
	stored, err := readStoredSettings(ctx, tx)
	if err != nil {
		return applied{}, err
	}
	plan := a.s.newPlanner(a.rs, tx).planBranding(stored, *entry)
	if !plan.Selectable {
		return leftOutFor(item, plan), nil
	}
	if a.s.targets.ApplySettings == nil {
		return applied{}, errors.New("settings restore is not available on this server")
	}
	if err := a.writeBlob(ctx, entry.Blob, entry.Blob); err != nil {
		return applied{}, err
	}
	_, after, err := a.s.targets.ApplySettings(ctx, tx, map[string]string{entry.Key: entry.Ref})
	if err != nil {
		return applied{}, err
	}
	item.Outcome = OutcomeRestored
	return applied{item: item, after: after}, nil
}

// groupSortMode keeps a known group sort mode and falls back to manual.
func groupSortMode(mode string) string {
	switch mode {
	case "manual", "name_asc", "name_desc", "recent", "most_items":
		return mode
	}
	return "manual"
}
