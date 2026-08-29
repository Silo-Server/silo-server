package artworkkey

import "strings"

// Upload image types.
//
// Administrator- and user-supplied images — library posters, collection posters
// and backdrops, profile avatars — are stored in exactly the same portable
// layout as catalog artwork:
//
//	artwork/v1/objects/{upload-image-type}/{revision[0:2]}/{revision}/original.{ext}
//
// There is deliberately no separate "uploads" prefix. An upload is artwork; it
// is addressed by the bytes it produced, carries a manifest, expands through
// ObjectKeys, parses through ParsePortableKey, and is delivered by the same
// resolver. A second grammar would have to be taught to every one of those, and
// would buy nothing: what distinguishes an upload from provider art is its
// image type and the fact that it has no re-downloadable source, not where it
// lives.
//
// The type names are their own namespace, so a library poster and a collection
// poster with identical bytes are still distinct revisions. That costs a little
// duplication and keeps deletion reasoning simple: no revision is ever shared
// across surfaces that answer to different owners.
//
// Two upload surfaces stay outside this namespace on purpose:
//
//   - Branding assets keep their existing "branding/{kind}/{ref}" keys. They are
//     already immutable and content-addressed, they are served from bytes by
//     Silo rather than resolved to a URL, and every configured installation
//     already stores a ref in that form.
//   - Legacy upload keys ("library-posters/{id}.jpg",
//     "collection-images/{id}/poster/original.webp",
//     "profile-avatars/{user}/{profile}/original.webp") are never rewritten.
//     They stay readable until their owning row is next replaced.
const (
	// ImageTypeLibraryPoster is an administrator-uploaded library tile image.
	ImageTypeLibraryPoster = "library-poster"
	// ImageTypeCollectionPoster is an uploaded or source-fetched collection
	// poster, for both admin and user collections.
	ImageTypeCollectionPoster = "collection-poster"
	// ImageTypeCollectionBackdrop is an uploaded or source-fetched collection
	// backdrop.
	ImageTypeCollectionBackdrop = "collection-backdrop"
	// ImageTypeAvatar is an uploaded profile avatar. Its variants are square.
	ImageTypeAvatar = "avatar"
)

// UploadImageTypes returns every upload artwork type in a stable order.
func UploadImageTypes() []string {
	return []string{
		ImageTypeLibraryPoster,
		ImageTypeCollectionPoster,
		ImageTypeCollectionBackdrop,
		ImageTypeAvatar,
	}
}

// IsUploadImageType reports whether an image type names a raw-upload surface.
// Upload artwork has no re-downloadable source: it is protected from the
// reconciler's provider reset path and from safe purge, and can only be
// restored by uploading it again.
func IsUploadImageType(imageType string) bool {
	switch strings.ToLower(strings.TrimSpace(imageType)) {
	case ImageTypeLibraryPoster, ImageTypeCollectionPoster, ImageTypeCollectionBackdrop, ImageTypeAvatar:
		return true
	default:
		return false
	}
}

// IsBrandingKey reports whether key belongs to the immutable custom-branding
// namespace. Branding objects are canonical-store bytes, but they are not
// artwork revisions and therefore never appear in revision GC inventory.
func IsBrandingKey(key string) bool {
	return strings.HasPrefix(strings.TrimSpace(key), "branding/")
}

// IsLegacyUploadKey reports whether key belongs to one of the mutable upload
// namespaces retained for rows that have not yet been replaced by portable
// artwork revisions.
func IsLegacyUploadKey(key string) bool {
	key = strings.TrimSpace(key)
	for _, prefix := range []string{
		"library-posters/",
		"collection-images/",
		"user-collection-images/",
		"profile-avatars/",
	} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// CollectionImageType maps a collection artwork slot ("poster" or "backdrop")
// onto its upload image type. It reports false for anything else so a handler
// rejects an unsupported slot before reading an upload body.
func CollectionImageType(slot string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(slot)) {
	case ImageTypePoster:
		return ImageTypeCollectionPoster, true
	case ImageTypeBackdrop:
		return ImageTypeCollectionBackdrop, true
	default:
		return "", false
	}
}
