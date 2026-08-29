package artworkstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"
)

const (
	// markerFileName holds the store marker at the logical root. It is
	// dot-prefixed, so ValidateKey rejects it and no object key can ever
	// address, overwrite, or delete it.
	markerFileName = ".silo-artwork-store"

	// markerFormatVersion versions the marker document, not the storage
	// format. Bumping it is how a future reader recognizes an incompatible
	// marker instead of misreading one.
	markerFormatVersion = 1

	// markerIDBytes is the entropy behind a marker id.
	markerIDBytes = 16

	// maxMarkerFileBytes bounds how much of the marker file is read. The
	// document is well under a hundred bytes; anything larger is corruption.
	maxMarkerFileBytes = 64 << 10
)

// ErrNoMarker reports that the store root carries no marker yet. A store that
// has never been initialized returns this; an initialized store whose marker
// vanished also returns it, and that is precisely the condition the caller must
// treat as "this node is looking at a different disk".
var ErrNoMarker = errors.New("artworkstore: store marker is absent")

// Marker identifies one physical copy of a filesystem artwork store. The
// database records the marker id of the store the cluster is bound to, and
// every API node verifies at startup that the marker under its configured root
// still matches. A missing or different marker means the node sees a different
// disk than the recorded store.
//
// The marker identifies a copy, not an installation: copying the tree copies
// the marker, and a storage-only import simply re-binds it. It never appears in
// an object key or a portable manifest, contains no secret, and is not used for
// authentication.
type Marker struct {
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

// ReadMarker returns the marker at the store root, or ErrNoMarker when the
// store has none.
func (s *FilesystemStore) ReadMarker(ctx context.Context) (Marker, error) {
	if err := ctx.Err(); err != nil {
		return Marker{}, err
	}
	root, release, err := s.openRoot()
	if err != nil {
		return Marker{}, err
	}
	defer release()
	return readMarker(root)
}

// EnsureMarker returns the store's marker, creating a random one when the root
// carries none. The bool reports whether this call created it, which is what
// lets the caller distinguish "new store, safe to pin" from "existing store,
// must match the pinned generation". Creation is create-only, so concurrent
// nodes racing on a fresh shared mount converge on one marker instead of
// overwriting each other.
func (s *FilesystemStore) EnsureMarker(ctx context.Context) (Marker, bool, error) {
	if err := ctx.Err(); err != nil {
		return Marker{}, false, err
	}
	root, release, err := s.openRoot()
	if err != nil {
		return Marker{}, false, err
	}
	defer release()
	switch marker, err := readMarker(root); {
	case err == nil:
		return marker, false, nil
	case !errors.Is(err, ErrNoMarker):
		return Marker{}, false, err
	}

	id, err := newMarkerID()
	if err != nil {
		return Marker{}, false, err
	}
	marker := Marker{
		Version:   markerFormatVersion,
		ID:        id,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	encoded, err := json.Marshal(marker)
	if err != nil {
		return Marker{}, false, fmt.Errorf("artworkstore: encoding store marker: %w", err)
	}
	encoded = append(encoded, '\n')

	tempName, err := writeTempFile(root, ".", encoded)
	if err != nil {
		return Marker{}, false, err
	}
	defer func() { _ = root.Remove(tempName) }()

	linkErr := root.Link(tempName, markerFileName)
	if linkErr != nil && !errors.Is(linkErr, fs.ErrExist) && isLinkUnsupported(linkErr) {
		// Without hard links the publish must still be create-only: a rename
		// would silently replace a marker a racing node published a moment
		// earlier, leaving the loser holding an id that is no longer on disk
		// and pinning a generation no store carries. O_EXCL keeps exactly one
		// winner; the cost is that a crash mid-write on this narrow first-boot
		// path leaves a truncated marker, which reads back as a hard error an
		// operator can resolve by deleting the file.
		linkErr = writeMarkerExclusive(root, encoded)
	}
	if linkErr != nil {
		if errors.Is(linkErr, fs.ErrExist) {
			// Another node created the marker first; its value wins.
			existing, err := readMarker(root)
			if err != nil {
				return Marker{}, false, err
			}
			return existing, false, nil
		}
		return Marker{}, false, fmt.Errorf("artworkstore: writing store marker in %s: %w", s.rootPath, linkErr)
	}
	syncDir(root, ".")
	return marker, true, nil
}

// writeMarkerExclusive publishes the marker with an atomic create-only open,
// for filesystems where the hard-link publish path is unavailable. A loser of
// the creation race receives fs.ErrExist, exactly like the link path, and
// adopts the winner's marker.
func writeMarkerExclusive(root *os.Root, encoded []byte) error {
	file, err := root.OpenFile(markerFileName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, storeFilePerm)
	if err != nil {
		return err
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		_ = root.Remove(markerFileName)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = root.Remove(markerFileName)
		return err
	}
	if err := file.Close(); err != nil {
		_ = root.Remove(markerFileName)
		return err
	}
	return nil
}

func readMarker(root *os.Root) (Marker, error) {
	file, _, err := openRegular(root, markerFileName)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Marker{}, ErrNoMarker
		}
		return Marker{}, err
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, maxMarkerFileBytes+1))
	if err != nil {
		return Marker{}, fmt.Errorf("artworkstore: reading store marker: %w", err)
	}
	if len(data) > maxMarkerFileBytes {
		return Marker{}, errors.New("artworkstore: store marker is implausibly large")
	}
	var marker Marker
	if err := json.Unmarshal(data, &marker); err != nil {
		return Marker{}, fmt.Errorf("artworkstore: decoding store marker: %w", err)
	}
	if marker.Version != markerFormatVersion {
		return Marker{}, fmt.Errorf("artworkstore: unsupported store marker version %d", marker.Version)
	}
	if !validMarkerID(marker.ID) {
		return Marker{}, errors.New("artworkstore: store marker id is malformed")
	}
	return marker, nil
}

func newMarkerID() (string, error) {
	buf := make([]byte, markerIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("artworkstore: generating a store marker id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func validMarkerID(id string) bool {
	if len(id) != markerIDBytes*2 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
