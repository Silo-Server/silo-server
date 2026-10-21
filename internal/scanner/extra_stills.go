package scanner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/imageutil"
)

// extraStillCacher stores an extra's card image in the image cache.
type extraStillCacher interface {
	CacheExtraStill(ctx context.Context, data []byte, extraID string) (storedPath string, thumbhash string, err error)
}

// extraStillSuffixes and extraStillExtensions name an extra's sidecar image
// in precedence order: "<name>-thumb.<ext>", then "<name>.<ext>", the rule
// episode stills follow (internal/metadata/nfo findEpisodeSidecars) and the
// one Jellyfin and Kodi use for a video's thumbnail.
var (
	extraStillSuffixes   = []string{"-thumb", ""}
	extraStillExtensions = strings.Fields(".jpg .jpeg .png .webp .tbn .gif")
)

// maxExtraStillBytes caps a sidecar read, matching
// metadata.MaxArtworkSourceBytes for other sidecar artwork.
const maxExtraStillBytes = 25 << 20

// extraStillSourceScheme prefixes still_source for a sidecar copy.
const extraStillSourceScheme = "file://"

// errUndecodableExtraStill marks a sidecar whose bytes are not a readable
// image; it is skipped until it changes.
var errUndecodableExtraStill = errors.New("extra image cannot be decoded")

// extraStillDirs finds extras' sidecar images. It uses the directory listings
// the scan walk already read (SidecarListing, which covers every image
// extension) and reads any other directory once per scan, so extras sharing a
// folder cost one listing between them.
type extraStillDirs struct {
	walked SidecarListing
	// names maps a directory to its image names, lowercased name to the
	// spellings on disk, or to nil when it could not be read.
	names map[string]map[string][]string
}

func newExtraStillDirs(walked SidecarListing) *extraStillDirs {
	return &extraStillDirs{walked: walked, names: make(map[string]map[string][]string)}
}

// imageNames returns dir's image names, and false when dir cannot be read.
func (d *extraStillDirs) imageNames(dir string) (map[string][]string, bool) {
	if names, ok := d.names[dir]; ok {
		return names, names != nil
	}
	listed, ok := d.walked[dir]
	if !ok {
		entries, err := os.ReadDir(dir)
		if err != nil {
			d.names[dir] = nil
			return nil, false
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				listed = append(listed, entry.Name())
			}
		}
	}
	names := make(map[string][]string)
	for _, name := range listed {
		if slices.Contains(extraStillExtensions, strings.ToLower(filepath.Ext(name))) {
			lower := strings.ToLower(name)
			names[lower] = append(names[lower], name)
		}
	}
	d.names[dir] = names
	return names, true
}

// find returns the sidecar image for the extra at videoPath, ignoring case
// and preferring an exact spelling, or "" when it has none. ok is false when
// the folder or one of its images could not be read, so whether the extra
// has an image is unknown.
func (d *extraStillDirs) find(videoPath string) (path string, info fs.FileInfo, ok bool) {
	dir := filepath.Dir(videoPath)
	names, ok := d.imageNames(dir)
	if !ok {
		return "", nil, false
	}
	base := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	for _, suffix := range extraStillSuffixes {
		for _, ext := range extraStillExtensions {
			exact := base + suffix + ext
			for _, name := range exactFirst(names[strings.ToLower(exact)], exact) {
				candidate := filepath.Join(dir, name)
				info, checked := usableExtraStill(candidate)
				if !checked {
					return "", nil, false
				}
				if info != nil {
					return candidate, info, true
				}
			}
		}
	}
	return "", nil, true
}

// exactFirst orders spellings so that exact, when present, comes first.
func exactFirst(spellings []string, exact string) []string {
	i := slices.Index(spellings, exact)
	if i <= 0 {
		return spellings
	}
	return slices.Concat([]string{exact}, spellings[:i], spellings[i+1:])
}

