package artworkkey

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
)

const (
	// PortableStorageFormat names the logical key layout every new
	// materialization is written in. It is what the artwork capability reports
	// as storage_format, so a client can tell which layout a server's keys
	// belong to without inspecting one.
	PortableStorageFormat = "artwork/v1"

	// PortableObjectsPrefix is the root of the portable, content-addressed
	// layout. Every new materialization lands beneath it:
	//
	//	artwork/v1/objects/{image-type}/{revision[0:2]}/{revision}/manifest.json
	//	artwork/v1/objects/{image-type}/{revision[0:2]}/{revision}/original.webp
	//	artwork/v1/objects/{image-type}/{revision[0:2]}/{revision}/w500.webp
	//
	// The prefix is part of the logical key, never of the physical location: an
	// S3 adapter may sit it below an operator key prefix and a filesystem
	// adapter below any root, and both strip that location before returning
	// logical keys. Copying artwork/v1/... between backends therefore preserves
	// every catalog reference.
	PortableObjectsPrefix = PortableStorageFormat + "/objects"

	// PortableRecipeVersion is the frozen recipe identifier that opens the
	// canonical revision stream. It covers everything that can change encoded
	// output: the variant ladder, encoder settings, orientation and color
	// handling, and the output container. Any output-affecting change must bump
	// it, which re-addresses every future write without invalidating or
	// overwriting a single stored object.
	PortableRecipeVersion = "silo-artwork-recipe-v1"

	// PortableFormatVersion is the storage-format version recorded in every
	// manifest. It describes the layout and manifest grammar, not the encoder.
	PortableFormatVersion = 1

	// ManifestName is the completeness marker written into a revision directory
	// after every image object has been stored successfully. A revision
	// directory without it is incomplete and must never be published or
	// adopted.
	ManifestName = "manifest.json"

	// portableShardLength is the length of the directory shard taken from the
	// front of the revision. It only keeps directories from growing
	// pathologically wide; it carries no identity meaning and the full digest
	// still appears in the path.
	portableShardLength = 2
)

// VariantBytes is one produced variant: its ladder name and its exact encoded
// bytes. Both participate in the revision digest.
type VariantBytes struct {
	Name string
	Data []byte
}

// RevisionInput describes a completed encode: what was produced, for which
// image type, in which output media type.
type RevisionInput struct {
	// ImageType is the normalized artwork type ("poster", "backdrop", "logo",
	// "still", "profile").
	ImageType string
	// MediaType is the output media type of every variant, e.g. "image/webp".
	// It is part of the revision digest, so it is required rather than
	// derived: a caller that cannot name its own output cannot address it.
	MediaType string
	// Ext is the output file extension including its dot, e.g. ".webp".
	Ext string
	// Variants are the produced variants in any order. An "original" variant is
	// required; it is the object catalog rows point at.
	Variants []VariantBytes
}

// PortableRevision is a fully addressed revision: the content-derived digest,
// every logical key it occupies, and the manifest bytes to write last.
type PortableRevision struct {
	// Revision is the lowercase hex SHA-256 of the canonical revision stream.
	Revision string
	// ImageType, MediaType, and Ext are the normalized inputs.
	ImageType string
	MediaType string
	Ext       string
	// Directory is the revision directory without a trailing slash.
	Directory string
	// OriginalKey is the logical key of the original variant — the value a
	// catalog target is set to once publication succeeds.
	OriginalKey string
	// ManifestKey is the logical key of manifest.json.
	ManifestKey string
	// VariantKeys maps each variant name to its logical key.
	VariantKeys map[string]string
	// Manifest is the decoded completeness marker.
	Manifest Manifest
	// ManifestJSON is the canonical encoding of Manifest — the exact bytes to
	// store at ManifestKey.
	ManifestJSON []byte
}

// ObjectKeys returns every logical key the revision occupies, manifest
// included, in sorted order. This is the set registered for garbage collection
// before the first byte is written.
func (r *PortableRevision) ObjectKeys() []string {
	if r == nil {
		return nil
	}
	keys := make([]string, 0, len(r.VariantKeys)+1)
	for _, key := range r.VariantKeys {
		keys = append(keys, key)
	}
	if r.ManifestKey != "" {
		keys = append(keys, r.ManifestKey)
	}
	sort.Strings(keys)
	return keys
}

