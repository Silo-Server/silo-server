package backup

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/secret"
)

var (
	// ErrKeyMismatch refuses a backup taken with another SECRET_KEY unless
	// the operator accepts losing its encrypted settings and credentials.
	ErrKeyMismatch = errors.New("this backup was taken with a different SECRET_KEY")
	// ErrNewerSchema refuses a backup whose schema this binary cannot run.
	ErrNewerSchema = errors.New("this backup is from a newer Silo; upgrade this binary first")
	// ErrUnknownMigrations refuses a backup whose database has a migration
	// this binary does not carry, such as one from another build's branch.
	ErrUnknownMigrations = errors.New("this backup's database has migrations this Silo does not carry")
	// ErrRollbackIncomplete reports a swap whose rollback stopped partway: the
	// databases stay closed and the journal names what remains.
	ErrRollbackIncomplete = errors.New("the rollback did not finish")
)

// Source is a backup folder on local disk: the folder at a local destination
// (<root>/backups/<server-id>/<id>), or a downloaded copy of its objects.
type Source struct {
	Dir      string
	Manifest *Manifest
	// private is the directory of the checked copies Validate reads from
	// (privatize); origin is the folder they came from.
	private, origin string
	// authenticated is set once Validate verified the manifest's MAC.
	authenticated bool
}

// OpenSource reads a backup folder's manifest.
func OpenSource(dir string) (*Source, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestName)) //nolint:gosec // G304: the backup folder the operator named
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s has no %s: it is not a complete backup", dir, ManifestName)
	}
	if err != nil {
		return nil, err
	}
	m, err := ParseManifest(data)
	if err != nil {
		return nil, err
	}
	return &Source{Dir: dir, Manifest: m}, nil
}

func (src *Source) path(name string) string { return filepath.Join(src.Dir, name) }

// ValidateOptions are the inputs to §10.2 step 1.
type ValidateOptions struct {
	// KeyCheck is the check value of the SECRET_KEY in the environment.
	KeyCheck string
	// BinaryGooseVersion is the newest migration this binary carries.
	BinaryGooseVersion int64
	// BinaryMigrations is every migration this binary carries, ascending
	// (database.MigrationVersions). Nil skips the check of the backup's
	// applied migrations against it.
	BinaryMigrations      []int64
	AcceptLostCredentials bool
	// AcceptSuperuser loads a backup that cannot be authenticated as a
	// PostgreSQL superuser (checkLoadRole).
	AcceptSuperuser bool
	// ManifestKey is secret.BackupManifestKey of the SECRET_KEY in the
	// environment: a backup written under that key must carry a manifest
	// MAC that verifies. Nil checks none.
	ManifestKey []byte
	// CopyDir is where Validate makes the private copies a restore reads
	// (Source.privatize); empty is os.TempDir.
	CopyDir string
}

