package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// ControlDB runs the swap's database-level statements on a connection to the
// maintenance database, never to the live database being replaced.
type ControlDB interface {
	// OID returns a database's oid, which stays with it across renames.
	OID(ctx context.Context, name string) (oid uint32, found bool, err error)
	// NameOf returns the current name of the database with oid.
	NameOf(ctx context.Context, oid uint32) (name string, found bool, err error)
	// OtherConnections counts sessions on name other than this command's.
	OtherConnections(ctx context.Context, name string) (int, error)
	// CheckPrivileges confirms the role can create and rename databases and
	// change ALLOW_CONNECTIONS on live.
	CheckPrivileges(ctx context.Context, live string) error
	Create(ctx context.Context, name string) error
	Drop(ctx context.Context, name string) error
	Rename(ctx context.Context, from, to string) error
	AllowConnections(ctx context.Context, name string, allow bool) error
	// Terminate ends every session on name other than this command's.
	Terminate(ctx context.Context, name string) error
}

// ErrSwapUnresolved reports databases whose names match no state the swap
// can be in. Nothing more is changed; the databases stay closed.
var ErrSwapUnresolved = errors.New("the databases are not in any state this restore left them in")

// SwapOptions replace the live database and restore blobs into the live
// stores. Silo must be stopped on every server first.
type SwapOptions struct {
	Validate ValidateOptions
	// LiveURL is DATABASE_URL.
	LiveURL      string
	DatabaseOnly bool
	// StoreConfig is the live blob storage, read from the live configuration
	// before anything changes, and OpenStores opens it. The journal records
	// it, credentials sealed with Cipher, so --recover can reopen the same
	// stores without opening either database. Both are unused with
	// DatabaseOnly.
	StoreConfig StoreConfig
	OpenStores  func(context.Context, StoreConfig) (blobstore.Stores, error)
	Cipher      *secret.Cipher
	// RestoredSettings reads the restored database's settings, so the swap
	// can confirm it records the storage location the live stores are.
	RestoredSettings func(ctx context.Context, dbURL string) (map[string]string, error)
	// WorkDir holds the local journal.
	WorkDir string
	// Journaled runs once the journal exists, just before the live database
	// closes, with the time --recover takes from here on.
	Journaled func(stamp string)
	Now       func() time.Time
	Out       io.Writer
}

// swapCleanupTimeout bounds a swap's cleanup: settling, rolling back, or
// dropping an unused restored database. Cleanup runs to the end when the
// command is interrupted, but a statement that never returns must not keep
// the databases closed forever; when the bound passes, the command stops
// and prints the journal and the --recover command. A variable for tests.
var swapCleanupTimeout = 5 * time.Minute

type cleanupKey struct{}

// cleanupContext is the context cleanup runs under: detached from ctx's
// cancellation, with one swapCleanupTimeout deadline for the whole cleanup.
// A cleanup step called from another keeps the deadline it was given.
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Value(cleanupKey{}) != nil {
		return ctx, func() {}
	}
	bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), swapCleanupTimeout)
	return context.WithValue(bounded, cleanupKey{}, true), cancel
}

// timedOut explains a cleanup error that is the cleanup deadline passing.
func timedOut(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf(" (cleanup stopped after %s)", swapCleanupTimeout)
	}
	return ""
}

