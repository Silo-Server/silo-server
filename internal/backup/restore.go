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
	"golang.org/x/sys/unix"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/s3client"
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
// (<root>/backups/<server-id>/<id>), a downloaded copy of its objects, or the
// plaintext FetchSource wrote of an encrypted or remote backup.
type Source struct {
	Dir      string
	Manifest *Manifest
	// fetched says Dir holds plaintext whose stored bytes were checked
	// against the manifest as they were fetched, into a folder only this
	// process can read; withoutBlobs that blobs.tar was not fetched, for a
	// database-only restore.
	fetched, withoutBlobs bool
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

// writesBlobs reports whether a restore from src writes blobs.tar: not for
// a database-only restore, and not from a backup that has none.
func (src *Source) writesBlobs() bool { return !src.withoutBlobs && src.Manifest.HasBlobs() }

// noBlobsNote is what a restore from a database-only backup prints instead
// of writing files.
const noBlobsNote = "This backup was taken before an upgrade and holds the database only; files in storage are left as they are."

// Objects reads the stored objects of one backup folder by name.
type Objects interface {
	Open(ctx context.Context, name string) (io.ReadCloser, error)
}

// DirObjects is a backup folder on local disk.
type DirObjects string

func (d DirObjects) Open(_ context.Context, name string) (io.ReadCloser, error) {
	f, err := os.Open(filepath.Join(string(d), name)) //nolint:gosec // G304: an object of the backup folder the operator named
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return f, err
}

// BucketObjects is a backup folder in a bucket: Prefix is the folder's key,
// such as <key prefix>/backups/<server-id>/<id>.
type BucketObjects struct {
	Client *s3client.Client
	Prefix string
}

func (b BucketObjects) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	body, err := b.Client.GetObjectStream(ctx, b.Client.Bucket(), strings.TrimSuffix(b.Prefix, "/")+"/"+name)
	if errors.Is(err, s3client.ErrNotFound) {
		return nil, ErrNotFound
	}
	return body, err
}

// noManifestError is a folder without a manifest. It wraps ErrNotFound.
type noManifestError struct{ where string }

func (e noManifestError) Error() string {
	return fmt.Sprintf("%s has no %s: it is not a complete backup", e.where, ManifestName)
}

func (e noManifestError) Unwrap() error { return ErrNotFound }

