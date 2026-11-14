package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

// DecryptObject writes the plaintext of the stored object name to w. With a
// manifest in the same folder it refuses a key other than the backup's
// before reading anything, and checks the stored bytes against the
// manifest's size and SHA-256 once they are read. The plaintext written
// before a failed check is not trustworthy; DecryptObjectToFile keeps none.
func DecryptObject(ctx context.Context, objects Objects, name string, key *ArchiveKey, w io.Writer) error {
	want, err := manifestEntry(ctx, objects, name, key)
	if err != nil {
		return err
	}
	body, err := objects.Open(ctx, name)
	if err != nil {
		return fmt.Errorf("opening %s: %w", name, err)
	}
	plain, err := openStored(body, key)
	if errors.Is(err, ErrWrongKey) {
		return fmt.Errorf("%w (this key %s)", ErrWrongKey, key.Check)
	}
	if err != nil {
		return err
	}
	defer func() { _ = plain.Close() }()
	if _, err := io.Copy(w, ctxReader{ctx: ctx, r: plain}); err != nil {
		return fmt.Errorf("decrypting %s: %w", name, err)
	}
	stored, err := plain.storedEntry(name)
	if err != nil {
		return fmt.Errorf("reading %s: %w", name, err)
	}
	if want != nil && (stored.Size != want.Size || stored.SHA256 != want.SHA256) {
		return fmt.Errorf("%s does not match its checksum in the manifest", name)
	}
	return nil
}

// DecryptObjectToFile is DecryptObject into a new file at path. It writes
// path.partial and renames it only once every check passed, so a failed
// decrypt leaves nothing behind.
func DecryptObjectToFile(ctx context.Context, objects Objects, name string, key *ArchiveKey, path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	partial := path + ".partial"
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // G304: the output the operator named
	if err != nil {
		return err
	}
	err = DecryptObject(ctx, objects, name, key, f)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(partial, path)
	}
	if err != nil {
		_ = os.Remove(partial)
	}
	return err
}

// manifestEntry reads the manifest beside an object, when there is one, and
// returns the object's entry: nil when the folder has no manifest or the
// manifest does not list the object. A manifest that names another key
// refuses the decrypt, and so does one of this key whose MAC does not
// verify: its checksums could be anyone's.
func manifestEntry(ctx context.Context, objects Objects, name string, key *ArchiveKey) (*ObjectEntry, error) {
	m, err := ReadSourceManifest(ctx, objects, "")
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the manifest beside %s: %w", name, err)
	}
	if !m.Encrypted {
		return nil, fmt.Errorf("backup %s is not encrypted; its files can be used as they are", m.ID)
	}
	if err := CheckKey(m, key); err != nil {
		return nil, err
	}
	if authenticated, err := m.checkMAC(key.manifest, key.Check); err != nil {
		return nil, err
	} else if !authenticated {
		return nil, fmt.Errorf("%w: the manifest beside %s cannot be verified with this key", ErrManifestTampered, name)
	}
	for _, o := range m.Objects {
		if o.Name == name {
			return &o, nil
		}
	}
	return nil, nil
}