// RestoreSwap runs §10.2 swap mode: it never destroys the current database
// before the restored one is verified, keeps the old database and every
// overwritten blob, and rolls back a failure in the blob or rename steps with
// the databases closed until the original is whole again.
func RestoreSwap(ctx context.Context, src *Source, loader DatabaseLoader, ctl ControlDB, opts SwapOptions) error {
	out := writerOrDiscard(opts.Out)
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	m := src.Manifest
	// A database-only backup leaves the live files as they are.
	opts.DatabaseOnly = opts.DatabaseOnly || !m.HasBlobs()
	// Step 1.
	if err := src.Validate(ctx, opts.Validate); err != nil {
		return err
	}
	live, err := databaseName(opts.LiveURL)
	if err != nil {
		return err
	}
	stamp := now().UTC().Format("20060102t150405z")
	restored, before := "silo_restore_"+stamp, "silo_before_restore_"+stamp
	opened, sealedStores, err := openSwapStores(ctx, src, opts, stamp)
	if err != nil {
		return err
	}
	stores := storesOf(opened)
	// Step 2: preflight. Nothing changes here.
	liveOID, err := swapPreflight(ctx, ctl, stores, live, restored, before)
	if err != nil {
		return err
	}
	// Step 3.
	restoredURL, err := WithDatabase(opts.LiveURL, restored)
	if err != nil {
		return err
	}
	if err := ctl.Create(ctx, restored); err != nil {
		return fmt.Errorf("creating %s: %w", restored, err)
	}
	discard := func(cause error) error {
		cleanup, cancel := cleanupContext(ctx)
		defer cancel()
		if dropErr := ctl.Drop(cleanup, restored); dropErr != nil {
			return fmt.Errorf("%w; dropping %s also failed%s: %w", cause, restored, timedOut(dropErr), dropErr)
		}
		_, _ = fmt.Fprintf(out, "Nothing changed: the live database and files are untouched.\n")
		return cause
	}
	restoredOID, found, err := ctl.OID(ctx, restored)
	if err != nil || !found {
		return discard(fmt.Errorf("reading %s's oid: %w", restored, errors.Join(err, errIf(!found, "not found"))))
	}
	_, _ = fmt.Fprintf(out, "Restoring into %s…\n", restored)
	if err := loader.Load(ctx, restoredURL, src.path(DumpName)); err != nil {
		return discard(err)
	}
	// Step 4.
	if err := verifyRestored(ctx, loader, restoredURL, m, opts.Validate); err != nil {
		return discard(err)
	}
	if opts.RestoredSettings != nil && !opts.DatabaseOnly {
		recorded, err := opts.RestoredSettings(ctx, restoredURL)
		if err != nil {
			return discard(fmt.Errorf("reading the restored settings: %w", err))
		}
		if err := blobstore.CheckRecordedLocation(recorded, identityOf(opened.Assets), privateIdentity(opened)); err != nil {
			return discard(fmt.Errorf("the backup records another storage location than this deployment uses: %w", err))
		}
	}
	if err := ctl.AllowConnections(ctx, restored, false); err != nil {
		return discard(err)
	}
	if err := ctl.Terminate(ctx, restored); err != nil {
		return discard(err)
	}
	_, _ = fmt.Fprintln(out, "The restored database matches the manifest; closed it to connections.")

	j, err := createJournal(JournalPath(opts.WorkDir, stamp), journalHeader{
		Time: stamp, ServerID: m.ServerID, BackupID: m.ID,
		LiveDatabase: live, RestoredDatabase: restored, BeforeDatabase: before,
		LiveOID: liveOID, RestoredOID: restoredOID,
		PreservePrefix: serverPrefix(m.ServerID) + stamp + "-overwritten-blobs/",
		Stores:         sealedStores,
	})
	if err != nil {
		return discard(err)
	}
	defer j.close()
	j.mirror = journalMirror(stores, j.header.PreservePrefix)
	if opts.Journaled != nil {
		opts.Journaled(stamp)
	}
	sw := &swap{ctl: ctl, j: j, stores: stores, out: out, manifest: m}
	return sw.run(ctx, src, opts.DatabaseOnly)
}

// openSwapStores opens the live stores, checks the archive against where its
// entries would land, and seals the storage configuration for the journal.
func openSwapStores(ctx context.Context, src *Source, opts SwapOptions, stamp string) (blobstore.Stores, *StoreConfig, error) {
	if opts.DatabaseOnly {
		return blobstore.Stores{}, nil, nil
	}
	if opts.OpenStores == nil {
		return blobstore.Stores{}, nil, errors.New("no blob storage to restore files into; pass --database-only to skip them")
	}
	opened, err := opts.OpenStores(ctx, opts.StoreConfig)
	if err != nil {
		return blobstore.Stores{}, nil, fmt.Errorf("opening the live blob storage: %w", err)
	}
	sealed, err := opts.StoreConfig.sealed(opts.Cipher, stamp)
	if err != nil {
		return blobstore.Stores{}, nil, err
	}
	if err := src.checkArchive(ctx, physicalOf(storesOf(opened))); err != nil {
		return blobstore.Stores{}, nil, err
	}
	return opened, sealed, nil
}

