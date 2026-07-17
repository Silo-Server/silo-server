package nfo

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

// maxLocalArtworkBytes caps sidecar artwork reads at 8 MiB (mirrors the
// audiobook sidecar cover guard). Discovery rejects larger files up front so
// the cache processor never queues an oversized read.
const maxLocalArtworkBytes = 8 << 20

var localArtworkExtensions = []string{".jpg", ".jpeg", ".png", ".webp"}

// localArtworkNames maps each image type to its sidecar base names in
// precedence order. "{basename}" expands to the media file's basename and is
// searched next to the media files; plain names are searched only in the
// directory-level sidecar search paths, which upstream code restricts to
// single-content observed roots (canUseObservedRootForDirectorySidecars), so
// one folder.jpg in a flat multi-movie folder attaches to none of them.
var localArtworkNames = map[metadata.ImageType][]string{
	metadata.ImagePoster:   {"poster", "folder", "cover", "{basename}-poster"},
	metadata.ImageBackdrop: {"fanart", "backdrop", "background", "{basename}-fanart"},
	metadata.ImageLogo:     {"logo", "clearlogo", "{basename}-logo"},
}

// GetImages implements metadata.ImageProvider by discovering sidecar artwork
// (poster.jpg, fanart.jpg, clearlogo.png, <basename>-poster.jpg, ...) next to
// the item's media files. Returned URLs use the file:// scheme with the
// absolute logical path; the image cache processor copies them into S3 so
// library files are never served directly. Local art applies even when no
// .nfo file is present.
func (p *Provider) GetImages(_ context.Context, req metadata.ImageRequest) ([]metadata.RemoteImage, error) {
	searchDirs := compactNFOPaths(req.PrimarySidecarSearchPaths)
	mediaFiles := compactNFOPaths(append([]string{req.RepresentativeFilePath}, req.AllGroupFilePaths...))
	if len(searchDirs) == 0 && len(mediaFiles) == 0 {
		return nil, nil
	}

	var images []metadata.RemoteImage
	for _, imageType := range []metadata.ImageType{metadata.ImagePoster, metadata.ImageBackdrop, metadata.ImageLogo} {
		if path := findLocalArtwork(localArtworkNames[imageType], searchDirs, mediaFiles); path != "" {
			images = append(images, metadata.RemoteImage{
				ProviderID: p.Slug(),
				URL:        "file://" + path,
				Type:       imageType,
				Rating:     0,
			})
		}
	}
	return images, nil
}

// findLocalArtwork returns the absolute path of the first matching sidecar
// image, trying names in precedence order across every candidate location.
func findLocalArtwork(names []string, searchDirs []string, mediaFiles []string) string {
	for _, name := range names {
		if suffix, ok := strings.CutPrefix(name, "{basename}"); ok {
			for _, mediaFile := range mediaFiles {
				dir := filepath.Dir(mediaFile)
				base := strings.TrimSuffix(filepath.Base(mediaFile), filepath.Ext(mediaFile))
				if path := findLocalArtworkFile(dir, base+suffix); path != "" {
					return path
				}
			}
			continue
		}
		for _, dir := range searchDirs {
			if path := findLocalArtworkFile(dir, name); path != "" {
				return path
			}
		}
	}
	return ""
}

func findLocalArtworkFile(dir, name string) string {
	for _, ext := range localArtworkExtensions {
		path := filepath.Join(dir, name+ext)
		info, err := os.Lstat(path)
		if err != nil {
			continue
		}
		// Reject symlinked leaves and non-regular files, and cap the size so
		// the cache processor's re-checks agree with discovery.
		if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxLocalArtworkBytes {
			continue
		}
		return path
	}
	return ""
}
