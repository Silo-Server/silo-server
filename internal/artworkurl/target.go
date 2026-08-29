package artworkurl

import (
	"errors"
	"strings"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
)

// CacheKey is a process-local map key for one target. It contains no source
// reference and must never be persisted as a catalog identity.
func (t Target) CacheKey() string {
	return t.Surface + "\x00" + strings.Join(t.Keys, "\x00") + "\x00" + t.Slot
}

// Stable artwork surface names. These values are persisted in signed target
// capabilities and must not be derived from slice positions.
const (
	SurfaceItemPosters            = "item posters"
	SurfaceItemBackdrops          = "item backdrops"
	SurfaceItemLogos              = "item logos"
	SurfaceLocalizedItemPosters   = "localized item posters"
	SurfaceLocalizedItemBackdrops = "localized item backdrops"
	SurfaceLocalizedItemLogos     = "localized item logos"
	SurfaceSeasonPosters          = "season posters"
	SurfaceLocalizedSeasonPosters = "localized season posters"
	SurfaceEpisodeStills          = "episode stills"
	SurfacePersonPhotos           = "person photos"
	SurfaceCollectionPosters      = "collection posters"
	SurfaceCollectionBackdrops    = "collection backdrops"
	SurfaceUserCollectionPosters  = "user collection posters"
	SurfaceLibraryPosters         = "library posters"
	SurfaceProfileAvatars         = "profile avatars"
	SurfaceChapterThumbnails      = "chapter thumbnails"
)

// Target identifies one catalog artwork selection. Reference is presentation
// input and is deliberately omitted from the signed capability: the delivery
// handler reloads the current selected and source references from the owning
// catalog row on every fallback.
type Target struct {
	Surface          string   `json:"surface"`
	Keys             []string `json:"keys"`
	Slot             string   `json:"slot"`
	ExpectedRevision string   `json:"expected_revision,omitempty"`
	Reference        string   `json:"-"`
}

// TargetRequest lets batch callers retain a distinct standard variant for
// each target while sharing one resolver invocation.
type TargetRequest struct {
	Target  Target
	Variant string
}

func (r TargetRequest) CacheKey() string { return r.Target.CacheKey() + "\x00" + r.Variant }

func (t Target) Validate() error {
	if strings.TrimSpace(t.Surface) == "" || strings.TrimSpace(t.Slot) == "" || len(t.Keys) == 0 {
		return errors.New("artworkurl: invalid artwork target")
	}
	for _, key := range t.Keys {
		if strings.TrimSpace(key) == "" || strings.ContainsRune(key, '\x00') {
			return errors.New("artworkurl: invalid artwork target key")
		}
	}
	if t.ExpectedRevision != "" && (len(t.ExpectedRevision) > 256 || strings.ContainsRune(t.ExpectedRevision, '\x00')) {
		return errors.New("artworkurl: invalid expected artwork revision")
	}
	return nil
}

// WithReference fills the non-serialized selected reference and derives the
// immutable revision when the reference carries one.
func (t Target) WithReference(reference string) Target {
	t.Reference = strings.TrimSpace(reference)
	t.ExpectedRevision = artworkkey.Revision(t.Reference)
	return t
}

// VariantFromReference returns the bounded stored variant named by reference,
// or original for a source reference that has no materialized variant yet.
func VariantFromReference(reference string) string {
	if info, ok := artworkkey.ParsePortableKey(reference); ok && info.Variant != "" {
		return info.Variant
	}
	base := reference
	if slash := strings.LastIndexByte(base, '/'); slash >= 0 {
		base = base[slash+1:]
	}
	if dot := strings.IndexByte(base, '.'); dot > 0 {
		candidate := base[:dot]
		if isStandardVariant(candidate) {
			return candidate
		}
	}
	return artworkkey.OriginalVariant
}

func isStandardVariant(variant string) bool {
	if variant == artworkkey.OriginalVariant {
		return true
	}
	if !strings.HasPrefix(variant, "w") || len(variant) < 2 || len(variant) > 6 {
		return false
	}
	for _, r := range variant[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ValidateVariant limits capabilities to the frozen standard ladders. Upload
// image types use the same artworkkey source of truth as materialization and
// GC, so a signer and a handler cannot drift on accepted names.
func ValidateVariant(slot, variant string) error {
	for _, allowed := range artworkkey.VariantNames(slot) {
		if variant == allowed {
			return nil
		}
	}
	return errors.New("artworkurl: unsupported artwork variant")
}
