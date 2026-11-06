package backup

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Silo-Server/silo-server/internal/backup/jsonsize"
	"github.com/Silo-Server/silo-server/internal/backup/userdata"
)

// A server's account documents together can be far larger than what a read
// holds in memory (maxRestoreSetMetadata): one per account, each up to
// maxRestoreSetDocument of watch history. The read writes each to the set's
// directory as it arrives, checked against its digest, and keeps only the
// account's entry (UserRef). The preview and a restore load one document at
// a time (loadUser), so memory follows the largest account, not their sum.

// UserRef is one account of a restore set's users section: the identity
// matching reads (id, username, and email; the other fields are empty) and
// the file its validated document was written to.
type UserRef struct {
	Account AccountEntry
	file    string
	sha256  string
}

// tempStorageError is a failure of the temporary directory a read writes
// to, not of the archive: openRestoreSet passes it on as the server's own
// error rather than calling the restore set unreadable.
type tempStorageError struct{ err error }

func (e *tempStorageError) Error() string { return "temporary directory: " + e.err.Error() }
func (e *tempStorageError) Unwrap() error { return e.err }

// storageWriter marks the errors of writes to the temporary directory.
type storageWriter struct{ w io.Writer }

func (s storageWriter) Write(b []byte) (int, error) {
	n, err := s.w.Write(b)
	if err != nil {
		err = &tempStorageError{err}
	}
	return n, err
}

// isUserDocument reports whether an archive path is an account document.
func isUserDocument(name string) bool {
	return strings.HasPrefix(name, restoreSetUsersDir) && strings.HasSuffix(name, ".json")
}

// dir returns the set's directory, creating it under os.TempDir() for a read
// that extracts nothing.
func (p *restoreSetParser) dir() (string, error) {
	if p.rs.dir == "" {
		if err := p.rs.makeDir(os.TempDir()); err != nil {
			return "", err
		}
	}
	return p.rs.dir, nil
}

// A set's directory outlives a process that stops without Cleanup, and a
// preview's holds every account document of a backup. Each directory holds
// an exclusive flock on its owner file for as long as its set is in use; the
// kernel drops it when the process exits, so a directory whose lock is free
// has no owner, whichever process on the host made it.
const (
	setDirPrefix = "restore-set-"
	setDirOwner  = ".owner"
	// unlockedSetDirAge is how old a directory without an owner file must
	// be before the sweep removes it: one is unlocked only between its
	// creation and its lock.
	unlockedSetDirAge = time.Hour
)

// makeDir creates the set's directory in parent and locks it. The owner
// file is locked under another name and renamed into place, so the sweep
// never finds an owner file it can lock while the directory is in use: the
// lock belongs to the file and moves with it.
func (rs *RestoreSet) makeDir(parent string) error {
	dir, err := os.MkdirTemp(parent, setDirPrefix)
	if err != nil {
		return &tempStorageError{err}
	}
	owner, err := lockedOwner(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return &tempStorageError{fmt.Errorf("locking the restore set's directory: %w", err)}
	}
	rs.dir, rs.owner = dir, owner
	return nil
}

func lockedOwner(dir string) (*os.File, error) {
	owner, err := os.CreateTemp(dir, setDirOwner+"-")
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(owner.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = owner.Close()
		return nil, err
	}
	if err := os.Rename(owner.Name(), filepath.Join(dir, setDirOwner)); err != nil {
		_ = owner.Close()
		return nil, err
	}
	return owner, nil
}

var sweepSetDirsOnce sync.Once

// sweepOrphanedSetDirs removes, once per process, the set directories under
// parent that no process holds: what a server that stopped mid-preview or
// mid-restore left behind.
func sweepOrphanedSetDirs(parent string) {
	sweepSetDirsOnce.Do(func() {
		dirs, err := filepath.Glob(filepath.Join(parent, setDirPrefix+"*"))
		if err != nil {
			return
		}
		for _, dir := range dirs {
			if orphanedSetDir(dir) {
				if err := os.RemoveAll(dir); err != nil {
					slog.Warn("removing a restore set left by a stopped server failed", "component", "backup", "dir", dir, "error", err)
				}
			}
		}
	})
}