// BuildPortableRevision derives the revision digest, every logical key, and the
// canonical manifest for a produced variant set.
//
// The digest is taken over the encoded bytes themselves, never over a provider
// URL, item ID, library path, or installation identity, so two servers that
// encode the same source under the same recipe converge on the same logical
// directory, and any byte, ladder, format, or recipe change lands on a
// different immutable revision instead of mutating an existing one.
func BuildPortableRevision(in RevisionInput) (*PortableRevision, error) {
	imageType, err := normalizePortableImageType(in.ImageType)
	if err != nil {
		return nil, err
	}
	mediaType, err := normalizePortableMediaType(in.MediaType)
	if err != nil {
		return nil, err
	}
	ext, err := normalizePortableExt(in.Ext)
	if err != nil {
		return nil, err
	}
	variants, err := normalizePortableVariants(in.Variants)
	if err != nil {
		return nil, err
	}

	revision := portableRevisionDigest(imageType, mediaType, variants)
	directory := PortableDirectory(imageType, revision)

	variantKeys := make(map[string]string, len(variants))
	manifestVariants := make([]ManifestVariant, 0, len(variants))
	for _, variant := range variants {
		filename := variant.Name + ext
		variantKeys[variant.Name] = directory + "/" + filename
		manifestVariants = append(manifestVariants, ManifestVariant{
			Digest:    hexDigest(variant.Data),
			Filename:  filename,
			Name:      variant.Name,
			SizeBytes: int64(len(variant.Data)),
		})
	}

	manifest := Manifest{
		FormatVersion: PortableFormatVersion,
		ImageType:     imageType,
		MediaType:     mediaType,
		RecipeVersion: PortableRecipeVersion,
		Revision:      revision,
		Variants:      manifestVariants,
	}
	manifestJSON, err := EncodeManifest(manifest)
	if err != nil {
		return nil, err
	}

	return &PortableRevision{
		Revision:     revision,
		ImageType:    imageType,
		MediaType:    mediaType,
		Ext:          ext,
		Directory:    directory,
		OriginalKey:  variantKeys[OriginalVariant],
		ManifestKey:  directory + "/" + ManifestName,
		VariantKeys:  variantKeys,
		Manifest:     manifest,
		ManifestJSON: manifestJSON,
	}, nil
}

