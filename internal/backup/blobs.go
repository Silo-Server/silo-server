package backup

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/blobstore"
)

// blobStores are the stores a backup reads blobs from and a restore writes
// them to, by their name in blobs.tar.
type blobStores map[string]blobstore.Store

// blobNamespaces are the key prefixes a backup may carry for each store: the
// irreplaceable blobs of docs/architecture/backups.md, and nothing else. A
// restore refuses any other key, so an archive can never write over
// backups/, the restore journal, or anything a backup does not own.
var blobNamespaces = map[string][]string{
	storeAssets: {
		artworkkey.LocalPrefix, // uploaded item artwork is cached as library artwork
		"collection-images/", "user-collection-images/", "library-posters/", "branding/", "subtitles/",
	},
	storeOperational: {"profile-avatars/"},
}

// allowedBlobKey reports whether key is a valid key inside store's backup
// namespaces.
func allowedBlobKey(store, key string) bool {
	if blobstore.ValidateKey(key) != nil || strings.HasPrefix(key, RootPrefix) {
		return false
	}
	for _, prefix := range blobNamespaces[store] {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// blobCopyResult is what writing blobs.tar found.
type blobCopyResult struct {
	count   int
	bytes   int64
	missing []MissingBlob
}

// writeBlobTar copies refs into a tar on w. Blobs are copied after the
// snapshot's enumeration, so a key replaced in place may carry newer bytes;
// a key deleted meanwhile is recorded as missing rather than failing the
// backup. Any other read error fails it.
func writeBlobTar(ctx context.Context, w io.Writer, stores blobStores, refs []blobRef) (blobCopyResult, error) {
	var result blobCopyResult
	tw := tar.NewWriter(w)
	written := make(map[string]bool)
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		store := stores[ref.store]
		if store == nil {
			result.missing = append(result.missing, MissingBlob{Store: ref.store, Key: ref.key, Source: ref.source})
			continue
		}
		if !allowedBlobKey(ref.store, ref.key) {
			// Provider artwork a collection borrows, for example: it can be
			// fetched again, so it is not a backup's to carry.
			continue
		}
		found, err := copyBlobAs(ctx, tw, store, ref.store+"/"+ref.key, ref.key, written, &result)
		if err != nil {
			return result, err
		}
		if !found {
			result.missing = append(result.missing, MissingBlob{Store: ref.store, Key: ref.key, Source: ref.source})
			continue
		}
		if !ref.ladder {
			continue
		}
		siblings, err := ladderSiblings(ctx, store, ref.key)
		if err != nil {
			return result, err
		}
		for _, key := range siblings {
			if _, err := copyBlobAs(ctx, tw, store, ref.store+"/"+key, key, written, &result); err != nil {
				return result, err
			}
		}
	}
	return result, tw.Close()
}

// copyBlobAs writes one object as the tar entry name. found is false when
// the object no longer exists.
func copyBlobAs(ctx context.Context, tw *tar.Writer, store blobstore.Store, name, key string, written map[string]bool, result *blobCopyResult) (bool, error) {
	if written[name] {
		return true, nil
	}
	body, info, err := store.Get(ctx, key)
	if errors.Is(err, blobstore.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading blob %s: %w", name, err)
	}
	defer func() { _ = body.Close() }()
	header := &tar.Header{Name: name, Mode: 0o644, Size: info.Size, ModTime: info.ModTime, Typeflag: tar.TypeReg, Format: tar.FormatPAX}
	if err := tw.WriteHeader(header); err != nil {
		return false, err
	}
	n, err := io.Copy(tw, body)
	if err != nil {
		return false, fmt.Errorf("copying blob %s: %w", name, err)
	}
	if n != info.Size {
		return false, fmt.Errorf("copying blob %s: read %d of %d bytes", name, n, info.Size)
	}
	written[name] = true
	result.count++
	result.bytes += n
	return true, nil
}

// ladderSiblings lists the variants stored beside an artwork original: the
// keys in its image-type directory that belong to the same revision (or, for
// a legacy unrevisioned original, the other unrevisioned keys). A key that is
// not an original has no ladder.
func ladderSiblings(ctx context.Context, store blobstore.Store, original string) ([]string, error) {
	if !strings.HasPrefix(path.Base(original), artworkkey.OriginalVariant+".") {
		return nil, nil
	}
	dir := artworkkey.Directory(original)
	if dir == "" {
		return nil, nil
	}
	objects, _, err := store.List(ctx, dir, "", 0)
	if err != nil {
		return nil, fmt.Errorf("listing variants of %s: %w", original, err)
	}
	revision := artworkkey.Revision(original)
	var keys []string
	for _, obj := range objects {
		if obj.Key == original || path.Dir(obj.Key)+"/" != dir {
			continue
		}
		sameRevision := revision != "" && artworkkey.OriginalOf(obj.Key) == original
		bothLegacy := revision == "" && artworkkey.Revision(obj.Key) == ""
		if sameRevision || bothLegacy {
			keys = append(keys, obj.Key)
		}
	}
	return keys, nil
}

// blobEntry is one parsed blobs.tar entry.
type blobEntry struct {
	store string
	key   string
}

// parseBlobEntry validates a blobs.tar header. Only regular files under a
// known store with a valid key are accepted.
func parseBlobEntry(h *tar.Header) (blobEntry, error) {
	if h.Typeflag != tar.TypeReg {
		return blobEntry{}, fmt.Errorf("blobs.tar: %q is not a regular file", h.Name)
	}
	store, key, ok := strings.Cut(h.Name, "/")
	if !ok || (store != storeAssets && store != storeOperational) {
		return blobEntry{}, fmt.Errorf("blobs.tar: %q is outside a known store", h.Name)
	}
	if !allowedBlobKey(store, key) {
		return blobEntry{}, fmt.Errorf("blobs.tar: %q is not a key a backup carries", h.Name)
	}
	if h.Size < 0 {
		return blobEntry{}, fmt.Errorf("blobs.tar: %q has a negative size", h.Name)
	}
	return blobEntry{store: store, key: key}, nil
}

// validateBlobArchive reads blobs.tar through before anything changes: every
// entry must be a regular file in its store's backup namespaces, and no two
// entries may land on the same object. physical names where a store's
// objects live, so two store names sharing one root count as one place, and
// reports false for a store the target does not have; nil compares by store
// name.
func validateBlobArchive(ctx context.Context, path string, physical func(store string) (string, bool)) (int, error) {
	f, err := os.Open(path) //nolint:gosec // G304: an object of the backup folder the operator named
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	if physical == nil {
		physical = func(store string) (string, bool) { return store, true }
	}
	seen := map[string]string{}
	tr := tar.NewReader(f)
	for count := 0; ; count++ {
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
		where, ok := physical(entry.store)
		if !ok {
			return count, fmt.Errorf("blobs.tar holds %s files, and the target has no %s store", entry.store, entry.store)
		}
		destination := where + "\x00" + entry.key
		if first, dup := seen[destination]; dup {
			return count, fmt.Errorf("blobs.tar: %q and %q write the same object", first, h.Name)
		}
		seen[destination] = h.Name
		if _, err := io.CopyN(io.Discard, tr, h.Size); err != nil {
			return count, fmt.Errorf("reading blobs.tar: %w", err)
		}
	}
}