// ReadSourceManifest reads and parses a backup folder's manifest. A folder
// without one returns an error that wraps ErrNotFound.
func ReadSourceManifest(ctx context.Context, objects Objects, where string) (*Manifest, error) {
	body, err := objects.Open(ctx, ManifestName)
	if errors.Is(err, ErrNotFound) {
		return nil, noManifestError{where: where}
	}
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

// CheckKey refuses an encrypted backup that key cannot open, before anything
// is downloaded. key may be nil when SECRET_KEY is not set.
func CheckKey(m *Manifest, key *ArchiveKey) error {
	if !m.Encrypted {
		return nil
	}
	if key == nil {
		return fmt.Errorf("this backup is encrypted (key check %s); set SECRET_KEY to the key it was taken with", m.KeyCheck)
	}
	if m.KeyCheck != key.Check {
		return fmt.Errorf("%w (backup %s, this key %s); an encrypted backup needs the key it was taken with", ErrKeyMismatch, m.KeyCheck, key.Check)
	}
	return nil
}

// fetchReserve is the free space FetchSource leaves beyond what it fetches.
const fetchReserve = 256 << 20

// FetchOptions say what FetchSource fetches and where.
type FetchOptions struct {
	// Key decrypts an encrypted backup; nil cannot.
	Key *ArchiveKey
	// WorkDir receives the plaintext; the caller makes it (mode 0700) and
	// removes it.
	WorkDir string
	// DatabaseOnly fetches db.dump alone, for a restore that skips files.
	DatabaseOnly bool
	// FreeBytes reports the space free at WorkDir; nil asks the filesystem.
	FreeBytes func(dir string) (uint64, error)
	Out       io.Writer
}

// FetchSource copies the objects a full restore reads (db.dump, and
// blobs.tar unless opts.DatabaseOnly or the backup has none) into
// opts.WorkDir as plaintext,
// decrypting as each streams and checking its stored bytes against the
// manifest's size and SHA-256. Before anything is fetched, the working
// folder must have room for the objects' stored sizes (a plaintext is never
// larger) plus a reserve, and no object may write more than its stored
// size. A mismatch fails before anything is restored.
func FetchSource(ctx context.Context, objects Objects, m *Manifest, opts FetchOptions) (*Source, error) {
	if err := CheckKey(m, opts.Key); err != nil {
		return nil, err
	}
	out := writerOrDiscard(opts.Out)
	key := opts.Key
	if !m.Encrypted {
		key = nil
	}
	bases := []string{DumpName, BlobsName}
	if opts.DatabaseOnly || !m.HasBlobs() {
		bases = bases[:1]
	}
	entries := make([]ObjectEntry, 0, len(bases))
	var need uint64 = fetchReserve
	for _, base := range bases {
		entry, ok := m.object(base)
		if !ok {
			return nil, fmt.Errorf("the manifest lists no %s", base)
		}
		entries = append(entries, entry)
		need += uint64(max(entry.Size, 0))
	}
	freeBytes := opts.FreeBytes
	if freeBytes == nil {
		freeBytes = dirFreeBytes
	}
	free, err := freeBytes(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	if free < need {
		return nil, fmt.Errorf("%w in %s: %s free, %s needed to fetch the backup (its files plus %s)",
			ErrNotEnoughSpace, opts.WorkDir, humanBytes(int64(free)), humanBytes(int64(need)), humanBytes(fetchReserve)) //nolint:gosec // G115: sizes of real files
	}
	for i, entry := range entries {
		_, _ = fmt.Fprintf(out, "Fetching %s (%s)…\n", entry.Name, humanBytes(entry.Size))
		if err := fetchObject(ctx, objects, entry, key, filepath.Join(opts.WorkDir, bases[i])); err != nil {
			return nil, err
		}
	}
	return &Source{Dir: opts.WorkDir, Manifest: m, fetched: true, withoutBlobs: opts.DatabaseOnly}, nil
}

// dirFreeBytes is the space available to this user at dir.
func dirFreeBytes(dir string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, fmt.Errorf("reading free space in %s: %w", dir, err)
	}
	return blockBytes(st.Bavail, st.Bsize), nil
}

func fetchObject(ctx context.Context, objects Objects, entry ObjectEntry, key *ArchiveKey, path string) error {
	body, err := objects.Open(ctx, entry.Name)
	if err != nil {
		return fmt.Errorf("opening %s: %w", entry.Name, err)
	}
	plain, err := openStored(body, key)
	if err != nil {
		return fmt.Errorf("opening %s: %w", entry.Name, err)
	}
	defer func() { _ = plain.Close() }()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // G304: a file in the working directory this command made
	if err != nil {
		return err
	}
	// The space check counted the stored size, which a plaintext never
	// exceeds; an object that writes more is refused at that point.
	n, err := io.Copy(f, io.LimitReader(ctxReader{ctx: ctx, r: plain}, entry.Size+1))
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("reading %s: %w", entry.Name, err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if n > entry.Size {
		return fmt.Errorf("%s is larger than its manifest says", entry.Name)
	}
	stored, err := plain.storedEntry(entry.Name)
	if err != nil {
		return fmt.Errorf("reading %s: %w", entry.Name, err)
	}
	if stored.Size != entry.Size || stored.SHA256 != entry.SHA256 {
		return fmt.Errorf("%s does not match its checksum in the manifest", entry.Name)
	}
	return nil
}

// ctxReader stops a copy when ctx ends.
type ctxReader struct {
	ctx context.Context //nolint:containedctx // scoped to one copy
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}

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
	if m.Encrypted && !src.fetched {
		return errors.New("this backup is encrypted; read it with FetchSource, which decrypts it")
	}
	// First, so nothing below trusts a checksum anyone with write access to
	// the backup could have changed with its object.
	authenticated, err := opts.CheckManifest(m)
	if err != nil {
		return err
	}
	src.authenticated = authenticated
	// Everything from here reads checked private copies, never the folder
	// again. A fetched source is one already: FetchSource wrote it into a
	// folder this process made (0700) and checked each object's stored
	// bytes as it was fetched.
	if !src.fetched {
		if err := src.privatize(opts.CopyDir); err != nil {
			return err
		}
	}
	if src.writesBlobs() {
		if err := src.checkArchive(ctx, nil); err != nil {
			return err
		}
	}
	if opts.BinaryGooseVersion > 0 && m.GooseVersion > opts.BinaryGooseVersion {
		return fmt.Errorf("%w: schema version %d, this binary %d", ErrNewerSchema, m.GooseVersion, opts.BinaryGooseVersion)
	}
	// A manifest from before the applied list was recorded has none; the
	// restored database is checked instead (verifyRestored).
	if err := opts.checkMigrations(m.AppliedMigrations, "the backup records"); err != nil {
		return err
	}
	// FetchSource refused an encrypted backup of another key before it
	// fetched anything; this is the same rule for a source made otherwise.
	if m.Encrypted && m.KeyCheck != opts.KeyCheck {
		return fmt.Errorf("%w (backup %s, this key %s); an encrypted backup needs the key it was taken with", ErrKeyMismatch, m.KeyCheck, opts.KeyCheck)
	}
	return nil
}

// CheckManifest authenticates m with the manifest key: it refuses a
// manifest whose MAC does not verify, and one it cannot authenticate at all
// unless opts.AcceptLostCredentials. authenticated says which it was.
func (opts ValidateOptions) CheckManifest(m *Manifest) (authenticated bool, err error) {
	authenticated, err = m.checkMAC(opts.ManifestKey, opts.KeyCheck)
	switch {
	case err != nil:
		return false, err
	case !authenticated && !opts.AcceptLostCredentials:
		return false, unauthenticatedError(m, opts.KeyCheck)
	}
	return authenticated, nil
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
	if src.withoutBlobs {
		return errors.New("blobs.tar was not fetched; this restore was asked for the database only")
	}
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
	// it is configured with instead of refusing them, and the backup bucket
	// settings (backup.destination, backup.s3.*): a copy keeps the backed-up
	// server's id, so writing to that bucket would rotate away the
	// original's backups. The copy backs up to its own storage until an
	// admin names a destination.
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
	// store root, or names BlobStore. Required unless DatabaseOnly.
	BlobsInto string
	// BlobStore, when set, receives the blobs instead of a directory: a
	// bucket under a key prefix. Both stores' files land in it.
	BlobStore    blobstore.Store
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
	hasBlobs := src.Manifest.HasBlobs()
	if !opts.DatabaseOnly && hasBlobs && opts.BlobsInto == "" {
		return RestoreReport{}, errors.New("--blobs-into is required unless --database-only")
	}
	if err := src.Validate(ctx, opts.Validate); err != nil {
		return RestoreReport{}, err
	}
	if err := checkSeparateTargets(ctx, opts); err != nil {
		return RestoreReport{}, err
	}
	// Both stores land in one directory.
	if !opts.DatabaseOnly && hasBlobs {
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
	switch {
	case !hasBlobs:
		_, _ = fmt.Fprintln(out, noBlobsNote)
	case opts.DatabaseOnly:
		_, _ = fmt.Fprintln(out, "Skipped files (--database-only): uploaded artwork, avatars, and branding will be missing.")
	default:
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
// be empty, which loading checks), and the files' destination must be new
// or empty: a directory that is not one of the live server's stores, or a
// bucket key prefix that holds nothing.
func checkSeparateTargets(ctx context.Context, opts SeparateOptions) error {
	if opts.LiveURL != "" && sameDatabase(opts.Into, opts.LiveURL) {
		return errors.New("--into is this server's own database; create a new, empty database to restore into")
	}
	if opts.DatabaseOnly {
		return nil
	}
	if opts.BlobStore != nil {
		held, _, err := opts.BlobStore.List(ctx, "", "", 1)
		switch {
		case err != nil:
			return fmt.Errorf("checking --blobs-into: %w", err)
		case len(held) > 0:
			return fmt.Errorf("--blobs-into %s is not empty; restore files under a new, empty key prefix, so nothing there is overwritten", opts.BlobsInto)
		}
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

// writeSeparateBlobs writes blobs.tar into opts.BlobStore, or the directory
// opts.BlobsInto; both stores land in the same place.
func writeSeparateBlobs(ctx context.Context, src *Source, opts SeparateOptions) (int, error) {
	root := opts.BlobStore
	if root == nil {
		dir, err := blobstore.NewFilesystem(opts.BlobsInto)
		if err != nil {
			return 0, err
		}
		root = dir
	}
	return writeBlobsFromTar(ctx, src.path(BlobsName), blobStores{storeAssets: root, storeOperational: root}, nil)
}

// VerifyBackup is the restore drill: §10.2 step 1 (which reads blobs.tar
// through), step 3 into an empty scratch database, and the verification
// checks of step 4.
func VerifyBackup(ctx context.Context, src *Source, loader DatabaseLoader, scratch string, validate ValidateOptions, out io.Writer) error {
	out = writerOrDiscard(out)
	if src.withoutBlobs {
		return errors.New("a restore drill reads blobs.tar; fetch the whole backup")
	}
	if err := src.Validate(ctx, validate); err != nil {
		return err
	}
	if err := loadAndVerify(ctx, src, loader, scratch, validate, out); err != nil {
		return err
	}
	if src.Manifest.HasBlobs() {
		_, _ = fmt.Fprintf(out, "blobs.tar holds the %d files the manifest records.\n", src.Manifest.Blobs.Count)
	}
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