// swapPreflight is §10.2 step 2: no other client on the live database, the
// privileges a swap needs, free names for the two databases it creates, and
// writable stores. It changes nothing and returns the live database's oid.
func swapPreflight(ctx context.Context, ctl ControlDB, stores blobStores, live, restored, before string) (uint32, error) {
	if others, err := ctl.OtherConnections(ctx, live); err != nil {
		return 0, fmt.Errorf("checking for other connections: %w", err)
	} else if others > 0 {
		return 0, fmt.Errorf("%d other clients are connected to %s; stop Silo on every server and turn off automatic restarts, or use separate mode (--into)", others, live)
	}
	if err := ctl.CheckPrivileges(ctx, live); err != nil {
		return 0, fmt.Errorf("%w; stop here and use separate mode (--into) instead", err)
	}
	for _, name := range []string{restored, before} {
		if _, taken, err := ctl.OID(ctx, name); err != nil {
			return 0, err
		} else if taken {
			return 0, fmt.Errorf("a database named %s already exists; wait a second and run the restore again", name)
		}
	}
	liveOID, found, err := ctl.OID(ctx, live)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, fmt.Errorf("database %s does not exist", live)
	}
	for name, store := range stores {
		if err := store.Probe(ctx); err != nil {
			return 0, fmt.Errorf("the %s blob store is not writable: %w", name, err)
		}
	}
	return liveOID, nil
}

func errIf(cond bool, msg string) error {
	if cond {
		return errors.New(msg)
	}
	return nil
}

// physicalOf names where each store's objects are, so stores sharing a root
// count as one place.
func physicalOf(stores blobStores) func(string) (string, bool) {
	return func(name string) (string, bool) {
		store := stores[name]
		if store == nil {
			return "", false
		}
		return blobstore.Location(store), true
	}
}

func storesOf(s blobstore.Stores) blobStores {
	stores := blobStores{}
	if s.Assets != nil {
		stores[storeAssets] = s.Assets
	}
	if s.Operational != nil {
		stores[storeOperational] = s.Operational
	}
	return stores
}

func identityOf(store blobstore.Store) string {
	if store == nil {
		return ""
	}
	return store.Identity()
}

// privateIdentity is the identity storage.operational_identity records: the
// private bucket's, or empty when the operational store is the local root.
func privateIdentity(s blobstore.Stores) string {
	if s.Operational == nil || s.Local() {
		return ""
	}
	return s.Operational.Identity()
}

// journalMirror copies the journal next to the preserved blobs, in the store
// that holds backups.
func journalMirror(stores blobStores, prefix string) func(context.Context, []byte) error {
	store := stores[storeOperational]
	if store == nil {
		store = stores[storeAssets]
	}
	if store == nil {
		return nil
	}
	return func(ctx context.Context, data []byte) error {
		return store.Put(ctx, prefix+"journal.json", data)
	}
}

// dbState is where the swap's two databases are, read from their names by
// OID. The journal's rename records are never trusted for this: a crash or a
// lost reply can separate a statement from its record.
type dbState int

const (
	stateOriginal     dbState = iota + 1 // live and restored under their own names
	stateFirstRenamed                    // live renamed to before; restored untouched
	stateSwapped                         // restored holds the live name
	stateRolledBack                      // live under its name; restored dropped
)

// swap is §10.2 steps 5 to 8 and the rollback.
type swap struct {
	ctl      ControlDB
	j        *journal
	stores   blobStores
	out      io.Writer
	manifest *Manifest
}

func (s *swap) observe(ctx context.Context) (dbState, error) {
	h := s.j.header
	liveName, liveFound, err := s.ctl.NameOf(ctx, h.LiveOID)
	if err != nil {
		return 0, err
	}
	restName, restFound, err := s.ctl.NameOf(ctx, h.RestoredOID)
	if err != nil {
		return 0, err
	}
	switch {
	case liveFound && liveName == h.LiveDatabase && restFound && restName == h.RestoredDatabase:
		return stateOriginal, nil
	case liveFound && liveName == h.BeforeDatabase && restFound && restName == h.RestoredDatabase:
		return stateFirstRenamed, nil
	case liveFound && liveName == h.BeforeDatabase && restFound && restName == h.LiveDatabase:
		return stateSwapped, nil
	case liveFound && liveName == h.LiveDatabase && !restFound:
		return stateRolledBack, nil
	}
	describe := func(name string, found bool) string {
		if !found {
			return "gone"
		}
		return "named " + name
	}
	return 0, fmt.Errorf("%w: the original database (oid %d) is %s and the restored one (oid %d) is %s",
		ErrSwapUnresolved, h.LiveOID, describe(liveName, liveFound), h.RestoredOID, describe(restName, restFound))
}