// usableExtraStill returns path's info when it can serve as an extra's
// image, or nil when it cannot: symlinks, empty files and files over
// maxExtraStillBytes are skipped, as for other sidecar artwork. checked is
// false when path could not be inspected.
func usableExtraStill(path string) (info fs.FileInfo, checked bool) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, errors.Is(err, fs.ErrNotExist)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxExtraStillBytes {
		return nil, true
	}
	return info, true
}

// extraStillSource identifies a sidecar by path, size and modification time
// in whole seconds, the format of metadata.LocalImageSourceURL, so replacing
// the image under the same name changes it.
func extraStillSource(path string, info fs.FileInfo) string {
	return extraStillSourceScheme + path + "?v=" + strconv.FormatInt(info.Size(), 10) + "-" + strconv.FormatInt(info.ModTime().Unix(), 10)
}

// syncExtraStill brings an extra's card image in line with the sidecar beside
// its file. current is the extra's stored still_source. An unchanged sidecar
// costs nothing beyond the folder listing; a new or replaced one is read and
// cached; a removed one clears the image. An image that cannot be decoded is
// recorded without one, so it is read again only once it changes. Other
// failures, including a folder that cannot be read, leave the stored image
// alone; they are logged and retried on the next scan, and never fail the
// extra itself.
func (s *Scanner) syncExtraStill(ctx context.Context, extraID, videoPath, current string, dirs *extraStillDirs) {
	if s.imageCacher == nil {
		return
	}
	path, info, ok := dirs.find(videoPath)
	if !ok {
		slog.WarnContext(ctx, "scanner: reading extra image folder failed", "component", "scanner", "path", videoPath)
		return
	}
	if path == "" {
		if strings.HasPrefix(current, extraStillSourceScheme) {
			s.setExtraStill(ctx, extraID, videoPath, "", "", "")
		}
		return
	}
	source := extraStillSource(path, info)
	if source == current {
		return
	}
	stored, thumbhash, err := s.cacheExtraStill(ctx, extraID, path, info)
	switch {
	case errors.Is(err, errUndecodableExtraStill):
		slog.WarnContext(ctx, "scanner: extra image cannot be decoded; skipped until it changes", "component", "scanner", "path", path, "error", err)
		s.setExtraStill(ctx, extraID, path, "", source, "")
	case err != nil:
		slog.WarnContext(ctx, "scanner: caching extra image failed", "component", "scanner", "path", path, "error", err)
	default:
		s.setExtraStill(ctx, extraID, path, stored, source, thumbhash)
	}
}

func (s *Scanner) setExtraStill(ctx context.Context, extraID, logPath, stored, source, thumbhash string) {
	if err := s.extraRepo.SetStill(ctx, extraID, stored, source, thumbhash); err != nil {
		slog.WarnContext(ctx, "scanner: saving extra image failed", "component", "scanner", "path", logPath, "error", err)
	}
}

// cacheExtraStill reads the sidecar at path, which find inspected as info,
// and stores it as the extra's card image. The read stays inside the
// sidecar's folder, and only the exact regular file find inspected is read,
// so an image swapped for a symlink or another file in between is refused.
func (s *Scanner) cacheExtraStill(ctx context.Context, extraID, path string, info fs.FileInfo) (string, string, error) {
	file, err := os.OpenInRoot(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return "", "", err
	}
	defer func() { _ = file.Close() }()
	stat, err := file.Stat()
	if err != nil {
		return "", "", err
	}
	if !os.SameFile(info, stat) || !stat.Mode().IsRegular() {
		return "", "", fmt.Errorf("extra image changed while being read: %s", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxExtraStillBytes+1))
	if err != nil {
		return "", "", err
	}
	if len(data) == 0 || len(data) > maxExtraStillBytes {
		return "", "", fmt.Errorf("extra image is empty or over %d bytes", maxExtraStillBytes)
	}
	stored, thumbhash, err := s.imageCacher.CacheExtraStill(ctx, data, extraID)
	// The same test a collection artwork upload uses for bad input.
	if err != nil && (errors.Is(err, imageutil.ErrInvalidImage) || imageutil.PixelDataUndecodable(data)) {
		return "", "", fmt.Errorf("%w: %w", errUndecodableExtraStill, err)
	}
	return stored, thumbhash, err
}
