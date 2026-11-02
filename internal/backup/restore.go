package backup

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/blobstore"
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
	for _, name := range []string{DumpName, BlobsName} {
		entry, ok := m.object(name)
		if !ok {
			return fmt.Errorf("the manifest lists no %s", name)
		}
		if err := checkFile(src.path(name), entry); err != nil {
			return err
		}
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
	if m.KeyCheck != "" && m.KeyCheck != opts.KeyCheck {
		if !opts.AcceptLostCredentials {
			return fmt.Errorf("%w (backup %s, this key %s); encrypted settings and credentials in it cannot be read with this key. Pass --accept-lost-credentials to restore anyway", ErrKeyMismatch, m.KeyCheck, opts.KeyCheck)
		}
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

func checkFile(path string, want ObjectEntry) error {
	f, err := os.Open(path) //nolint:gosec // G304: an object of the backup folder the operator named
	if err != nil {
		return fmt.Errorf("opening %s: %w", filepath.Base(path), err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return fmt.Errorf("reading %s: %w", filepath.Base(path), err)
	}
	if n != want.Size || hex.EncodeToString(h.Sum(nil)) != want.SHA256 {
		return fmt.Errorf("%s does not match its checksum in the manifest", want.Name)
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
	// Into is the pre-created empty database to restore into.
	Into string
	// BlobsInto is a directory that receives the blobs, laid out as a local
	// store root. Required unless DatabaseOnly.
	BlobsInto    string
	DatabaseOnly bool
	Out          io.Writer
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
	validate.AcceptLostCredentials = true
	if err := src.Validate(ctx, validate); err != nil {
		return err
	}
	if src.Manifest.KeyCheck != "" && src.Manifest.KeyCheck != validate.KeyCheck {
		_, _ = fmt.Fprintln(out, "Warning: this backup was taken with a different SECRET_KEY; its encrypted settings and credentials cannot be read with this one.")
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
	tr := tar.NewReader(f)
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
