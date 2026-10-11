package nfo

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

// localArtworkExtensions lists the sidecar extensions the image pipeline can
// decode, in precedence order. libvips sniffs the content rather than the
// extension, so Kodi's .tbn (a JPEG) and .gif decode like the others.
var localArtworkExtensions = []string{".jpg", ".jpeg", ".png", ".webp", ".tbn", ".gif"}

// itemArtworkNames returns an item's sidecar names for one image type in
// precedence order. Suffixes attach to the basename of one of the item's own
// media files ("Film (2020)-poster") and beat the generic directory names, as
// in Kodi. Generic names are searched only in the directory-level sidecar
// search paths, which upstream code restricts to single-content observed roots
// (canUseObservedRootForDirectorySidecars), so one folder.jpg in a flat
// multi-movie folder attaches to none of them.
//
// A series has no media file of its own: its request lists every episode file,
// so basename art such as "S01E01-poster.jpg" belongs to an episode and never
// becomes series art.
func itemArtworkNames(contentType string, imageType metadata.ImageType) (suffixes, names []string) {
	switch imageType {
	case metadata.ImagePoster:
		suffixes = []string{"-poster"}
		names = []string{"poster", "folder", "cover", "default"}
		if contentType == typeSeries {
			names = append(names, "show")
		} else {
			names = append(names, "movie")
		}
	case metadata.ImageBackdrop:
		// Silo stores one backdrop, so the first match by precedence wins.
		suffixes = []string{"-fanart"}
		names = []string{"fanart", "backdrop", "background", "art"}
	case metadata.ImageLogo:
		suffixes = []string{"-logo", "-clearlogo"}
		names = []string{"logo", "clearlogo"}
	}
	if contentType == typeSeries {
		suffixes = nil
	}
	return suffixes, names
}

// GetImages implements metadata.ImageProvider by discovering sidecar artwork
// (poster.jpg, fanart.jpg, clearlogo.png, <basename>-poster.jpg, ...) next to
// the item's media files. Returned URLs are file:// sources with a size and
// mtime fingerprint (metadata.LocalImageSourceURL); the image cache processor
// copies them into artwork storage so library files are never served
// directly. Local art applies even when no .nfo file is present.
func (p *Provider) GetImages(ctx context.Context, req metadata.ImageRequest) ([]metadata.RemoteImage, error) {
	searchDirs := compactNFOPaths(req.PrimarySidecarSearchPaths)
	var mediaFiles []string
	if req.ContentType != typeSeries {
		mediaFiles = compactNFOPaths(append([]string{req.RepresentativeFilePath}, req.AllGroupFilePaths...))
	}
	if len(searchDirs) == 0 && len(mediaFiles) == 0 {
		return nil, nil
	}

	finder := newSidecarFinder(ctx)
	var images []metadata.RemoteImage
	for _, imageType := range []metadata.ImageType{metadata.ImagePoster, metadata.ImageBackdrop, metadata.ImageLogo} {
		suffixes, names := itemArtworkNames(req.ContentType, imageType)
		if source := finder.findItemArtwork(suffixes, names, searchDirs, mediaFiles); source != "" {
			images = append(images, metadata.RemoteImage{
				ProviderID: p.Slug(),
				URL:        source,
				Type:       imageType,
				Rating:     0,
			})
		}
	}
	return images, nil
}

// findItemArtwork returns the source of the first usable sidecar: basename
// suffixes across the media files first, then generic names across the
// search directories.
func (f *sidecarFinder) findItemArtwork(suffixes, names, searchDirs, mediaFiles []string) string {
	for _, suffix := range suffixes {
		for _, mediaFile := range mediaFiles {
			if source := f.find(filepath.Dir(mediaFile), mediaBasename(mediaFile)+suffix); source != "" {
				return source
			}
		}
	}
	for _, name := range names {
		for _, dir := range searchDirs {
			if source := f.find(dir, name); source != "" {
				return source
			}
		}
	}
	return ""
}

func mediaBasename(mediaFile string) string {
	base := filepath.Base(mediaFile)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// sidecarFinder lists each directory once per request and matches sidecar
// names case-insensitively, so Poster.JPG and Folder.jpg are found on
// case-sensitive filesystems too.
type sidecarFinder struct {
	ctx context.Context
	// listings maps a directory to its entries keyed by lower-cased name. A
	// name can map to several entries that differ only in case.
	listings map[string]map[string][]string
}

func newSidecarFinder(ctx context.Context) *sidecarFinder {
	return &sidecarFinder{ctx: ctx, listings: make(map[string]map[string][]string)}
}

func (f *sidecarFinder) listing(dir string) map[string][]string {
	if listing, ok := f.listings[dir]; ok {
		return listing
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.WarnContext(f.ctx, "nfo: cannot list folder for sidecar artwork", "component", "metadata",
			"dir", dir, "error", err)
	}
	listing := make(map[string][]string, len(entries))
	for _, entry := range entries {
		lower := strings.ToLower(entry.Name())
		listing[lower] = append(listing[lower], entry.Name())
	}
	f.listings[dir] = listing
	return listing
}

// find returns the file:// source of the first usable image in dir whose name
// is name plus a supported extension, ignoring case.
func (f *sidecarFinder) find(dir, name string) string {
	listing := f.listing(dir)
	if len(listing) == 0 {
		return ""
	}
	lowerName := strings.ToLower(name)
	for _, ext := range localArtworkExtensions {
		for _, entry := range listing[lowerName+ext] {
			if source := f.usableSource(filepath.Join(dir, entry)); source != "" {
				return source
			}
		}
	}
	return ""
}

// usableSource returns the fingerprinted source for path, or "" when the file
// cannot be used. Symlinked leaves are rejected and the size is capped so the
// image-cache processor's re-checks agree with discovery. Skips an admin can
// fix (a symlink, an oversized or unreadable file) log at warn level.
func (f *sidecarFinder) usableSource(path string) string {
	info, err := os.Lstat(path)
	attrs := []any{"component", "metadata", "path", path}
	switch {
	case err != nil:
		if errors.Is(err, fs.ErrNotExist) {
			slog.DebugContext(f.ctx, "nfo: sidecar artwork disappeared during discovery", attrs...)
		} else {
			slog.WarnContext(f.ctx, "nfo: skipping unreadable sidecar artwork", append(attrs, "error", err)...)
		}
	case info.Mode()&fs.ModeSymlink != 0:
		slog.WarnContext(f.ctx, "nfo: skipping symlinked sidecar artwork; place the image file itself in the folder", attrs...)
	case !info.Mode().IsRegular():
		slog.DebugContext(f.ctx, "nfo: skipping sidecar artwork that is not a regular file", attrs...)
	case info.Size() == 0:
		slog.DebugContext(f.ctx, "nfo: skipping empty sidecar artwork", attrs...)
	case info.Size() > metadata.MaxLocalArtworkBytes:
		slog.WarnContext(f.ctx, "nfo: skipping sidecar artwork over the size limit",
			append(attrs, "size_bytes", info.Size(), "limit_bytes", metadata.MaxLocalArtworkBytes)...)
	default:
		return metadata.LocalImageSourceURL(path, info.Size(), info.ModTime())
	}
	return ""
}
