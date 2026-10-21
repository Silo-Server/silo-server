package nfo

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

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
		// The first match is the primary backdrop; findBackdrops lists the
		// rest after it.
		suffixes = []string{"-fanart"}
		names = []string{"fanart", "backdrop", "background", "art"}
	case metadata.ImageLogo:
		suffixes = []string{"-logo", "-clearlogo"}
		names = []string{"logo", "clearlogo"}
	case metadata.ImageBanner:
		suffixes = []string{"-banner"}
		names = []string{"banner"}
	case metadata.ImageThumb:
		suffixes = []string{"-landscape", "-thumb"}
		names = []string{"landscape", "thumb"}
	case metadata.ImageClearArt:
		// art.jpg is a backdrop, not clear art.
		suffixes = []string{"-clearart"}
		names = []string{"clearart"}
	case metadata.ImageDisc:
		suffixes = []string{"-disc", "-cdart", "-discart"}
		names = []string{"disc", "cdart", "discart"}
	}
	return suffixes, names
}

// itemImageTypes lists the sidecar image types an item can carry. Disc art
// belongs to a movie's disc, so a series has none, as in Jellyfin.
func itemImageTypes(contentType string) []metadata.ImageType {
	types := []metadata.ImageType{
		metadata.ImagePoster, metadata.ImageBackdrop, metadata.ImageLogo,
		metadata.ImageBanner, metadata.ImageThumb, metadata.ImageClearArt,
	}
	if contentType != typeSeries {
		types = append(types, metadata.ImageDisc)
	}
	return types
}

// GetImages implements metadata.ImageProvider by discovering sidecar artwork
// (poster.jpg, fanart.jpg, clearlogo.png, banner.jpg, landscape.jpg,
// clearart.png, disc.png, <basename>-poster.jpg, ...) next to the item's media
// files. It returns one image per type, except backdrops, which come in
// display order (findBackdrops). Returned URLs are file:// sources with a size and
// mtime fingerprint (metadata.LocalImageSourceURL); the image cache processor
// copies them into artwork storage so library files are never served
// directly. Local art applies even when no .nfo file is present. A slot with
// no sidecar file takes the artwork the item's NFO names, if any (see
// nfoItemArt).
func (p *Provider) GetImages(ctx context.Context, req metadata.ImageRequest) ([]metadata.RemoteImage, error) {
	searchDirs := compactNFOPaths(req.PrimarySidecarSearchPaths)
	// A series has no media file of its own: its request lists every episode
	// file, so basename art such as "S01E01-poster.jpg" belongs to an episode
	// and never becomes series art.
	groupFiles := compactNFOPaths(append([]string{req.RepresentativeFilePath}, req.AllGroupFilePaths...))
	var mediaFiles []string
	if req.ContentType != typeSeries {
		mediaFiles = groupFiles
	}
	if len(searchDirs) == 0 && len(mediaFiles) == 0 {
		return nil, nil
	}

	finder := newSidecarFinder(ctx)
	var images []metadata.RemoteImage
	for _, imageType := range itemImageTypes(req.ContentType) {
		var sources []string
		if imageType == metadata.ImageBackdrop {
			sources = finder.findBackdrops(req.ContentType, searchDirs, mediaFiles)
		} else {
			suffixes, names := itemArtworkNames(req.ContentType, imageType)
			if source := finder.findItemArtwork(suffixes, names, searchDirs, mediaFiles); source != "" {
				sources = []string{source}
			}
		}
		for _, source := range sources {
			images = append(images, metadata.RemoteImage{
				ProviderID: p.Slug(),
				URL:        source,
				Type:       imageType,
				Rating:     0,
			})
		}
	}
	return append(images, p.nfoItemArt(ctx, req, finder, groupFiles, searchDirs, images)...), nil
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
	// name can map to several entries that differ only in case. A directory
	// that exists but cannot be listed maps to nil.
	listings map[string]map[string][]string
	// realDirs maps a folder to its path with symlinks resolved, or "" when
	// it cannot be resolved (see realDir).
	realDirs map[string]string
}

func newSidecarFinder(ctx context.Context) *sidecarFinder {
	return &sidecarFinder{ctx: ctx, listings: make(map[string]map[string][]string)}
}

