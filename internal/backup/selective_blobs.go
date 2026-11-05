package backup

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/Silo-Server/silo-server/internal/branding"
	"github.com/Silo-Server/silo-server/internal/imageutil"
)

// selectedBlobs is every blob the selected items of rs can write, which is
// all a restore extracts: exactly the keys the apply step reads. That is an
// item artwork's family (blobFamily), a branding asset, a library's poster,
// and a collection's or template collection's uploaded poster and backdrop
// with their families, under its own collection-images/<id>/ directory
// (copyCollectionArt). Settings, plugins, and page sections write no files.
func selectedBlobs(rs *RestoreSet, sel map[string][]string) map[string]bool {
	type pick struct{ section, id string }
	chosen := map[pick]bool{}
	for section, ids := range sel {
		for _, id := range ids {
			chosen[pick{section, id}] = true
		}
	}
	has := func(section, id string) bool { return chosen[pick{section, id}] }
	keep := map[string]bool{}
	for _, l := range rs.Libraries.Libraries {
		if l.Poster != "" && has(SectionLibraries, strconv.Itoa(l.ID)) {
			keep[l.Poster] = true
		}
	}
	for _, a := range rs.Collections.ItemArtwork {
		if has(SectionCollections, a.itemID()) {
			for _, key := range rs.blobFamily(a.Path) {
				keep[key] = true
			}
		}
	}
	for _, b := range rs.Collections.Branding {
		if has(SectionCollections, b.itemID()) {
			keep[b.Blob] = true
		}
	}
	images := func(id string, keys ...string) {
		prefix := collectionImagePrefix + id + "/"
		for _, key := range keys {
			if !strings.HasPrefix(key, prefix) {
				continue
			}
			for _, member := range rs.blobFamily(key) {
				if strings.HasPrefix(member, prefix) {
					keep[member] = true
				}
			}
		}
	}
	for _, c := range rs.Collections.Collections {
		if has(SectionCollections, collectionItemID(c.ID)) {
			images(c.ID, c.Poster, c.Backdrop)
		}
	}
	for _, t := range rs.Collections.TemplateCollections {
		if has(SectionCollections, t.itemID()) {
			images(t.ID, t.Poster, t.Backdrop)
		}
	}
	return keep
}

// collectionImagePrefix is where an admin collection's uploaded images live.
const collectionImagePrefix = "collection-images/"

// ErrNotEnoughTempSpace refuses a restore whose files do not fit in the
// temporary directory it extracts them to.
var ErrNotEnoughTempSpace = errors.New("not enough free space in the temporary directory to restore")

// maxExtractedFiles bounds the files one restore extracts: each is a file
// in the temporary directory while the restore runs.
const maxExtractedFiles = 50_000

// inodeMargin is the free inodes a restore leaves in the temporary
// directory's file system.
const inodeMargin = 10_000

// checkTempSpace requires the bytes of the blobs keep, plus freeSpaceMargin,
// free in dir, where the restore extracts them, and a free inode for each
// file plus inodeMargin; and at most maxExtractedFiles files.
func checkTempSpace(dir string, rs *RestoreSet, keep map[string]bool) error {
	if len(keep) == 0 {
		return nil
	}
	if len(keep) > maxExtractedFiles {
		return fmt.Errorf("%w: the selected items have %d files, more than the %d one restore extracts; select fewer items",
			ErrNotEnoughTempSpace, len(keep), maxExtractedFiles)
	}
	var need uint64
	for key := range keep {
		need += uint64(rs.blobSizes[key]) //nolint:gosec // G115: a tar header size the read checked is not negative
	}
	need += freeSpaceMargin
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return fmt.Errorf("reading free space in the temporary directory: %w", err)
	}
	if free := blockBytes(st.Bavail, st.Bsize); free < need {
		return fmt.Errorf("%w: %s free, %s needed (the selected items' files plus 1 GB); free space or select fewer items",
			ErrNotEnoughTempSpace, formatBytes(free), formatBytes(need))
	}
	// A file system that reports no inodes (st.Files 0) allocates them as
	// it needs them.
	if inodes := uint64(len(keep)) + inodeMargin; st.Files > 0 && st.Ffree < inodes {
		return fmt.Errorf("%w: %d files can be created there, %d needed (the selected items' files plus %d); free space or select fewer items",
			ErrNotEnoughTempSpace, st.Ffree, inodes, inodeMargin)
	}
	return nil
}

// maxRestoredFile is the largest file any upload check accepts: a login
// background (branding). A restore reads a file this size into memory to
// check it.
const maxRestoredFile = 12 << 20

// maxRestoredImage is the most an uploaded image may be: item artwork
// (metadata.MaxUploadedImageBytes), a collection image, a library poster.
const maxRestoredImage = 10 << 20

// restoredImageTypes are the types an uploaded image's bytes may be.
var restoredImageTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true}

// ErrRestoredFileRefused fails an item whose file the upload path for its
// kind would refuse.
var ErrRestoredFileRefused = errors.New("a file in the backup is not one this server accepts as an upload")

// checkRestoredBlob applies the upload path's checks to the file key a
// restore writes: a branding asset its kind's (branding.CheckStoredAsset),
// any other image the image upload limit, a JPEG, PNG, or WebP by its
// bytes, and a size libvips reads within the upload bound. It is called
// before a file reaches storage.
func checkRestoredBlob(key string, data []byte) error {
	if strings.HasPrefix(key, "branding/") {
		if err := branding.CheckStoredAsset(key, data); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrRestoredFileRefused, key, err)
		}
		return nil
	}
	if len(data) > maxRestoredImage {
		return fmt.Errorf("%w: %s is %s, over the %s an uploaded image may be", ErrRestoredFileRefused, key, formatBytes(uint64(len(data))), formatBytes(maxRestoredImage))
	}
	if sniffed := http.DetectContentType(data); !restoredImageTypes[sniffed] {
		return fmt.Errorf("%w: %s is %s, not a JPEG, PNG, or WebP image", ErrRestoredFileRefused, key, sniffed)
	}
	if err := imageutil.CheckImage(data); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrRestoredFileRefused, key, err)
	}
	return nil
}