// checkMigrations refuses applied migrations this binary does not carry,
// naming them. A nil binary list checks nothing.
func (opts ValidateOptions) checkMigrations(applied []int64, where string) error {
	if opts.BinaryMigrations == nil {
		return nil
	}
	var unknown []string
	for _, v := range applied {
		if _, found := slices.BinarySearch(opts.BinaryMigrations, v); !found {
			unknown = append(unknown, strconv.FormatInt(v, 10))
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s %s; restore it with the Silo build that applied them", ErrUnknownMigrations, where, strings.Join(unknown, ", "))
}

// Validate checks every input before anything changes: checksums, the
// archive's entries, the schema version, and the key.
func (src *Source) Validate(ctx context.Context, opts ValidateOptions) error {
	m := src.Manifest
	// First, so nothing below trusts a checksum anyone with write access to
	// the backup could have changed with its object.
	authenticated, err := m.checkMAC(opts.ManifestKey, opts.KeyCheck)
	if err != nil {
		return err
	}
	src.authenticated = authenticated
	if !authenticated && !opts.AcceptLostCredentials {
		return unauthenticatedError(m, opts.KeyCheck)
	}
	// Everything from here reads checked private copies, never the folder
	// again.
	if err := src.privatize(opts.CopyDir); err != nil {
		return err
	}
	if err := src.checkArchive(ctx, nil); err != nil {
		return err
	}
	if opts.BinaryGooseVersion > 0 && m.GooseVersion > opts.BinaryGooseVersion {
		return fmt.Errorf("%w: schema version %d, this binary %d", ErrNewerSchema, m.GooseVersion, opts.BinaryGooseVersion)
	}
	// A manifest from before the applied list was recorded has none; the
	// restored database is checked instead (verifyRestored).
	if err := opts.checkMigrations(m.AppliedMigrations, "the backup records"); err != nil {
		return err
	}
	return nil
}

// ErrUnauthenticatedBackup refuses a backup whose manifest this server
// cannot authenticate, without the operator's --accept-lost-credentials.
var ErrUnauthenticatedBackup = errors.New("this backup cannot be verified")

// unauthenticatedError says why m cannot be authenticated and what accepts
// it. A backup taken with another key is ErrKeyMismatch too: its encrypted
// settings are lost with it, which is what --accept-lost-credentials
// accepts, and its MAC was made with that key.
func unauthenticatedError(m *Manifest, keyCheck string) error {
	switch {
	case keyCheck == "":
		return fmt.Errorf("%w: SECRET_KEY is not set, so its manifest cannot be checked. Set SECRET_KEY, or pass --accept-lost-credentials to restore it unverified", ErrUnauthenticatedBackup)
	case m.KeyCheck == "":
		return fmt.Errorf("%w: its manifest records no SECRET_KEY check value, which every Silo backup has, so it may have been changed. Pass --accept-lost-credentials to restore it unverified, encrypted settings and credentials included as lost", ErrUnauthenticatedBackup)
	}
	return fmt.Errorf("%w (backup %s, this key %s): %w; encrypted settings and credentials in it cannot be read with this key, and its manifest cannot be verified, so it may have been changed. Pass --accept-lost-credentials to restore it anyway",
		ErrUnauthenticatedBackup, m.KeyCheck, keyCheck, ErrKeyMismatch)
}

// ErrSuperuserLoad refuses to load a dump that cannot be authenticated as a
// PostgreSQL superuser.
var ErrSuperuserLoad = errors.New("refusing to load a backup that cannot be verified as a PostgreSQL superuser")

// msgUnverifiedLoad warns that the restore loads a backup it accepted
// unauthenticated.
const msgUnverifiedLoad = "Warning: this backup cannot be verified: it was taken with another SECRET_KEY, records none, or SECRET_KEY is not set here. Anyone who could write to its storage could have changed it, and encrypted settings and credentials in it cannot be read."

// checkLoadRole runs before pg_restore loads the dump into the database
// dbURL names, on that database's server. An authenticated dump is this
// deployment's own. One that is not could have been changed by anyone who
// could write to the backup's storage, and a crafted archive runs any SQL
// pg_restore lets it with the connecting role's rights; for a superuser
// that is the whole cluster. So it says so, and refuses a superuser role
// unless the operator passed --accept-superuser.
func checkLoadRole(ctx context.Context, src *Source, loader DatabaseLoader, dbURL string, validate ValidateOptions, out io.Writer) error {
	if src.authenticated {
		return nil
	}
	_, _ = fmt.Fprintln(out, msgUnverifiedLoad)
	super, err := loader.Superuser(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("reading whether the restoring role is a superuser: %w", err)
	}
	if super && !validate.AcceptSuperuser {
		return fmt.Errorf("%w: the role in the database URL is a superuser, so a changed dump could run any SQL on the whole PostgreSQL server. "+
			"Connect as a role that is not a superuser and owns only the database to restore into, or pass --accept-superuser", ErrSuperuserLoad)
	}
	return nil
}

// checkArchive validates blobs.tar against where its entries would land and
// against the manifest's count. physical is as for validateBlobArchive.
func (src *Source) checkArchive(ctx context.Context, physical func(store string) (string, bool)) error {
	n, err := validateBlobArchive(ctx, src.path(BlobsName), physical)
	if err != nil {
		return err
	}
	if n != src.Manifest.Blobs.Count {
		return fmt.Errorf("blobs.tar holds %d files, the manifest records %d", n, src.Manifest.Blobs.Count)
	}
	return nil
}

// DatabaseLoader restores a dump into a database and reads one back.
type DatabaseLoader interface {
	// Empty reports whether a database holds no relations of its own.
	Empty(ctx context.Context, dbURL string) (bool, error)
	// Load runs pg_restore --exit-on-error of dumpPath into dbURL.
	Load(ctx context.Context, dbURL, dumpPath string) error
	// Inspect reads the applied migrations and the core table inventory.
	Inspect(ctx context.Context, dbURL string) (Inspection, error)
	// UnbindStorage deletes the recorded storage identities
	// (blobstore.IdentitySettingKey, blobstore.OperationalIdentitySettingKey)
	// from a restored database, so the server started on it binds the stores
	// it is configured with instead of refusing them.
	UnbindStorage(ctx context.Context, dbURL string) error
	// ClearUnreadableSettings deletes the encrypted server_settings rows
	// cipher cannot open (nil opens none) and returns their keys. Rows are
	// only deleted: a value is bound to its key name.
	ClearUnreadableSettings(ctx context.Context, dbURL string, cipher *secret.Cipher) ([]string, error)
	// DropSubtitles deletes the restored database's provider subtitle rows
	// (not uploads) whose object exists returns false for, or all of them
	// when exists is nil, and returns how many it deleted.
	DropSubtitles(ctx context.Context, dbURL string, exists func(ctx context.Context, key string) (bool, error)) (int, error)
	// RotateSigningSecrets replaces the restored database's signing keys
	// (rotatedSettings) with new ones encrypted with cipher, or deletes
	// them when cipher is nil, and reports whether it wrote new ones.
	RotateSigningSecrets(ctx context.Context, dbURL string, cipher *secret.Cipher) (bool, error)
	// CarryCredentials replaces the restored database's long-lived
	// credentials (secretTables, secretCarried) with liveURL's, or deletes them when
	// liveURL is "".
	CarryCredentials(ctx context.Context, dbURL, liveURL string) (CarriedCredentials, error)
	// KeepStricterSettings writes into dbURL the live server's (liveURL)
	// value of each access setting the restored data would widen, and
	// returns those keys.
	KeepStricterSettings(ctx context.Context, dbURL, liveURL string, cipher *secret.Cipher) ([]string, error)
	// ServerID reads the server identity (serveridentity.Key) dbURL's
	// settings hold, or "" when they hold none.
	ServerID(ctx context.Context, dbURL string) (string, error)
	// CarryPolicies gives dbURL liveURL's custom access policies ("" for
	// none), returning the backup's it turned off.
	CarryPolicies(ctx context.Context, dbURL, liveURL string) ([]string, error)
	// CarryPluginReleases gives dbURL's plugin installations liveURL's
	// releases ("" for none), returning those it turned off.
	CarryPluginReleases(ctx context.Context, dbURL, liveURL string) ([]string, error)
	// CarryServerSpecificSettings gives dbURL liveURL's server-specific
	// settings and returns how many it changed.
	CarryServerSpecificSettings(ctx context.Context, dbURL, liveURL string) (int, error)
	// CarryOutboundSettings gives dbURL liveURL's outbound settings, each
	// bundle as a unit, clearing the secrets of one liveURL lacks ("" for
	// none), and returns the bundles carried and cleared.
	CarryOutboundSettings(ctx context.Context, dbURL, liveURL string) (carried, cleared []string, err error)
	// InterruptWork ends the work queued or running in the restored
	// database dbURL and gives it liveURL's storage transition target
	// (none for ""), returning what it changed.
	InterruptWork(ctx context.Context, dbURL, liveURL string) ([]string, error)
	// Extensions lists the extensions the dump at dumpPath creates.
	Extensions(ctx context.Context, dumpPath string) ([]string, error)
	// Superuser reports whether the role dbURL connects as is a superuser.
	Superuser(ctx context.Context, dbURL string) (bool, error)
	// AccountRollback compares dbURL's accounts with liveURL's, by id.
	AccountRollback(ctx context.Context, dbURL, liveURL string) (AccountRollback, error)
}

// clearLostSettings removes, from a restored database taken with another
// SECRET_KEY and restored with --accept-lost-credentials, the encrypted
// settings this key cannot read: Silo refuses to start on a setting it
// cannot decrypt, so they are deleted and listed for the admin to enter
// again. A backup taken with this key changes nothing.
func clearLostSettings(ctx context.Context, loader DatabaseLoader, dbURL string, m *Manifest, validate ValidateOptions, cipher *secret.Cipher, out io.Writer) error {
	if !validate.AcceptLostCredentials || m.KeyCheck == "" || m.KeyCheck == validate.KeyCheck {
		return nil
	}
	keys, err := loader.ClearUnreadableSettings(ctx, dbURL, cipher)
	if err != nil {
		return fmt.Errorf("clearing the settings this SECRET_KEY cannot read: %w", err)
	}
	if len(keys) > 0 {
		_, _ = fmt.Fprintf(out, "Cleared %d encrypted settings this SECRET_KEY cannot read; enter them again after Silo starts: %s\n", len(keys), strings.Join(keys, ", "))
	}
	return nil
}

// dropOmittedSubtitles removes, when the backup left provider subtitles out
// (backup.include_subtitles off), the restored rows whose object is not in
// the stores the restored server uses, so players do not list tracks with
// no content. Silo downloads them again when asked; uploads are always in
// the backup. exists nil means none is there.
func dropOmittedSubtitles(ctx context.Context, loader DatabaseLoader, dbURL string, m *Manifest, exists func(context.Context, string) (bool, error), out io.Writer) error {
	if m.Blobs.IncludesSubtitles {
		return nil
	}
	n, err := loader.DropSubtitles(ctx, dbURL, exists)
	if err != nil {
		return fmt.Errorf("removing the downloaded subtitles the backup left out: %w", err)
	}
	if n > 0 {
		_, _ = fmt.Fprintf(out, "Removed %d downloaded subtitles the backup left out; Silo downloads them again when they are chosen.\n", n)
	}
	return nil
}

// Inspection is what DatabaseLoader.Inspect reads from a database.
type Inspection struct {
	// GooseVersion is the newest applied migration; Applied is every one,
	// ascending, without goose's version 0.
	GooseVersion int64
	Applied      []int64
	Tables       []TableCount
}

// verifyRestored is the check of §10.2 step 4: the schema version is
// readable, the applied migrations are the manifest's (when it records them)
// and all ones this binary carries, and the core table inventory matches the
// manifest, presence and counts both.
func verifyRestored(ctx context.Context, loader DatabaseLoader, dbURL string, m *Manifest, opts ValidateOptions) error {
	in, err := loader.Inspect(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("reading the restored database: %w", err)
	}
	if in.GooseVersion != m.GooseVersion {
		return fmt.Errorf("restored schema version %d, the manifest records %d", in.GooseVersion, m.GooseVersion)
	}
	if m.AppliedMigrations != nil && !slices.Equal(in.Applied, m.AppliedMigrations) {
		return errors.New("the restored database's applied migrations differ from the ones the manifest records")
	}
	if err := opts.checkMigrations(in.Applied, "the restored database has"); err != nil {
		return err
	}
	got := map[string]TableCount{}
	for _, t := range in.Tables {
		got[t.Name] = t
	}
	var diffs []string
	for _, want := range m.Tables {
		have := got[want.Name]
		if have.Present != want.Present || have.Rows != want.Rows {
			diffs = append(diffs, fmt.Sprintf("%s: backup %s, restored %s", want.Name, describeCount(want), describeCount(have)))
		}
	}
	if len(diffs) > 0 {
		return fmt.Errorf("the restored inventory does not match the manifest: %s", strings.Join(diffs, "; "))
	}
	return nil
}

func describeCount(t TableCount) string {
	if !t.Present {
		return "absent"
	}
	return fmt.Sprintf("%d rows", t.Rows)
}

// RestoreReport is what a restore found and did.
type RestoreReport struct {
	Database     string
	BlobsWritten int
	MissingBlobs []MissingBlob
}

func printMissing(out io.Writer, m *Manifest) {
	if len(m.MissingBlobs) == 0 {
		return
	}
	_, _ = fmt.Fprintf(out, "%d files were already missing when this backup was taken:\n", len(m.MissingBlobs))
	for _, b := range m.MissingBlobs {
		_, _ = fmt.Fprintf(out, "  %s %s (%s)\n", b.Store, b.Key, b.Source)
	}
}

// SeparateOptions restores into a new location, touching nothing live.
type SeparateOptions struct {
	Validate ValidateOptions
	// Cipher is this server's SECRET_KEY, which decides the encrypted
	// settings a restore with another key clears; nil clears them all.
	Cipher *secret.Cipher
	// Into is the pre-created empty database to restore into.
	Into string
	// BlobsInto is a directory that receives the blobs, laid out as a local
	// store root. Required unless DatabaseOnly.
	BlobsInto    string
	DatabaseOnly bool
	// LiveURL, when known, is the live server's database, which Into must
	// not be; LiveBlobRoots are its local store directories, which
	// BlobsInto must not be.
	LiveURL       string
	LiveBlobRoots []string
	Out           io.Writer
}

// RestoreSeparate runs §10.2 steps 1 and 3 into opts.Into, the verification
// of step 4, and writes the blobs to opts.BlobsInto. The target database stays
// open for the operator to point DATABASE_URL at.
func RestoreSeparate(ctx context.Context, src *Source, loader DatabaseLoader, opts SeparateOptions) (RestoreReport, error) {
	out := writerOrDiscard(opts.Out)
	if !opts.DatabaseOnly && opts.BlobsInto == "" {
		return RestoreReport{}, errors.New("--blobs-into is required unless --database-only")
	}
	if err := src.Validate(ctx, opts.Validate); err != nil {
		return RestoreReport{}, err
	}
	if err := checkSeparateTargets(opts); err != nil {
		return RestoreReport{}, err
	}
	// Both stores land in one directory.
	if !opts.DatabaseOnly {
		if err := src.checkArchive(ctx, func(string) (string, bool) { return opts.BlobsInto, true }); err != nil {
			return RestoreReport{}, err
		}
	}
	if err := loadAndVerify(ctx, src, loader, opts.Into, opts.Validate, out); err != nil {
		return RestoreReport{}, err
	}
	// The restored settings name the backed-up server's stores, which are
	// not where this copy's files go.
	if err := loader.UnbindStorage(ctx, opts.Into); err != nil {
		return RestoreReport{}, fmt.Errorf("clearing the restored storage identities: %w", err)
	}
	// New signing keys first, so the lost-settings pass does not list them.
	if err := rotateSigningSecrets(ctx, loader, opts.Into, opts.Cipher, out); err != nil {
		return RestoreReport{}, err
	}
	if err := clearLostSettings(ctx, loader, opts.Into, src.Manifest, opts.Validate, opts.Cipher, out); err != nil {
		return RestoreReport{}, err
	}
	// A copy is not the live server: it gets none of the backup's
	// credentials and none of the live ones.
	if err := carryCredentials(ctx, loader, opts.Into, "", out); err != nil {
		return RestoreReport{}, err
	}
	if err := carryOutboundSettings(ctx, loader, opts.Into, "", out); err != nil {
		return RestoreReport{}, err
	}
	if err := carryPluginReleases(ctx, loader, opts.Into, "", out); err != nil {
		return RestoreReport{}, err
	}
	if err := carryPolicies(ctx, loader, opts.Into, "", out); err != nil {
		return RestoreReport{}, err
	}
	if err := interruptWork(ctx, loader, opts.Into, "", out); err != nil {
		return RestoreReport{}, err
	}
	// The copy's files land in an empty directory, so a subtitle the backup
	// left out is nowhere.
	if !opts.DatabaseOnly {
		if err := dropOmittedSubtitles(ctx, loader, opts.Into, src.Manifest, nil, out); err != nil {
			return RestoreReport{}, err
		}
	}
	report := RestoreReport{Database: opts.Into, MissingBlobs: src.Manifest.MissingBlobs}
	if opts.DatabaseOnly {
		_, _ = fmt.Fprintln(out, "Skipped files (--database-only): uploaded artwork, avatars, and branding will be missing.")
	} else {
		n, err := writeSeparateBlobs(ctx, src, opts)
		if err != nil {
			return report, err
		}
		report.BlobsWritten = n
		_, _ = fmt.Fprintf(out, "Wrote %d files to %s.\n", n, opts.BlobsInto)
	}
	printMissing(out, src.Manifest)
	_, _ = fmt.Fprintln(out, "Restored. Point DATABASE_URL and the artwork storage settings at the new locations, then start Silo.")
	return report, nil
}

// checkSeparateTargets refuses, before anything is written, a separate
// restore aimed at the live server or at files it would overwrite: a copy
// keeps nothing it replaces. Into must not be the live database (and must
// be empty, which loading checks), and BlobsInto must be a new or empty
// directory that is not one of the live server's stores.
func checkSeparateTargets(opts SeparateOptions) error {
	if opts.LiveURL != "" && sameDatabase(opts.Into, opts.LiveURL) {
		return errors.New("--into is this server's own database; create a new, empty database to restore into")
	}
	if opts.DatabaseOnly {
		return nil
	}
	dir := resolvedPath(opts.BlobsInto)
	for _, root := range opts.LiveBlobRoots {
		if root != "" && resolvedPath(root) == dir {
			return fmt.Errorf("--blobs-into %s is this server's own file storage; restore files into a new, empty directory", opts.BlobsInto)
		}
	}
	f, err := os.Open(opts.BlobsInto)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking --blobs-into: %w", err)
	}
	defer func() { _ = f.Close() }()
	names, err := f.Readdirnames(1)
	switch {
	case errors.Is(err, io.EOF):
		return nil
	case err != nil:
		return fmt.Errorf("checking --blobs-into: %w", err)
	case len(names) > 0:
		return fmt.Errorf("--blobs-into %s is not empty; restore files into a new, empty directory, so nothing in it is overwritten", opts.BlobsInto)
	}
	return nil
}

// resolvedPath is path made absolute, with symbolic links resolved when it
// exists.
func resolvedPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// sameDatabase reports whether two connection strings reach the same
// database on the same server, as pgx resolves them; one that does not
// parse is compared as text.
func sameDatabase(a, b string) bool {
	ca, errA := pgconn.ParseConfig(a)
	cb, errB := pgconn.ParseConfig(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return ca.Host == cb.Host && ca.Port == cb.Port && ca.Database == cb.Database
}

// writeSeparateBlobs writes blobs.tar into the directory opts.BlobsInto;
// both stores land in it.
func writeSeparateBlobs(ctx context.Context, src *Source, opts SeparateOptions) (int, error) {
	root, err := blobstore.NewFilesystem(opts.BlobsInto)
	if err != nil {
		return 0, err
	}
	return writeBlobsFromTar(ctx, src.path(BlobsName), blobStores{storeAssets: root, storeOperational: root}, nil)
}

// VerifyBackup is the restore drill: §10.2 step 1 (which reads blobs.tar
// through), step 3 into an empty scratch database, and the verification
// checks of step 4.
func VerifyBackup(ctx context.Context, src *Source, loader DatabaseLoader, scratch string, validate ValidateOptions, out io.Writer) error {
	out = writerOrDiscard(out)
	if err := src.Validate(ctx, validate); err != nil {
		return err
	}
	if err := loadAndVerify(ctx, src, loader, scratch, validate, out); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "blobs.tar holds the %d files the manifest records.\n", src.Manifest.Blobs.Count)
	printMissing(out, src.Manifest)
	_, _ = fmt.Fprintln(out, "Verified: the backup restores and matches its manifest.")
	return nil
}

func loadAndVerify(ctx context.Context, src *Source, loader DatabaseLoader, dbURL string, validate ValidateOptions, out io.Writer) error {
	empty, err := loader.Empty(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("checking the target database: %w", err)
	}
	if !empty {
		return errors.New("the target database is not empty; create a new, empty database to restore into")
	}
	if err := checkLoadRole(ctx, src, loader, dbURL, validate, out); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "Restoring the database…")
	if err := loader.Load(ctx, dbURL, src.path(DumpName)); err != nil {
		return err
	}
	if err := verifyRestored(ctx, loader, dbURL, src.Manifest, validate); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "The restored database matches the manifest's table inventory.")
	return nil
}