func (s *swap) run(ctx context.Context, src *Source, databaseOnly bool) error {
	h := s.j.header
	// Step 5: quiesce. From here until step 7 or the rollback finishes, the
	// live database stays closed.
	if err := s.ctl.AllowConnections(ctx, h.LiveDatabase, false); err != nil {
		return s.settle(ctx, fmt.Errorf("closing %s: %w", h.LiveDatabase, err))
	}
	if err := s.j.step(ctx, stepLiveClosed); err != nil {
		return s.settle(ctx, err)
	}
	if err := s.ctl.Terminate(ctx, h.LiveDatabase); err != nil {
		return s.settle(ctx, fmt.Errorf("ending sessions on %s: %w", h.LiveDatabase, err))
	}
	// Step 6.
	switch {
	case !src.Manifest.HasBlobs():
		_, _ = fmt.Fprintln(s.out, noBlobsNote)
	case databaseOnly:
		_, _ = fmt.Fprintln(s.out, "Skipped files (--database-only): uploaded artwork, avatars, and branding will be missing.")
	default:
		n, err := writeBlobsFromTar(ctx, src.path(BlobsName), s.stores, func(e blobEntry) error { return s.preserve(ctx, e) })
		if err != nil {
			return s.settle(ctx, err)
		}
		_, _ = fmt.Fprintf(s.out, "Restored %d files; the ones they replaced are kept under %s.\n", n, h.PreservePrefix)
	}
	if err := s.j.step(ctx, stepBlobsWritten); err != nil {
		return s.settle(ctx, err)
	}
	// Step 7. Each rename is recorded as intended first, and whether it took
	// effect is read back by OID, whatever the statement answered.
	if err := s.renameStep(ctx, stepRenameLiveIntent, h.LiveDatabase, h.BeforeDatabase, stateFirstRenamed, stepLiveRenamed); err != nil {
		return s.settle(ctx, err)
	}
	if err := s.renameStep(ctx, stepRenameRestIntent, h.RestoredDatabase, h.LiveDatabase, stateSwapped, stepRestoredRenamed); err != nil {
		return s.settle(ctx, err)
	}
	// The swap is done once the second rename lands; nothing after it rolls
	// back.
	return s.reopen(ctx)
}

func (s *swap) renameStep(ctx context.Context, intent, from, to string, want dbState, done string) error {
	if err := s.j.step(ctx, intent); err != nil {
		return err
	}
	renameErr := s.ctl.Rename(ctx, from, to)
	state, err := s.observe(ctx)
	if err != nil {
		return errors.Join(renameErr, err)
	}
	if state != want {
		if renameErr == nil {
			renameErr = errors.New("the rename did not take effect")
		}
		return fmt.Errorf("renaming %s to %s: %w", from, to, renameErr)
	}
	return s.j.step(ctx, done)
}

// settle ends a swap that failed: it finishes the swap when the databases
// show it completed, rolls back otherwise, and stops with everything closed
// when the databases are in no state it knows.
func (s *swap) settle(ctx context.Context, cause error) error {
	// Settling finishes or rolls back whatever stopped the swap, an
	// interrupt included, within one cleanup deadline.
	ctx, cancel := cleanupContext(ctx)
	defer cancel()
	state, err := s.observe(ctx)
	if err != nil {
		return s.stuck(cause, err)
	}
	if state == stateSwapped {
		_, _ = fmt.Fprintf(s.out, "%v\nThe databases show the swap had already completed; finishing it.\n", cause)
		return s.reopen(ctx)
	}
	return s.rollback(ctx, cause)
}

func (s *swap) stuck(cause, err error) error {
	h := s.j.header
	_, _ = fmt.Fprintf(s.out, "Stopped%s: %v\nNothing more was changed. The databases stay closed; the journal is %s.\nCheck the databases, then run: silo backup restore --recover %s\n", timedOut(err), err, s.j.path, h.Time)
	return fmt.Errorf("%w: %w (after: %w)", ErrRollbackIncomplete, err, cause)
}

// preserve copies a key the restore is about to overwrite, and records the
// key before it changes either way.
func (s *swap) preserve(ctx context.Context, entry blobEntry) error {
	store := s.stores[entry.store]
	preserved := false
	body, _, err := store.Get(ctx, entry.key)
	switch {
	case errors.Is(err, blobstore.ErrNotFound):
	case err != nil:
		return fmt.Errorf("reading %s/%s before replacing it: %w", entry.store, entry.key, err)
	default:
		putErr := store.PutStream(ctx, s.j.header.PreservePrefix+entry.key, body, blobstore.MediaType(entry.key))
		_ = body.Close()
		if putErr != nil {
			return fmt.Errorf("preserving %s/%s: %w", entry.store, entry.key, putErr)
		}
		preserved = true
	}
	return s.j.blob(journalBlob{Store: entry.store, Key: entry.key, Preserved: preserved})
}

