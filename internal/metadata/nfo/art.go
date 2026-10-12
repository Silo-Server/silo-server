package nfo

import (
	"context"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/netguard"
)

// nfoThumb is a Kodi <thumb> artwork reference: a URL, or a path relative to
// the NFO's folder. aspect names the artwork kind; in a tvshow.nfo,
// type="season" with season="N" scopes it to season N.
type nfoThumb struct {
	Aspect string `xml:"aspect,attr"`
	Type   string `xml:"type,attr"`
	Season string `xml:"season,attr"`
	Value  string `xml:",chardata"`
}

// nfoFanart is <fanart>, whose <thumb>s are backdrops. Older Kodi exports
// write them relative to the url attribute.
type nfoFanart struct {
	URL    string     `xml:"url,attr"`
	Thumbs []nfoThumb `xml:"thumb"`
}

// nfoArtKind is an artwork kind an NFO names.
type nfoArtKind string

const (
	artPoster    nfoArtKind = "poster"
	artLogo      nfoArtKind = "clearlogo"
	artBackdrop  nfoArtKind = "fanart"
	artLandscape nfoArtKind = "landscape"
	artBanner    nfoArtKind = "banner"
	artClearArt  nfoArtKind = "clearart"
	artDiscArt   nfoArtKind = "discart"
	artKeyArt    nfoArtKind = "keyart"
	artStill     nfoArtKind = "still" // an episode's <thumb>
)

// nfoArtAspects maps a lower-cased <thumb aspect> to its kind. A thumb with
// no aspect is a poster; an aspect not listed is ignored.
var nfoArtAspects = map[string]nfoArtKind{
	"":                   artPoster,
	string(artPoster):    artPoster,
	string(artLogo):      artLogo,
	"logo":               artLogo,
	string(artBackdrop):  artBackdrop,
	string(artLandscape): artLandscape,
	"thumb":              artLandscape,
	string(artBanner):    artBanner,
	string(artClearArt):  artClearArt,
	string(artDiscArt):   artDiscArt,
	"disc":               artDiscArt,
	string(artKeyArt):    artKeyArt,
}

// nfoArtImageTypes maps each artwork kind that has an image slot to it.
// Banner, clear art, disc art, key art, and landscape (Jellyfin's thumb) art
// are parsed but have no slot yet: adding one is an entry here (and, for a
// sidecar file, an itemArtworkNames case).
var nfoArtImageTypes = map[nfoArtKind]metadata.ImageType{
	artPoster:   metadata.ImagePoster,
	artLogo:     metadata.ImageLogo,
	artBackdrop: metadata.ImageBackdrop,
}

// nfoArtRef is one artwork reference an NFO makes, as written.
type nfoArtRef struct {
	Kind nfoArtKind
	// Season is the season a tvshow <thumb season="N"> applies to, when
	// SeasonSet. Item artwork has SeasonSet false.
	Season    int
	SeasonSet bool
	Ref       string
}

// tvdbBannerPath matches the TVDB banner paths older Kodi exports list as
// <thumb>s with no aspect: wide graphical, text, and blank banners, and wide
// season banners.
var tvdbBannerPath = regexp.MustCompile(`(?i)/banners/(?:graphical|text|blank|seasonswide)/`)

// itemArtRefs lists a <movie> or <tvshow>'s <thumb> and <fanart> references:
// the ones that name their aspect first, in document order, then the
// aspect-less <thumb>s, which count as posters unless they are TVDB banners.
func itemArtRefs(thumbs []nfoThumb, fanart nfoFanart) []nfoArtRef {
	var refs, unnamed []nfoArtRef
	for _, thumb := range thumbs {
		aspect := strings.ToLower(strings.TrimSpace(thumb.Aspect))
		kind, ok := nfoArtAspects[aspect]
		ref := strings.TrimSpace(thumb.Value)
		if !ok || ref == "" || (aspect == "" && tvdbBannerPath.MatchString(ref)) {
			continue
		}
		art := nfoArtRef{Kind: kind, Ref: ref}
		if strings.EqualFold(strings.TrimSpace(thumb.Type), "season") || strings.TrimSpace(thumb.Season) != "" {
			// season="-1" is Kodi's "all seasons" art, not one season's.
			season, ok := parseNFOInt(thumb.Season)
			if !ok || season < 0 {
				continue
			}
			art.Season, art.SeasonSet = season, true
		}
		if aspect == "" {
			unnamed = append(unnamed, art)
			continue
		}
		refs = append(refs, art)
	}
	refs = append(refs, unnamed...)
	base := strings.TrimSpace(fanart.URL)
	for _, thumb := range fanart.Thumbs {
		ref := strings.TrimSpace(thumb.Value)
		if ref == "" {
			continue
		}
		if base != "" && !strings.Contains(ref, "://") {
			ref = strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(ref, "/")
		}
		refs = append(refs, nfoArtRef{Kind: artBackdrop, Ref: ref})
	}
	return refs
}

// episodeArtRefs lists an episode's <thumb> references: its still.
func episodeArtRefs(thumbs []nfoThumb) []nfoArtRef {
	var refs []nfoArtRef
	for _, thumb := range thumbs {
		if ref := strings.TrimSpace(thumb.Value); ref != "" {
			refs = append(refs, nfoArtRef{Kind: artStill, Ref: ref})
		}
	}
	return refs
}

// artResolver turns the image references an NFO makes into image sources.
// Local paths resolve against the NFO's folder and must stay inside one of
// the folders holding the item's media files or directory-level sidecars;
// anything outside, missing, or not a usable image is dropped here rather
// than queued for a copy that can only fail.
type artResolver struct {
	// finder checks a local image as sidecar discovery does and returns its
	// fingerprinted source.
	finder  *sidecarFinder
	nfoDir  string
	folders []string
}

