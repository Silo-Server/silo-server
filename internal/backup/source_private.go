package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// consumedObjects are the objects a full restore or a verify of src reads
// after it validates them: blobs.tar only when the backup has one (a
// before-upgrade backup is the database alone).
func (src *Source) consumedObjects() []string {
	if !src.Manifest.HasBlobs() {
		return []string{DumpName}
	}
	return []string{DumpName, BlobsName}
}

// privatize copies the objects a restore consumes into a new directory only
// this process can write (0700, each file 0600), hashing each copy as it is
// written, and compares the copies with the manifest's checksums. Every
// later read goes to the copies: a path someone else can write could be
// swapped between its check and pg_restore opening it. It first requires
// the objects' size plus freeSpaceMargin free where the copies go (under
// dir, os.TempDir when empty). Close removes the copies; a failure removes
// them before it returns.
func (src *Source) privatize(dir string) (err error) {
	if src.private != "" {
		return nil
	}
	if dir == "" {
		dir = os.TempDir()
	}
	var need uint64
	for _, name := range src.consumedObjects() {
		entry, ok := src.Manifest.object(name)
		if !ok {
			return fmt.Errorf("the manifest lists no %s", name)
		}
		need += uint64(max(entry.Size, 0))
	}
	if err := checkCopySpace(dir, need+freeSpaceMargin); err != nil {
		return err
	}
	private, err := os.MkdirTemp(dir, "silo-restore-source-")
	if err != nil {
		return fmt.Errorf("creating a private copy of the backup: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(private)
		}
	}()
	for _, name := range src.consumedObjects() {
		entry, _ := src.Manifest.object(name)
		if err := copyChecked(filepath.Join(src.Dir, name), filepath.Join(private, name), entry); err != nil {
			return err
		}
	}
	src.origin, src.Dir, src.private = src.Dir, private, private
	return nil
}

// Folder is the backup folder the source was opened from, whatever Dir
// reads now.
func (src *Source) Folder() string {
	if src.private != "" {
		return src.origin
	}
	return src.Dir
}

// Close removes the private copies privatize made, if any.
func (src *Source) Close() error {
	if src == nil || src.private == "" {
		return nil
	}
	dir := src.private
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("removing %s: %w", dir, err)
	}
	src.Dir, src.private = src.origin, ""
	return nil
}

// copyChecked copies from to a new 0600 file to, hashing what it writes,
// and requires the copy to match want.
func copyChecked(from, to string, want ObjectEntry) error {
	in, err := os.Open(from) //nolint:gosec // G304: an object of the backup folder the operator named
	if err != nil {
		return fmt.Errorf("opening %s: %w", want.Name, err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: a file in the private directory this process made
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, want.Size+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("copying %s: %w", want.Name, err)
	}
	if n != want.Size || hex.EncodeToString(h.Sum(nil)) != want.SHA256 {
		return fmt.Errorf("%s does not match its checksum in the manifest", want.Name)
	}
	return nil
}

// ErrNoCopySpace refuses a restore whose private copies would not fit.
var ErrNoCopySpace = errors.New("not enough free space for a private copy of the backup")

// checkCopySpace requires need bytes free in dir.
func checkCopySpace(dir string, need uint64) error {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return fmt.Errorf("reading free space in %s: %w", dir, err)
	}
	if free := blockBytes(st.Bavail, st.Bsize); free < need {
		return fmt.Errorf("%w in %s: %s free, %s needed (the backup's database dump and files plus 1 GB); free space there or set TMPDIR to a larger disk",
			ErrNoCopySpace, dir, formatBytes(free), formatBytes(need))
	}
	return nil
}