func (s *swap) reopen(ctx context.Context) error {
	h := s.j.header
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = s.ctl.AllowConnections(ctx, h.LiveDatabase, true); err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		_, _ = fmt.Fprintf(s.out, "The swap is done, but %s is still closed to connections (%v). Run:\n  ALTER DATABASE %s WITH ALLOW_CONNECTIONS true;\n", h.LiveDatabase, err, quoteIdent(h.LiveDatabase))
		return fmt.Errorf("reopening %s: %w", h.LiveDatabase, err)
	}
	_ = s.j.step(ctx, stepLiveReopened)
	if s.manifest != nil {
		printMissing(s.out, s.manifest)
	}
	_, _ = fmt.Fprintf(s.out, `Restored %s into %s.
The previous database is kept as %s, closed to connections, and the files the restore replaced are under %s.
Once the restored server works, drop them:
  DROP DATABASE %s;
  (delete %s from the blob store)
To go back instead, stop Silo, copy the files under %s back over their keys, then:
  ALTER DATABASE %s RENAME TO %s;
  ALTER DATABASE %s RENAME TO %s;
  ALTER DATABASE %s WITH ALLOW_CONNECTIONS true;
`, h.BackupID, h.LiveDatabase, h.BeforeDatabase, h.PreservePrefix,
		quoteIdent(h.BeforeDatabase), h.PreservePrefix,
		h.PreservePrefix,
		quoteIdent(h.LiveDatabase), quoteIdent(h.RestoredDatabase),
		quoteIdent(h.BeforeDatabase), quoteIdent(h.LiveDatabase),
		quoteIdent(h.LiveDatabase))
	return nil
}

// rollback undoes a failure in step 6 or before the second rename completes,
// in this order, with connections blocked throughout and the original
// database reopened last:
//
//  1. undo the first rename, so the original database has the live name;
//  2. put preserved blobs back and delete keys the restore added;
//  3. drop the restored database;
//  4. reopen the original database.
//
// Each database step reads the databases by OID first, so running it again
// after a crash is safe. A step that fails stops the rollback with everything
// kept and the databases closed; the journal lets --recover finish it.
func (s *swap) rollback(ctx context.Context, cause error) error {
	ctx, cancel := cleanupContext(ctx)
	defer cancel()
	h := s.j.header
	_, _ = fmt.Fprintf(s.out, "Rolling back (%v)…\n", cause)
	steps := []struct {
		name, describe string
		run            func() error
	}{
		{stepRollbackRename, fmt.Sprintf("rename %s back to %s", h.BeforeDatabase, h.LiveDatabase), func() error {
			return s.untilState(ctx, stateFirstRenamed, func() error { return s.ctl.Rename(ctx, h.BeforeDatabase, h.LiveDatabase) }, stateOriginal, stateRolledBack)
		}},
		{stepRollbackBlobs, "put the preserved files back and delete the files the restore added", func() error {
			if s.j.has(stepRollbackBlobs) {
				return nil
			}
			return s.restoreBlobs(ctx)
		}},
		{stepRollbackDrop, "drop " + h.RestoredDatabase, func() error {
			return s.untilState(ctx, stateOriginal, func() error { return s.ctl.Drop(ctx, h.RestoredDatabase) }, stateRolledBack)
		}},
		{stepRollbackReopened, "reopen " + h.LiveDatabase, func() error {
			if state, err := s.observe(ctx); err != nil {
				return err
			} else if state != stateRolledBack {
				return fmt.Errorf("%w before reopening it", ErrSwapUnresolved)
			}
			return s.ctl.AllowConnections(ctx, h.LiveDatabase, true)
		}},
	}
	for i, step := range steps {
		if err := step.run(); err != nil {
			_, _ = fmt.Fprintf(s.out, "Rollback stopped%s: could not %s: %v\nThe databases stay closed. Kept: %s, %s, the files under %s, and the journal %s.\nRemaining steps:\n",
				timedOut(err), step.describe, err, h.LiveDatabase, h.RestoredDatabase, h.PreservePrefix, s.j.path)
			for _, rest := range steps[i:] {
				_, _ = fmt.Fprintf(s.out, "  - %s\n", rest.describe)
			}
			_, _ = fmt.Fprintf(s.out, "Fix the cause, then run: silo backup restore --recover %s\n", h.Time)
			return fmt.Errorf("%w: %w (after: %w)", ErrRollbackIncomplete, err, cause)
		}
		if err := s.j.step(ctx, step.name); err != nil {
			_, _ = fmt.Fprintf(s.out, "Rollback stopped%s: could not record that it could %s: %v\nThe databases stay closed; the journal is %s.\nFix the cause, then run: silo backup restore --recover %s\n",
				timedOut(err), step.describe, err, s.j.path, h.Time)
			return fmt.Errorf("%w: %w (after: %w)", ErrRollbackIncomplete, err, cause)
		}
	}
	_, _ = fmt.Fprintln(s.out, "Rolled back: the deployment is as it was before the restore.")
	return cause
}

