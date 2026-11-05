package backup

import (
	"bytes"
	"cmp"
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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// ExecuteRestore runs one backup_restore job:
//
//  1. take the subsystem lock and hold it for the whole run;
//  2. read the source backup's restore set and refuse it when its digest
//     is not the one the preview returned;
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
	report.BackupID, report.Digest = req.BackupID, req.Digest
	if s.opts.Locker == nil {
		return fail(errors.New("backups are not available on this server"))
	}
	held, err := lockWithin(ctx, s.opts.Locker, enqueueHandoff)
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

	rs, located, err := s.readSelected(ctx, work, req, func(planned *RestoreSet) map[string]bool {
		wanted := selectedBlobs(planned, req.Selection)
		maps.Copy(wanted, selectedUserBlobs(planned, req.Users))
		return wanted
	})
	if err != nil {
		return fail(err)
	}
	rs.unverified = !located.authenticated
	defer rs.Cleanup()

	if err := s.recordStoreIdentities(work); err != nil {
		return fail(err)
	}
	a := newApplier(s, rs, run, report, req)
	// The report the job keeps lists every item's outcome once the run
	// ends, however it ends: until then each item's is a row of its own.
	defer a.collectItems(context.WithoutCancel(ctx))
	if len(req.Users) > 0 || len(req.Selection[SectionHomeSections]) > 0 {
		if a.serverID, err = s.serverID(work); err != nil {
			return fail(err)
		}
	}
	pinned := map[string]bool{req.BackupID: true}
	result, err := s.backupWith(work, held, KindBeforeRestore, "", nil, pinned)
	if err != nil {
		return fail(fmt.Errorf("%w: %w", errBeforeRestoreBackup, err))
	}
	// Follow-ups an earlier restore committed but never finished run first;
	// one that waits for a restore's end waits for this one's.
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
	op := s.beginOperation(work, held, ActiveOperation{Operation: OperationRestore, BackupID: req.BackupID, StartedAt: s.opts.Now()})
	defer op.end(work, nil)

	for _, spec := range restoreSections {
		for _, id := range req.selectedIDs(spec.id) {
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
	// Default sections of the libraries this restore created, where it
	// wrote none of their saved ones: the one call of the final runner. A
	// restore that stops leaves them owed to the end of the next one, which
	// may write the saved sections still missing.
	s.finishFollowUps(work)
	finished := s.opts.Now().UTC()
	report.FinishedAt = &finished
	return report, nil
}

// newApplier is the applier of req, counting its items and the backup
// taken before them.
func newApplier(s *Service, rs *RestoreSet, run RestoreRun, report *SelectiveReport, req RestoreRequest) *applier {
	a := &applier{s: s, rs: rs, run: run, report: report, total: 1, users: map[string]UserSelection{}, staged: map[string]error{}, filesWritten: map[string]bool{},
		runID: cmp.Or(run.JobID, uuid.NewString())}
	for _, spec := range restoreSections {
		a.total += len(req.selectedIDs(spec.id))
	}
	for _, u := range req.Users {
		a.users[u.Account] = u
	}
	return a
}

// recordStoreIdentities records each store's identity before any item
// runs: items write files inside their transactions, and a store recording
// its identity on that first write would need a second pool connection.
func (s *Service) recordStoreIdentities(ctx context.Context) error {
	for _, store := range s.stores {
		if err := blobstore.RecordIdentity(ctx, store); err != nil {
			return fmt.Errorf("recording the storage: %w", err)
		}
	}
	return nil
}

// selectedIDs lists the items of section the request selects: for
// accounts, the ones its Users name.
func (req RestoreRequest) selectedIDs(section string) []string {
	if section != SectionUsers {
		return req.Selection[section]
	}
	ids := make([]string, 0, len(req.Users))
	for _, u := range req.Users {
		ids = append(ids, u.Account)
	}
	return ids
}

// readSelected reads the restore set req names, extracting only the files
// its selected items write (wanted, from the preview's read of the same
// set), once the temporary directory has room for them (checkTempSpace).
// Nothing is written before the first item, so reading stops when the job
// (ctx) is canceled, as well as when work ends.
func (s *Service) readSelected(ctx, work context.Context, req RestoreRequest, wanted func(*RestoreSet) map[string]bool) (*RestoreSet, locatedSource, error) {
	reading, stopReading := context.WithCancel(work)
	defer stopReading()
	stop := context.AfterFunc(ctx, stopReading)
	defer stop()
	located, err := s.locateConfirmed(reading, req.source(), req.AcceptUnauthenticated)
	if err != nil {
		return nil, located, err
	}
	planned, err := s.cachedSet(reading, located)
	if err != nil {
		return nil, located, err
	}
	if planned.Digest != req.Digest {
		return nil, located, ErrDigestMismatch
	}
	keep := wanted(planned)
	if err := checkTempSpace(os.TempDir(), planned, keep); err != nil {
		return nil, located, err
	}
	rs, err := s.openRestoreSet(reading, located, func(key string) bool { return keep[key] })
	if err != nil {
		return nil, located, err
	}
	if rs.Digest != req.Digest {
		rs.Cleanup()
		return nil, located, ErrDigestMismatch
	}
	return rs, located, nil
}

// applier applies items, one fenced transaction each.
type applier struct {
	s  *Service
	rs *RestoreSet
	// runID names this run's backup_restore_items rows.
	runID string
	// seasonal counts the seasonal sections this restore wrote
	// (maxRestoredSeasonal).
	seasonal int
	// serverID is this server's identity; users and home sections match by
	// id only when the backup is this server's.
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
	// staged holds, per artwork item, nil once stageArtwork wrote its
	// objects, or why it could not; an item it did not write is absent,
	// and applyArtwork never publishes one.
	staged map[string]error
	// collectionBytes and libraryCollectionBytes are the collection
	// metadata committed items wrote (overCollectionBudget).
	collectionBytes        int64
	libraryCollectionBytes map[int]int64
	// filesWritten marks the artwork items stageArtwork wrote missing files
	// back for: the catalog already showed them, so their transaction
	// finds them the same and has nothing more to do.
	filesWritten map[string]bool
}

// applied is one item's outcome before its transaction commits.
type applied struct {
	item  ReportItem
	after func(context.Context)
	// mapLibrary records a backup library id and the library it became.
	mapLibrary [2]int
	// metadata is the collection metadata the item wrote, charged to the
	// restore's budget once it commits (overCollectionBudget).
	metadata collectionCharge
}

// recentItems is how many of the latest outcomes the running job's result
// lists; every outcome is a backup_restore_items row until the run ends.
const recentItems = 20

// clone is the report an item's commit writes: counts, the library map,
// and the latest outcomes only, so the write is the same size for the
// thousandth item as for the first.
func (a *applier) clone() *SelectiveReport {
	next := *a.report
	next.Items = slices.Clone(a.report.Items[max(len(a.report.Items)-recentItems, 0):])
	next.Counts = maps.Clone(a.report.Counts)
	next.LibraryMap = maps.Clone(a.report.LibraryMap)
	return &next
}

// recordItem writes an item's outcome as a row in the item's transaction.
func (a *applier) recordItem(ctx context.Context, tx pgx.Tx, item ReportItem) error {
	data, _ := json.Marshal(item) // strings, ints, and lists of strings
	if _, err := tx.Exec(ctx, `INSERT INTO backup_restore_items (run_id, seq, item) VALUES ($1, $2, $3)
		ON CONFLICT (run_id, seq) DO UPDATE SET item = EXCLUDED.item`, a.runID, a.current, data); err != nil {
		return fmt.Errorf("recording the item's outcome: %w", err)
	}
	return nil
}

// collectItems puts every item's outcome into the report, in order, once
// the run ends. Without them (a failure reading them) the report keeps the
// latest outcomes. The rows stay until the statement that records the final
// report removes them (adminjob Complete, Fail, CancelWithResult, or the
// recovery of an interrupted run), so a crash before then loses none.
func (a *applier) collectItems(ctx context.Context) {
	pool := a.s.opts.Pool
	if pool == nil {
		return
	}
	rows, err := pool.Query(ctx, `SELECT item FROM backup_restore_items WHERE run_id = $1 ORDER BY seq`, a.runID)
	if err != nil {
		return
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ReportItem, error) {
		var item ReportItem
		var data []byte
		if err := row.Scan(&data); err != nil {
			return item, err
		}
		err := json.Unmarshal(data, &item)
		return item, err
	})
	if err != nil {
		return
	}
	if len(items) > 0 {
		a.report.Items = items
	}
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
		a.stageArtwork(ctx, id)
	}
	if section == SectionPlugins {
		a.stagePlugin(ctx, id)
	}
	var result applied
	next := a.clone()
	err := a.commit(ctx, next, "Restoring "+section, func(tx pgx.Tx) error {
		var err error
		result, err = a.applyWithRetry(ctx, tx, section, id)
		if err != nil {
			return err
		}
		if err := a.recordItem(ctx, tx, result.item); err != nil {
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
	a.charge(result.metadata)
	if result.after != nil {
		result.after(ctx)
	}
	return nil
}

// itemAttempts bounds how often an item starts again from its read after
// a retryable failure (retryableItemError).
const itemAttempts = 3

// applyWithRetry runs one item in a savepoint of tx. A deadlock, a
// serialization failure, or a section whose references changed between
// its read and its lock rolls the savepoint back and starts the item again
// from its read; after itemAttempts it is left out as changed during the
// restore. Any other failure records the item as failed.
func (a *applier) applyWithRetry(ctx context.Context, tx pgx.Tx, section, id string) (applied, error) {
	for attempt := 1; ; attempt++ {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return applied{}, err
		}
		result, err := a.applyItem(ctx, sp, section, id)
		result.item.Section, result.item.ID = section, id
		if err == nil {
			return result, sp.Commit(ctx)
		}
		_ = sp.Rollback(ctx)
		if retryableItemError(err) && attempt < itemAttempts && ctx.Err() == nil {
			continue
		}
		item := ReportItem{Section: section, ID: id, Kind: result.item.Kind, Label: result.item.Label}
		if retryableItemError(err) {
			item = leftOut(item, ReasonChanged, "It changed on this server while the restore ran. Restore it again.").item
		} else {
			item.Outcome, item.Error = OutcomeFailed, err.Error()
		}
		if item.Label == "" {
			item.Label = id
		}
		return applied{item: item}, nil
	}
}

// retryableItemError reports a failure an item survives by starting again
// from its read: a deadlock or serialization failure, or a section whose
// references changed under it.
func retryableItemError(err error) bool {
	if errors.Is(err, sections.ErrReferencesChanged) || errors.Is(err, errSettingsChanged) {
		return true
	}
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && (pgErr.Code == "40P01" || pgErr.Code == "40001")
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
	// A restored file meets the checks an upload of it would.
	if size > maxRestoredFile {
		return fmt.Errorf("%w: %s is %s, over the %s any upload may be", ErrRestoredFileRefused, to, formatBytes(uint64(size)), formatBytes(maxRestoredFile))
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("reading %s: %w", from, err)
	}
	if err := checkRestoredBlob(to, data); err != nil {
		return err
	}
	err = blobstore.CreateExclusive(ctx, store, to, bytes.NewReader(data), blobstore.MediaType(to))
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
	input := libraryInput(e)
	input.ID, input.PosterPath = newID, poster
	input.RealtimeMonitoring, input.CollectionUngroupedSortOrder = &realtime, &ungrouped
	costly := costlyLibraryOptions(e)
	if a.rs.unverified {
		input = withoutCostlyOptions(input)
	}
	folder, err := catalog.CreateFolderInTx(ctx, tx, input)
	if err != nil {
		return applied{}, err
	}
	item.Outcome = OutcomeRestored
	item.Message = "Created; its first scan is queued."
	if a.rs.unverified && len(costly) > 0 {
		item.Notes = append(item.Notes, "Created with "+joinLabels(costly)+" off: they are not restored from a backup this server cannot verify.")
	}
	// The editor's follow-ups run after commit; the rows record that they
	// are owed, so a crash before they finish leaves them for the next run.
	// Its default sections wait for the end of the restore, which may
	// write the library's saved ones (followUpLibrarySections).
	for _, kind := range []string{followUpLibraryCreated, followUpLibrarySections} {
		if err := recordFollowUp(ctx, tx, kind, folder.ID); err != nil {
			return applied{}, err
		}
	}
	return applied{item: item, mapLibrary: [2]int{e.ID, folder.ID}, after: a.s.CompleteFollowUps}, nil
}

// libraryInput is the library e creates, before its id and poster.
func libraryInput(e LibraryEntry) catalog.CreateFolderInput {
	return catalog.CreateFolderInput{
		Paths: e.Paths, Type: e.Type, Name: e.Name, MetadataLanguage: e.MetadataLanguage,
		ChapterThumbnailsEnabled: e.ChapterThumbnailsEnabled, IntroDetectionEnabled: e.IntroDetectionEnabled,
		TrickplayEnabled: e.TrickplayEnabled, TrailerKinds: e.TrailerKinds, Disabled: !e.Enabled,
		AutoTranslateMetadata: e.AutoTranslateMetadata,
		BoxSetsEnabled:        e.BoxSetsEnabled, BoxSetMinMovies: e.BoxSetMinMovies, BoxSetsInSearch: e.BoxSetsInSearch,
	}
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

// stagePlugin reads the plugin's installed manifest before the item's
// transaction opens. Best effort: RestorePlugin reads it again, inside the
// transaction, when this did not leave it in the cache.
func (a *applier) stagePlugin(ctx context.Context, id string) {
	doc := indexed(a.rs.Plugins, a.rs.lookup().plugins, id)
	if doc == nil || a.rs.unverified || a.s.targets.Plugins == nil {
		return
	}
	if err := a.s.targets.Plugins.PrepareRestorePlugin(ctx, doc.PluginID); err != nil {
		slog.WarnContext(ctx, "reading a plugin's manifest before restoring it", "component", "backup", "plugin_id", doc.PluginID, "error", err)
	}
}

func (a *applier) applyPlugin(ctx context.Context, tx pgx.Tx, id string) (applied, error) {
	item := ReportItem{Kind: KindPlugin, Label: id}
	doc := indexed(a.rs.Plugins, a.rs.lookup().plugins, id)
	if doc == nil {
		return leftOut(item, ReasonNotInBackup, "The backup has no such plugin."), nil
	}
	item.Label = doc.PluginID + " " + doc.Version
	if a.rs.unverified {
		return leftOut(item, ReasonUnverifiedBackup, "Its configuration can connect to outside services, so it is not restored from a backup this server cannot verify."), nil
	}
	if a.s.targets.Plugins == nil {
		return applied{}, plugins.ErrRestoreUnavailable
	}
	in := plugins.RestoreInstallationInput{PluginID: doc.PluginID, Version: doc.Version,
		SourceInstallationID: doc.InstallationID, ConfigUnreadable: !a.s.keyMatches(a.rs)}
	for _, c := range doc.Configs {
		in.Configs = append(in.Configs, plugins.RestoreConfig{Key: c.Key, Stored: c.Value})
	}
	res, after, err := a.s.targets.Plugins.RestorePlugin(ctx, tx, in)
	if errors.Is(err, plugins.ErrPluginNotInstalled) {
		return leftOut(item, ReasonNeedsInstall, needsInstallMessage(*doc)), nil
	}
	if err != nil {
		return applied{}, err
	}
	for _, r := range res.Rejected {
		item.Notes = append(item.Notes, fmt.Sprintf("Configuration %q was not restored: %s.", r.Key, r.Reason))
	}
	switch {
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

// needsInstallMessage says how to restore a plugin this server does not
// have: a restore never installs one from a backup.
func needsInstallMessage(doc PluginDocument) string {
	msg := fmt.Sprintf("Install %s %s from a plugin catalog, then restore it again", doc.PluginID, doc.Version)
	if len(doc.Configs) > 0 {
		return msg + " to apply its settings. A restore never installs a plugin from a backup."
	}
	return msg + ". A restore never installs a plugin from a backup."
}
