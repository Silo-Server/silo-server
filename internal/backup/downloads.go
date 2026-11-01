package backup

// Download links and verified reads of a backup's stored objects.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// ObjectLink is one downloadable object of a backup. URL is set when the
// destination presigned it; otherwise the caller issues its own capability
// for Name, which OpenObject serves.
type ObjectLink struct {
	Name      string
	Size      int64
	URL       string
	ExpiresAt time.Time
}

// downloadable lists what "Download full backup" hands out: the stored
// objects as they are, and the manifest that describes them.
func downloadable(m *Manifest) []ObjectEntry {
	out := append([]ObjectEntry(nil), m.Objects...)
	return append(out, ObjectEntry{Name: ManifestName})
}

// Download scopes.
const (
	// ScopeFull is every stored object as it is, with the manifest.
	ScopeFull = "full"
	// ScopeRestoreSet is restore-set.tar alone, decrypted: the file an
	// upload on another server takes. An encrypted one streams decrypted
	// through this server, never presigned.
	ScopeRestoreSet = "restore-set"
)

// DownloadLinks returns the objects of a complete backup in scope, presigned
// where the destination can presign.
func (s *Service) DownloadLinks(ctx context.Context, id, scope string, ttl time.Duration) ([]ObjectLink, error) {
	b, err := s.completeBackup(ctx, id)
	if err != nil {
		return nil, err
	}
	m := b.manifest
	objects := downloadable(m)
	if scope == ScopeRestoreSet {
		entry, ok := m.object(RestoreSetName)
		if !ok {
			return nil, ErrNoRestoreSet
		}
		if m.Encrypted {
			return s.decryptedRestoreSetLink(m, entry)
		}
		objects = []ObjectEntry{entry}
	}
	var links []ObjectLink
	for _, obj := range objects {
		link := ObjectLink{Name: obj.Name, Size: obj.Size}
		url, expires, ok, err := b.t.DownloadURL(ctx, objectKey(b.serverID, id, obj.Name), ttl)
		if err != nil {
			return nil, err
		}
		if ok {
			link.URL, link.ExpiresAt = url, expires
		}
		links = append(links, link)
	}
	return links, nil
}

// decryptedRestoreSetLink is the one link of an encrypted backup's restore
// set: unsigned, for the caller to sign, since this server streams it
// decrypted. The size is the stored one; the decrypted file is slightly
// smaller.
func (s *Service) decryptedRestoreSetLink(m *Manifest, entry ObjectEntry) ([]ObjectLink, error) {
	if err := s.readable(m); err != nil {
		return nil, err
	}
	return []ObjectLink{{Name: RestoreSetName, Size: entry.Size}}, nil
}

// OpenObject streams one object of a complete backup: a stored object as it
// is, or, for an encrypted backup, restore-set.tar decrypted with this
// server's key, released only once the stored bytes matched the manifest.
func (s *Service) OpenObject(ctx context.Context, id, name string) (io.ReadCloser, int64, error) {
	b, err := s.completeBackup(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	if b.manifest.Encrypted && name == RestoreSetName {
		return s.verifiedPlain(ctx, b, RestoreSetName)
	}
	for _, obj := range downloadable(b.manifest) {
		if obj.Name == name {
			return b.t.Open(ctx, objectKey(b.serverID, id, name))
		}
	}
	return nil, 0, ErrNotFound
}

// ErrStoredMismatch reports a stored object whose size or SHA-256 is not
// the manifest's: it was replaced or damaged after the backup.
var ErrStoredMismatch = errors.New("the stored file does not match its checksum in the backup's manifest")

// verifiedPlain decrypts one object of an encrypted backup into an unlinked
// temporary file while hashing the stored bytes, and returns the plaintext
// only when they match the manifest's size and SHA-256. An object
// substituted under the same key (it decrypts fine) is refused before a
// byte of it is released. The temporary file has no name once created, so
// nothing is left behind even if the process dies.
func (s *Service) verifiedPlain(ctx context.Context, b storedBackup, base string) (io.ReadCloser, int64, error) {
	want, ok := b.manifest.object(base)
	if !ok {
		return nil, 0, ErrNotFound
	}
	r, err := s.openPlain(ctx, b, base)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = r.Close() }()
	spool, err := os.CreateTemp("", "silo-"+base+"-*")
	if err != nil {
		return nil, 0, err
	}
	_ = os.Remove(spool.Name())
	fail := func(err error) (io.ReadCloser, int64, error) {
		_ = spool.Close()
		return nil, 0, err
	}
	// The plaintext of an age file is never larger than its ciphertext.
	n, err := io.Copy(spool, io.LimitReader(ctxReader{ctx: ctx, r: r}, want.Size+1))
	if err != nil {
		return fail(fmt.Errorf("decrypting %s: %w", want.Name, err))
	}
	stored, err := r.storedEntry(want.Name)
	if err != nil {
		return fail(fmt.Errorf("reading %s: %w", want.Name, err))
	}
	if n > want.Size || stored.Size != want.Size || stored.SHA256 != want.SHA256 {
		return fail(fmt.Errorf("%s: %w", want.Name, ErrStoredMismatch))
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	return spool, n, nil
}

// storedBackup is a complete backup at the current destination.
type storedBackup struct {
	t        target
	serverID string
	id       string
	manifest *Manifest
}

func (s *Service) completeBackup(ctx context.Context, id string) (storedBackup, error) {
	if _, _, ok := ParseFolderName(id); !ok {
		return storedBackup{}, ErrNotFound
	}
	t, err := s.target(ctx)
	if err != nil {
		return storedBackup{}, err
	}
	serverID, err := s.serverID(ctx)
	if err != nil {
		return storedBackup{}, err
	}
	m, err := readManifest(ctx, t, objectKey(serverID, id, ManifestName))
	if err != nil {
		return storedBackup{}, err
	}
	return storedBackup{t: t, serverID: serverID, id: id, manifest: m}, nil
}

// readable refuses an encrypted backup this server's key cannot open,
// naming both keys by their check values.
func (s *Service) readable(m *Manifest) error {
	if !m.Encrypted {
		return nil
	}
	if s.opts.ArchiveKey == nil || m.KeyCheck != s.opts.KeyCheck {
		return &WrongKeyError{Backup: m.KeyCheck, Server: s.opts.KeyCheck}
	}
	return nil
}

// openPlain opens one object of a backup by its base name, decrypted when
// the backup is encrypted. The stored bytes are hashed as they are read; the
// caller checks storedEntry against the manifest once it has read through.
func (s *Service) openPlain(ctx context.Context, b storedBackup, base string) (*storedReader, error) {
	if err := s.readable(b.manifest); err != nil {
		return nil, err
	}
	body, _, err := b.t.Open(ctx, objectKey(b.serverID, b.id, storedName(base, b.manifest.Encrypted)))
	if err != nil {
		return nil, err
	}
	var key *ArchiveKey
	if b.manifest.Encrypted {
		key = s.opts.ArchiveKey
	}
	return openStored(body, key)
}