func orphanedSetDir(dir string) bool {
	owner, err := os.Open(filepath.Join(dir, setDirOwner)) //nolint:gosec // G304: a path under the temporary directory this sweep listed
	if err != nil {
		info, statErr := os.Stat(dir)
		return errors.Is(err, os.ErrNotExist) && statErr == nil && info.IsDir() && time.Since(info.ModTime()) > unlockedSetDirAge
	}
	defer func() { _ = owner.Close() }()
	// The probe's own lock goes with Close.
	return unix.Flock(int(owner.Fd()), unix.LOCK_EX|unix.LOCK_NB) == nil
}

// userDocument writes an account document to the set's directory, checking
// its digest. It is decoded only once the whole archive is verified
// (decodeUsers).
func (p *restoreSetParser) userDocument(h *tar.Header, tr io.Reader, want FileDigest) error {
	dir, err := p.dir()
	if err != nil {
		return err
	}
	file, err := p.copyEntry(dir, "user", h, tr, want.SHA256)
	if err != nil {
		return err
	}
	if p.userFiles == nil {
		p.userFiles = map[string]string{}
	}
	p.userFiles[h.Name] = file
	p.rs.userBytes += h.Size
	return nil
}

// checkSpillSpace requires need bytes plus freeSpaceMargin free in dir.
func checkSpillSpace(dir string, need int64) error {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return &tempStorageError{fmt.Errorf("reading its free space: %w", err)}
	}
	free := blockBytes(st.Bavail, st.Bsize)
	if want := uint64(need) + freeSpaceMargin; free < want { //nolint:gosec // G115: sizes the index check made non-negative
		return fmt.Errorf("%w: the backup's accounts need %s in %s and %s is free (with 1 GB to spare); free space there or set TMPDIR to a larger disk",
			ErrNotEnoughTempSpace, formatBytes(want), dir, formatBytes(free))
	}
	return nil
}

// decodeUserFile reads and decodes one written account document, bounded on
// its own (decodeLimits), after checking it is still the bytes the archive
// held.
func decodeUserFile(file, wantSHA string) (*UserDocument, error) {
	data, err := os.ReadFile(file) //nolint:gosec // G304: a file this read created
	if err != nil {
		return nil, &tempStorageError{err}
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != wantSHA {
		return nil, fmt.Errorf("%s no longer matches its digest", file)
	}
	var doc UserDocument
	if _, err := jsonsize.Check(data, &doc, decodeLimits(len(data))); err != nil {
		return nil, err
	}
	if err := decodeStrict(data, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

// checkUserDocument bounds and validates a decoded account document, locking
// credentials a restore must not carry over, and returns the blob keys it
// references. The read and every load run it, so a loaded document is the
// one the read accepted.
func checkUserDocument(doc *UserDocument) ([]string, error) {
	// Bounds first: validation may clear what it locks.
	if err := userdata.CheckBounds(doc); err != nil {
		return nil, err
	}
	return validateUserDocument(doc)
}

// loadUser reads one account's document. Each call decodes a fresh copy the
// caller may change.
func (rs *RestoreSet) loadUser(ref UserRef) (*UserDocument, error) {
	doc, err := decodeUserFile(ref.file, ref.sha256)
	if err == nil {
		_, err = checkUserDocument(doc)
	}
	if err != nil {
		return nil, fmt.Errorf("reading account %d from the restore set: %w", ref.Account.ID, err)
	}
	return doc, nil
}

// user returns the account with backup id, or false.
func (rs *RestoreSet) user(id string) (UserRef, bool) {
	for _, ref := range rs.Users {
		if strconv.Itoa(ref.Account.ID) == id {
			return ref, true
		}
	}
	return UserRef{}, false
}