// portableRevisionDigest hashes the canonical revision stream:
//
//	"silo-artwork-recipe-v1" 0x00
//	image-type               0x00
//	media-type               0x00
//	for each variant, ordered lexically by name:
//	    name                 0x00
//	    big-endian uint64 byte length
//	    exact encoded bytes
//
// NUL-terminated names and length-prefixed payloads make the stream
// unambiguous: no two distinct variant sets can produce the same bytes.
// variants must already be normalized and sorted.
func portableRevisionDigest(imageType, mediaType string, variants []VariantBytes) string {
	h := sha256.New()
	writeField := func(s string) {
		_, _ = io.WriteString(h, s)
		_, _ = h.Write([]byte{0})
	}
	writeField(PortableRecipeVersion)
	writeField(imageType)
	writeField(mediaType)
	var size [8]byte
	for _, variant := range variants {
		writeField(variant.Name)
		binary.BigEndian.PutUint64(size[:], uint64(len(variant.Data)))
		_, _ = h.Write(size[:])
		_, _ = h.Write(variant.Data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// PortableDirectory returns the revision directory for an image type and
// revision, without a trailing slash.
func PortableDirectory(imageType, revision string) string {
	imageType = strings.ToLower(strings.TrimSpace(imageType))
	revision = strings.ToLower(strings.TrimSpace(revision))
	if imageType == "" || len(revision) <= portableShardLength {
		return ""
	}
	return PortableObjectsPrefix + "/" + imageType + "/" + revision[:portableShardLength] + "/" + revision
}

// PortableKey returns the logical key of one variant within a revision.
func PortableKey(imageType, revision, variant, ext string) string {
	directory := PortableDirectory(imageType, revision)
	variant = strings.TrimSpace(variant)
	if directory == "" || variant == "" {
		return ""
	}
	if ext == "" {
		ext = defaultVariantExt
	} else if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return directory + "/" + variant + ext
}

// PortableManifestKey returns the manifest key for a revision.
func PortableManifestKey(imageType, revision string) string {
	directory := PortableDirectory(imageType, revision)
	if directory == "" {
		return ""
	}
	return directory + "/" + ManifestName
}

// PortableKeyInfo is a parsed portable logical key.
type PortableKeyInfo struct {
	// ImageType is the type segment, which is authoritative for the object:
	// it was fixed when the revision was addressed.
	ImageType string
	// Revision is the full content digest.
	Revision string
	// Directory is the revision directory, without a trailing slash.
	Directory string
	// Variant is the ladder name ("original", "w500", ...), empty for the
	// manifest.
	Variant string
	// Ext is the file extension including its dot.
	Ext string
	// IsManifest reports whether the key addresses manifest.json.
	IsManifest bool
}

// ParsePortableKey parses a portable logical key. It reports false for legacy
// provider-oriented keys, plugin references, and anything else, so callers can
// keep serving both grammars from one code path.
func ParsePortableKey(key string) (PortableKeyInfo, bool) {
	key = strings.TrimSpace(key)
	if key == "" || strings.Contains(key, "://") {
		return PortableKeyInfo{}, false
	}
	rest, ok := strings.CutPrefix(key, PortableObjectsPrefix+"/")
	if !ok {
		return PortableKeyInfo{}, false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 4 {
		return PortableKeyInfo{}, false
	}
	imageType, shard, revision, filename := parts[0], parts[1], parts[2], parts[3]
	if err := validatePortableImageType(imageType); err != nil {
		return PortableKeyInfo{}, false
	}
	if !isHexDigest(revision) || len(revision) <= portableShardLength || shard != revision[:portableShardLength] {
		return PortableKeyInfo{}, false
	}
	info := PortableKeyInfo{
		ImageType: imageType,
		Revision:  revision,
		Directory: PortableObjectsPrefix + "/" + imageType + "/" + shard + "/" + revision,
	}
	if filename == ManifestName {
		info.IsManifest = true
		info.Ext = ".json"
		return info, true
	}
	dot := strings.IndexByte(filename, '.')
	if dot <= 0 || dot == len(filename)-1 {
		return PortableKeyInfo{}, false
	}
	variant, ext := filename[:dot], filename[dot:]
	if err := validatePortableVariantName(variant); err != nil {
		return PortableKeyInfo{}, false
	}
	if _, err := normalizePortableExt(ext); err != nil {
		return PortableKeyInfo{}, false
	}
	info.Variant = variant
	info.Ext = ext
	return info, true
}

// IsPortableKey reports whether key belongs to the portable layout.
func IsPortableKey(key string) bool {
	_, ok := ParsePortableKey(key)
	return ok
}

// ImageTypeFromKey returns the artwork type a stored key belongs to.
//
// Portable keys carry the type in a fixed position. Legacy provider-oriented
// keys encode it as the variant's parent directory
// (".../{imageType}/{variant}.{ext}"). Full URLs, plugin references, and keys
// with no directory segment return an empty string.
func ImageTypeFromKey(key string) string {
	if info, ok := ParsePortableKey(key); ok {
		return info.ImageType
	}
	key = strings.TrimSpace(key)
	if key == "" || strings.Contains(key, "://") {
		return ""
	}
	lastSlash := strings.LastIndex(key, "/")
	if lastSlash <= 0 {
		return ""
	}
	dir := key[:lastSlash]
	return dir[strings.LastIndex(dir, "/")+1:]
}

func normalizePortableImageType(imageType string) (string, error) {
	imageType = strings.ToLower(strings.TrimSpace(imageType))
	if err := validatePortableImageType(imageType); err != nil {
		return "", err
	}
	return imageType, nil
}

// validatePortableImageType requires the already-canonical form. Parsing a
// stored key uses it directly: a key that only becomes valid after
// normalization is a different key than the one that was written, and on a
// case-insensitive filesystem it would alias the canonical one.
func validatePortableImageType(imageType string) error {
	if imageType == "" {
		return fmt.Errorf("artworkkey: image type is required")
	}
	for i := 0; i < len(imageType); i++ {
		c := imageType[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-' && i > 0:
		default:
			return fmt.Errorf("artworkkey: invalid image type %q", imageType)
		}
	}
	return nil
}

func normalizePortableMediaType(mediaType string) (string, error) {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "" {
		return "", fmt.Errorf("artworkkey: media type is required")
	}
	slash := strings.IndexByte(mediaType, '/')
	if slash <= 0 || slash == len(mediaType)-1 {
		return "", fmt.Errorf("artworkkey: invalid media type %q", mediaType)
	}
	for i := 0; i < len(mediaType); i++ {
		c := mediaType[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '/' && i == slash:
		case c == '.' || c == '+' || c == '-':
		default:
			return "", fmt.Errorf("artworkkey: invalid media type %q", mediaType)
		}
	}
	return mediaType, nil
}

func normalizePortableExt(ext string) (string, error) {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		return "", fmt.Errorf("artworkkey: output extension is required")
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if len(ext) < 2 {
		return "", fmt.Errorf("artworkkey: invalid output extension %q", ext)
	}
	for i := 1; i < len(ext); i++ {
		c := ext[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			continue
		}
		return "", fmt.Errorf("artworkkey: invalid output extension %q", ext)
	}
	return ext, nil
}

// normalizePortableVariants validates the produced set and returns it ordered
// lexically by name, which is the order the revision stream and the manifest
// both use.
func normalizePortableVariants(variants []VariantBytes) ([]VariantBytes, error) {
	if len(variants) == 0 {
		return nil, fmt.Errorf("artworkkey: at least one variant is required")
	}
	normalized := make([]VariantBytes, 0, len(variants))
	seen := make(map[string]struct{}, len(variants))
	hasOriginal := false
	for _, variant := range variants {
		name := strings.ToLower(strings.TrimSpace(variant.Name))
		if err := validatePortableVariantName(name); err != nil {
			return nil, err
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("artworkkey: duplicate variant %q", name)
		}
		if len(variant.Data) == 0 {
			return nil, fmt.Errorf("artworkkey: variant %q has no data", name)
		}
		seen[name] = struct{}{}
		if name == OriginalVariant {
			hasOriginal = true
		}
		normalized = append(normalized, VariantBytes{Name: name, Data: variant.Data})
	}
	if !hasOriginal {
		return nil, fmt.Errorf("artworkkey: variant set is missing the %q variant", OriginalVariant)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].Name < normalized[j].Name })
	return normalized, nil
}

func validatePortableVariantName(name string) error {
	if name == "" {
		return fmt.Errorf("artworkkey: variant name is required")
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return fmt.Errorf("artworkkey: invalid variant name %q", name)
		}
	}
	return nil
}

func hexDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func isHexDigest(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			continue
		}
		return false
	}
	return true
}
