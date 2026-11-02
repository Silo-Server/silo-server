package backup

// Download links and verified reads of a backup's stored objects.

import (
	"context"
	"io"
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
	// ScopeRestoreSet is restore-set.tar alone: the file an upload on
	// another server takes.
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

// OpenObject streams one stored object of a complete backup as it is.
func (s *Service) OpenObject(ctx context.Context, id, name string) (io.ReadCloser, int64, error) {
	b, err := s.completeBackup(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	for _, obj := range downloadable(b.manifest) {
		if obj.Name == name {
			return b.t.Open(ctx, objectKey(b.serverID, id, name))
		}
	}
	return nil, 0, ErrNotFound
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

// openPlain opens one object of a backup. The stored bytes are hashed as
// they are read; the caller checks storedEntry against the manifest once it
// has read through.
func (s *Service) openPlain(ctx context.Context, b storedBackup, name string) (*storedReader, error) {
	body, _, err := b.t.Open(ctx, objectKey(b.serverID, b.id, name))
	if err != nil {
		return nil, err
	}
	return openStored(body), nil
}