// untilState runs act when the databases are in from, and requires them to
// end in one of done. Already being in done is success; anything else is an
// error, never a guess.
func (s *swap) untilState(ctx context.Context, from dbState, act func() error, done ...dbState) error {
	state, err := s.observe(ctx)
	if err != nil {
		return err
	}
	if slices.Contains(done, state) {
		return nil
	}
	if state != from {
		return fmt.Errorf("%w for this step", ErrSwapUnresolved)
	}
	actErr := act()
	if state, err = s.observe(ctx); err != nil {
		return errors.Join(actErr, err)
	}
	if slices.Contains(done, state) {
		return nil
	}
	if actErr == nil {
		actErr = errors.New("the statement did not take effect")
	}
	return actErr
}

// restoreBlobs undoes every blob the journal records, newest first. Each undo
// is idempotent, so a rollback that stopped can run again.
func (s *swap) restoreBlobs(ctx context.Context) error {
	blobs := s.j.blobs
	for i := len(blobs) - 1; i >= 0; i-- {
		b := blobs[i]
		store := s.stores[b.Store]
		if store == nil {
			return fmt.Errorf("no %s store to restore %s into", b.Store, b.Key)
		}
		if !b.Preserved {
			if _, err := store.Delete(ctx, []string{b.Key}); err != nil {
				return fmt.Errorf("deleting %s/%s: %w", b.Store, b.Key, err)
			}
			continue
		}
		body, _, err := store.Get(ctx, s.j.header.PreservePrefix+b.Key)
		if err != nil {
			return fmt.Errorf("reading the preserved copy of %s/%s: %w", b.Store, b.Key, err)
		}
		err = store.PutStream(ctx, b.Key, body, blobstore.MediaType(b.Key))
		_ = body.Close()
		if err != nil {
			return fmt.Errorf("putting back %s/%s: %w", b.Store, b.Key, err)
		}
	}
	return nil
}

// RecoverSwap finishes a swap that stopped, from its local journal: the swap
// when the databases show the second rename landed, the rollback otherwise.
// It opens no connection to either database: what it needs to know about
// them comes from pg_database over the control connection, and the blob
// storage comes from the journal, its credentials unsealed with cipher.
func RecoverSwap(ctx context.Context, journalPath string, ctl ControlDB, cipher *secret.Cipher, openStores func(context.Context, StoreConfig) (blobstore.Stores, error), out io.Writer) error {
	out = writerOrDiscard(out)
	j, err := openJournal(journalPath)
	if err != nil {
		return err
	}
	defer j.close()
	s := &swap{ctl: ctl, j: j, out: out, stores: blobStores{}}
	state, err := s.observe(ctx)
	if err != nil {
		return s.stuck(errors.New("recovering an interrupted restore"), err)
	}
	if state == stateSwapped {
		return s.reopen(ctx)
	}
	if len(j.blobs) > 0 && !j.has(stepRollbackBlobs) {
		cfg, err := j.header.Stores.opened(cipher, j.header.Time)
		if err != nil {
			return err
		}
		stores, err := openStores(ctx, cfg)
		if err != nil {
			return fmt.Errorf("opening the blob stores: %w", err)
		}
		s.stores = storesOf(stores)
		j.mirror = journalMirror(s.stores, j.header.PreservePrefix)
	}
	err = s.rollback(ctx, errors.New("recovering an interrupted restore"))
	if errors.Is(err, ErrRollbackIncomplete) {
		return err
	}
	return nil
}