func (f *sidecarFinder) listing(dir string) map[string][]string {
	if listing, ok := f.listings[dir]; ok {
		return listing
	}
	listing, err := listDirectory(dir)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		listing = map[string][]string{}
	default:
		slog.DebugContext(f.ctx, "nfo: cannot list folder for sidecar artwork; looking up exact names", "component", "metadata",
			"dir", dir, "error", err)
		listing = nil
	}
	f.listings[dir] = listing
	return listing
}

// find returns the file:// source of the first usable image in dir whose name
// is name plus a supported extension, ignoring case. An entry spelled exactly
// as asked wins over other spellings of the same name.
func (f *sidecarFinder) find(dir, name string) string {
	listing := f.listing(dir)
	if listing == nil {
		// A folder that can be searched but not listed (execute-only
		// permission) still serves names looked up exactly.
		for _, ext := range localArtworkExtensions {
			path := filepath.Join(dir, name+ext)
			if _, err := os.Lstat(path); err != nil {
				continue
			}
			if source := f.usableSource(path); source != "" {
				return source
			}
		}
		return ""
	}
	for _, ext := range localArtworkExtensions {
		exact := name + ext
		spellings := listing[strings.ToLower(exact)]
		if slices.Contains(spellings, exact) {
			if source := f.usableSource(filepath.Join(dir, exact)); source != "" {
				return source
			}
		}
		for _, entry := range spellings {
			if entry == exact {
				continue
			}
			if source := f.usableSource(filepath.Join(dir, entry)); source != "" {
				return source
			}
		}
	}
	return ""
}

// Listings of large folders are shared across requests. A folder holding one
// title is cheap to list for each request, but a flat folder of many movies
// would otherwise be listed again for every movie in it. A shared listing is
// used only while the folder's mtime is unchanged (adding, removing or
// renaming an entry changes it) and for at most sharedListingTTL. A folder
// changed within sharedListingSettle is not shared: on a filesystem with
// coarse timestamps, an entry added in the same tick as the listing would
// leave the mtime unchanged.
const (
	sharedListingMinEntries = 256
	sharedListingMaxDirs    = 8
	sharedListingTTL        = time.Minute
	sharedListingSettle     = 5 * time.Second
)

type sharedListing struct {
	modTime time.Time
	expires time.Time
	names   map[string][]string
}

var sharedListings = struct {
	sync.Mutex
	dirs map[string]sharedListing
}{dirs: make(map[string]sharedListing)}

// listDirectory returns dir's entries keyed by lower-cased name, each key's
// entries sorted so the choice among case variants is stable.
func listDirectory(dir string) (map[string][]string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	sharedListings.Lock()
	cached, ok := sharedListings.dirs[dir]
	sharedListings.Unlock()
	if ok && cached.modTime.Equal(info.ModTime()) && now.Before(cached.expires) {
		return cached.names, nil
	}

	d, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	entries, err := d.Readdirnames(-1)
	_ = d.Close()
	if err != nil {
		return nil, err
	}
	names := make(map[string][]string, len(entries))
	for _, entry := range entries {
		lower := strings.ToLower(entry)
		names[lower] = append(names[lower], entry)
	}
	for _, spellings := range names {
		if len(spellings) > 1 {
			slices.Sort(spellings)
		}
	}
	if len(entries) >= sharedListingMinEntries && now.Sub(info.ModTime()) > sharedListingSettle {
		sharedListings.Lock()
		if _, ok := sharedListings.dirs[dir]; !ok && len(sharedListings.dirs) >= sharedListingMaxDirs {
			for key := range sharedListings.dirs {
				delete(sharedListings.dirs, key)
				break
			}
		}
		sharedListings.dirs[dir] = sharedListing{modTime: info.ModTime(), expires: now.Add(sharedListingTTL), names: names}
		sharedListings.Unlock()
	}
	return names, nil
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
	case info.Size() > metadata.MaxArtworkSourceBytes:
		slog.WarnContext(f.ctx, "nfo: skipping sidecar artwork over the size limit",
			append(attrs, "size_bytes", info.Size(), "limit_bytes", metadata.MaxArtworkSourceBytes)...)
	default:
		return metadata.LocalImageSourceURL(path, info.Size(), info.ModTime())
	}
	return ""
}
