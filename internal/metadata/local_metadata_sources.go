package metadata

import "strings"

// localMetadataURLPrefix marks an image URL the item's local metadata names
// (an NFO <thumb>), as opposed to one a remote provider returned. The NFO
// author is not a trusted provider, so such a URL is handled like a local
// sidecar file: it waits in *_source_path until the image cache has copied it
// (downloading through the cache's public-address guard) and is never served
// to clients or fetched by any other path.
const localMetadataURLPrefix = "nfo+"

// LocalMetadataURLSource returns the artwork source for an http(s) image URL
// local metadata names, for example "nfo+https://img.example.org/p.jpg".
func LocalMetadataURLSource(rawURL string) string {
	return localMetadataURLPrefix + rawURL
}

// isLocalMetadataURLSource reports whether lowerPath, already lower-cased, is
// a source LocalMetadataURLSource made.
func isLocalMetadataURLSource(lowerPath string) bool {
	rest, ok := strings.CutPrefix(lowerPath, localMetadataURLPrefix)
	return ok && (strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://"))
}

// localSourceProviderID attributes a local source in the image cache:
// "nfo" for an NFO-named URL, whose copy is downloaded like a provider's and
// keyed like one, and the synthetic "local" provider for a sidecar file,
// whose copy is keyed by a content hash under local/.
func localSourceProviderID(source string) string {
	if isLocalMetadataURLSource(strings.ToLower(strings.TrimSpace(source))) {
		return localMetadataURLProviderID
	}
	return imageCacheLocalProviderID
}

// localMetadataURLProviderID is the image-cache provider for NFO-named URLs.
const localMetadataURLProviderID = "nfo"

// localMetadataImageURL returns the URL a LocalMetadataURLSource source names.
func localMetadataImageURL(source string) (string, bool) {
	source = strings.TrimSpace(source)
	if !isLocalMetadataURLSource(strings.ToLower(source)) {
		return "", false
	}
	return source[len(localMetadataURLPrefix):], true
}