func newArtResolver(finder *sidecarFinder, nfoPath string, folders []string) artResolver {
	return artResolver{finder: finder, nfoDir: filepath.Dir(nfoPath), folders: compactNFOPaths(folders)}
}

// source returns the image source for ref: an http(s) URL as a
// metadata.LocalMetadataURLSource, or a local image as a fingerprinted
// file:// source (metadata.LocalImageSourceURL). Any other reference gives "".
func (r artResolver) source(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if strings.Contains(ref, "://") {
		return remoteArtSource(ref)
	}
	// Windows paths and other platforms' forms name nothing on this server.
	if strings.ContainsRune(ref, '\\') || (len(ref) > 1 && ref[1] == ':') {
		return ""
	}
	path := ref
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.nfoDir, path)
	}
	path = filepath.Clean(path)
	if !slices.Contains(localArtworkExtensions, strings.ToLower(filepath.Ext(path))) || !r.contains(path) {
		return ""
	}
	return r.finder.usableSource(path)
}

// contains reports whether path lies in one of the item's folders, both as
// written and with symlinks resolved, so a symlinked directory inside the
// folder cannot lead out of it. The image cache processor confines the read
// to the library's roots again.
func (r artResolver) contains(path string) bool {
	if !slices.ContainsFunc(r.folders, func(folder string) bool { return pathWithin(path, folder) }) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(r.folders, func(folder string) bool {
		real, ok := r.finder.realDir(folder)
		return ok && pathWithin(resolved, real)
	})
}

// realDir returns dir with symlinks resolved, resolving each folder once per
// request: every episode's resolver checks against the same series roots.
// ok is false when dir cannot be resolved.
func (f *sidecarFinder) realDir(dir string) (string, bool) {
	real, seen := f.realDirs[dir]
	if !seen {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			real = resolved
		}
		if f.realDirs == nil {
			f.realDirs = make(map[string]string)
		}
		f.realDirs[dir] = real
	}
	return real, real != ""
}

// maxArtURLBytes caps a remote artwork URL an NFO names.
const maxArtURLBytes = 2048

// remoteArtSource returns the source for an http(s) image URL an NFO names,
// one the image cache could download: it needs a host and no credentials,
// and a literal address must be public (the cache refuses the rest, so they
// are dropped up front). Host names are checked when the cache connects.
func remoteArtSource(raw string) string {
	if len(raw) > maxArtURLBytes {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return ""
	}
	if addr, err := netip.ParseAddr(host); err == nil && netguard.Classify(addr) != netguard.Public {
		return ""
	}
	return metadata.LocalMetadataURLSource(u.String())
}

// pathWithin reports whether path is folder or lies under it.
func pathWithin(path, folder string) bool {
	rel, err := filepath.Rel(folder, path)
	return err == nil && filepath.IsLocal(rel)
}

// itemFolders are the folders an item's NFO may name local images in: the
// directory-level sidecar folders and the folders of its media files.
func itemFolders(sidecarDirs, mediaFiles []string) []string {
	folders := slices.Clone(sidecarDirs)
	for _, file := range mediaFiles {
		folders = append(folders, filepath.Dir(file))
	}
	return folders
}

// nfoItemArt returns the artwork the item's NFO names for each image slot
// sidecar files leave empty, first reference first: sidecar files outrank it.
// Its sources are local (file:// or a metadata.LocalMetadataURLSource), so
// they rank above remote providers' artwork like sidecars do.
func (p *Provider) nfoItemArt(ctx context.Context, req metadata.ImageRequest, finder *sidecarFinder, mediaFiles, sidecarDirs []string, sidecars []metadata.RemoteImage) []metadata.RemoteImage {
	have := make(map[metadata.ImageType]bool, len(sidecars))
	for _, img := range sidecars {
		have[img.Type] = true
	}
	if req.SeasonNumber != nil || len(mediaFiles) == 0 || len(have) == len(nfoArtImageTypes) {
		return nil
	}
	nfoPath, parsed, err := p.newLookup(ctx).findItemNFO(mediaFiles, sidecarDirs, req.ContentType)
	if err != nil || parsed == nil || len(parsed.Art) == 0 {
		return nil
	}
	resolver := newArtResolver(finder, nfoPath, itemFolders(sidecarDirs, mediaFiles))
	var images []metadata.RemoteImage
	for _, ref := range parsed.Art {
		imageType, ok := nfoArtImageTypes[ref.Kind]
		if !ok || ref.SeasonSet || have[imageType] {
			continue
		}
		if source := resolver.source(ref.Ref); source != "" {
			images = append(images, metadata.RemoteImage{ProviderID: p.Slug(), URL: source, Type: imageType})
			have[imageType] = true
		}
	}
	return images
}

// resolvePeoplePhotos resolves each credit's NFO <thumb> into a photo source
// against the NFO's folder, dropping the ones that name no usable image.
func resolvePeoplePhotos(people []models.ItemPerson, resolver artResolver) {
	for i := range people {
		if people[i].LocalPhotoSource != "" {
			people[i].LocalPhotoSource = resolver.source(people[i].LocalPhotoSource)
		}
	}
}

// firstArtSource returns the first reference of kind (for season, when
// seasonSet) that resolves to a source.
func firstArtSource(refs []nfoArtRef, kind nfoArtKind, season int, seasonSet bool, resolver artResolver) string {
	for _, ref := range refs {
		if ref.Kind != kind || ref.SeasonSet != seasonSet || (seasonSet && ref.Season != season) {
			continue
		}
		if source := resolver.source(ref.Ref); source != "" {
			return source
		}
	}
	return ""
}