// writeBlobsFromTar writes each entry to its store. before runs ahead of each
// write. The archive was validated as a whole first (checkArchive).
func writeBlobsFromTar(ctx context.Context, path string, stores blobStores, before func(blobEntry) error) (int, error) {
	f, err := os.Open(path) //nolint:gosec // G304: an object of the backup folder the operator named
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	guard := newTarGuard(f)
	tr := tar.NewReader(guard)
	count := 0
	for {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return count, nil
		}
		if err != nil {
			return count, fmt.Errorf("reading blobs.tar: %w", err)
		}
		if err := guard.checkEntry(h.Size); err != nil {
			return count, fmt.Errorf("reading blobs.tar: %w", err)
		}
		entry, err := parseBlobEntry(h)
		if err != nil {
			return count, err
		}
		store := stores[entry.store]
		if store == nil {
			return count, fmt.Errorf("blobs.tar holds %s files, and the target has no %s store", entry.store, entry.store)
		}
		if before != nil {
			if err := before(entry); err != nil {
				return count, err
			}
		}
		if err := store.PutStream(ctx, entry.key, io.LimitReader(tr, h.Size), blobstore.MediaType(entry.key)); err != nil {
			return count, fmt.Errorf("writing %s/%s: %w", entry.store, entry.key, err)
		}
		count++
	}
}

func writerOrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// quoteIdent quotes a database name for the SQL the command prints.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
