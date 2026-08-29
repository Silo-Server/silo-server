package artworkkey

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Manifest is the immutable completeness marker stored at
// {revision-directory}/manifest.json.
//
// It describes only what the revision directory contains. It deliberately holds
// no source URL, provider token, absolute path, installation ID, database ID,
// user ID, bucket, physical prefix, credential, HMAC, or signature, so a store
// tree can be copied to another server, root, bucket, or backend and remain
// exactly as meaningful — and so nothing secret is ever written into artwork
// storage.
//
// Field order is lexical by JSON name, which is also the canonical encoding
// order: encoding/json emits struct fields in declaration order, so the
// declaration order here *is* the on-disk byte order. Keep it sorted.
type Manifest struct {
	// FormatVersion is the storage-format version (PortableFormatVersion). It
	// versions the layout and this document, not the encoder.
	FormatVersion int `json:"format_version"`
	// ImageType is the normalized artwork type.
	ImageType string `json:"image_type"`
	// MediaType is the output media type shared by every variant.
	MediaType string `json:"media_type"`
	// RecipeVersion is the encode recipe that produced the variants
	// (PortableRecipeVersion). It participates in the revision digest.
	RecipeVersion string `json:"recipe_version"`
	// Revision is the content digest, matching the directory it lives in.
	Revision string `json:"revision"`
	// Variants describes every stored image object, ordered lexically by name.
	Variants []ManifestVariant `json:"variants"`
}

// ManifestVariant describes one stored image object.
type ManifestVariant struct {
	// Digest is the lowercase hex SHA-256 of the stored bytes.
	Digest string `json:"digest"`
	// Filename is the object's name inside the revision directory.
	Filename string `json:"filename"`
	// Name is the ladder name ("original", "w500", ...).
	Name string `json:"name"`
	// SizeBytes is the exact stored length.
	SizeBytes int64 `json:"size_bytes"`
}

// SelectVariant returns the manifest entry to serve for requested. A manifest
// that predates LadderVersion may omit the newly-added wide rung; in that one
// case selection walks down the live ladder to the nearest variant the
// manifest actually contains, ending at original. Other omissions are not
// silently healed here because they are not explained by the ladder change.
func (m Manifest) SelectVariant(requested string) (ManifestVariant, bool) {
	byName := make(map[string]ManifestVariant, len(m.Variants))
	for _, variant := range m.Variants {
		byName[variant.Name] = variant
	}
	if variant, ok := byName[requested]; ok {
		return variant, true
	}
	if !IsLadderExtensionVariant(m.ImageType, requested) {
		return ManifestVariant{}, false
	}
	wantedWidth, ok := variantWidth(requested)
	if !ok {
		return ManifestVariant{}, false
	}
	for _, width := range VariantWidths(m.ImageType) {
		if width >= wantedWidth {
			continue
		}
		if variant, exists := byName["w"+strconv.Itoa(width)]; exists {
			return variant, true
		}
	}
	variant, ok := byName[OriginalVariant]
	return variant, ok
}

func variantWidth(variant string) (int, bool) {
	if !strings.HasPrefix(variant, "w") {
		return 0, false
	}
	width, err := strconv.Atoi(strings.TrimPrefix(variant, "w"))
	return width, err == nil && width > 0
}

// EncodeManifest returns the canonical encoding of m: UTF-8 JSON, object keys
// in lexical order, variants ordered lexically by name, no insignificant
// whitespace, no HTML escaping, and no trailing newline. The same manifest
// always encodes to the same bytes on every platform.
func EncodeManifest(m Manifest) ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(m); err != nil {
		return nil, fmt.Errorf("artworkkey: encode manifest: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// ParseManifest decodes and validates stored manifest bytes.
//
// It is strict on purpose: unknown fields are rejected, and the bytes must be
// exactly the canonical encoding of what they decode to. A manifest is an
// integrity record, so "close enough" is not adoptable — a reader that cannot
// reproduce the exact bytes cannot trust that it understands them.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("artworkkey: decode manifest: %w", err)
	}
	if decoder.More() {
		return Manifest{}, fmt.Errorf("artworkkey: manifest has trailing content")
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	canonical, err := EncodeManifest(m)
	if err != nil {
		return Manifest{}, err
	}
	if !bytes.Equal(bytes.TrimRight(data, "\n"), canonical) {
		return Manifest{}, fmt.Errorf("artworkkey: manifest is not canonically encoded")
	}
	return m, nil
}

// Validate checks the manifest's internal consistency: supported versions, a
// well-formed revision, and a variant list that is complete, unique, ordered,
// and consistent with the image type it claims. It does not read any stored
// object; ValidateManifestObjects does that.
func (m Manifest) Validate() error {
	if m.FormatVersion != PortableFormatVersion {
		return fmt.Errorf("artworkkey: unsupported manifest format version %d", m.FormatVersion)
	}
	if m.RecipeVersion == "" {
		return fmt.Errorf("artworkkey: manifest recipe version is required")
	}
	if _, err := normalizePortableImageType(m.ImageType); err != nil {
		return err
	}
	if _, err := normalizePortableMediaType(m.MediaType); err != nil {
		return err
	}
	if !isHexDigest(m.Revision) {
		return fmt.Errorf("artworkkey: manifest revision %q is not a SHA-256 digest", m.Revision)
	}
	if len(m.Variants) == 0 {
		return fmt.Errorf("artworkkey: manifest lists no variants")
	}
	hasOriginal := false
	for i, variant := range m.Variants {
		if err := validatePortableVariantName(variant.Name); err != nil {
			return err
		}
		if i > 0 && m.Variants[i-1].Name >= variant.Name {
			return fmt.Errorf("artworkkey: manifest variants are not ordered and unique at %q", variant.Name)
		}
		if variant.Name == OriginalVariant {
			hasOriginal = true
		}
		if variant.Filename == "" || strings.Contains(variant.Filename, "/") {
			return fmt.Errorf("artworkkey: manifest variant %q has an invalid filename %q", variant.Name, variant.Filename)
		}
		ext, ok := strings.CutPrefix(variant.Filename, variant.Name)
		if !ok {
			return fmt.Errorf("artworkkey: manifest variant %q filename %q does not match its name", variant.Name, variant.Filename)
		}
		if _, err := normalizePortableExt(ext); err != nil {
			return err
		}
		if variant.SizeBytes <= 0 {
			return fmt.Errorf("artworkkey: manifest variant %q has size %d", variant.Name, variant.SizeBytes)
		}
		if !isHexDigest(variant.Digest) {
			return fmt.Errorf("artworkkey: manifest variant %q digest %q is not a SHA-256 digest", variant.Name, variant.Digest)
		}
	}
	if !hasOriginal {
		return fmt.Errorf("artworkkey: manifest is missing the %q variant", OriginalVariant)
	}
	return nil
}

// Directory returns the revision directory the manifest belongs in.
func (m Manifest) Directory() string {
	return PortableDirectory(m.ImageType, m.Revision)
}

// ObjectKeys returns every logical key the manifest accounts for, manifest
// included, in sorted order.
func (m Manifest) ObjectKeys() []string {
	directory := m.Directory()
	if directory == "" {
		return nil
	}
	keys := make([]string, 0, len(m.Variants)+1)
	keys = append(keys, directory+"/"+ManifestName)
	for _, variant := range m.Variants {
		keys = append(keys, directory+"/"+variant.Filename)
	}
	// manifest.json sorts before every "original"/"wNNN" filename, and the
	// variants are already ordered, so the result is sorted by construction.
	return keys
}

// ManifestObjectReader opens a stored object by logical key. It is a function
// rather than an interface so this package stays free of any storage
// dependency: an artworkstore.Store, an S3 client, or a test fixture all adapt
// to it in a line.
type ManifestObjectReader func(ctx context.Context, key string) (io.ReadCloser, error)

// ValidateManifestObjects verifies that a manifest actually describes the
// directory it sits in: every listed object exists with the recorded size and
// digest, no more and no less than the digest requires, and the bytes re-derive
// the revision the manifest and directory claim.
//
// This is the gate before adopting a revision that this server did not write —
// a copied store tree, a shared filesystem another node materialized into, or a
// re-validation after suspected corruption. It re-runs the content addressing
// rather than trusting the document, so a tampered or truncated manifest cannot
// launder mismatched bytes into the catalog.
//
// It reads every variant, which is bounded by the artwork ladder (a few MB) and
// is why this is a validation path, not a serving path.
func ValidateManifestObjects(ctx context.Context, m Manifest, read ManifestObjectReader) error {
	if read == nil {
		return fmt.Errorf("artworkkey: manifest object reader is required")
	}
	if err := m.Validate(); err != nil {
		return err
	}
	directory := m.Directory()
	if directory == "" {
		return fmt.Errorf("artworkkey: manifest revision %q has no directory", m.Revision)
	}
	variants := make([]VariantBytes, 0, len(m.Variants))
	for _, variant := range m.Variants {
		key := directory + "/" + variant.Filename
		data, err := readManifestObject(ctx, read, key)
		if err != nil {
			return err
		}
		if int64(len(data)) != variant.SizeBytes {
			return fmt.Errorf("artworkkey: object %s is %d bytes, manifest says %d", key, len(data), variant.SizeBytes)
		}
		if digest := hexDigest(data); digest != variant.Digest {
			return fmt.Errorf("artworkkey: object %s digest %s does not match manifest digest %s", key, digest, variant.Digest)
		}
		variants = append(variants, VariantBytes{Name: variant.Name, Data: data})
	}
	normalized, err := normalizePortableVariants(variants)
	if err != nil {
		return err
	}
	imageType, err := normalizePortableImageType(m.ImageType)
	if err != nil {
		return err
	}
	mediaType, err := normalizePortableMediaType(m.MediaType)
	if err != nil {
		return err
	}
	if m.RecipeVersion != PortableRecipeVersion {
		// A revision produced by another recipe cannot be re-derived here: this
		// build only knows how to hash its own recipe's stream. The digests
		// above still prove the bytes are intact.
		return fmt.Errorf("artworkkey: manifest recipe %q is not %q", m.RecipeVersion, PortableRecipeVersion)
	}
	if revision := portableRevisionDigest(imageType, mediaType, normalized); revision != m.Revision {
		return fmt.Errorf("artworkkey: stored objects derive revision %s, manifest claims %s", revision, m.Revision)
	}
	return nil
}

// ReadManifest loads and validates the manifest of a revision directory
// together with the objects it describes.
func ReadManifest(ctx context.Context, directory string, read ManifestObjectReader) (Manifest, error) {
	if read == nil {
		return Manifest{}, fmt.Errorf("artworkkey: manifest object reader is required")
	}
	directory = strings.TrimRight(strings.TrimSpace(directory), "/")
	if directory == "" {
		return Manifest{}, fmt.Errorf("artworkkey: revision directory is required")
	}
	data, err := readManifestObject(ctx, read, directory+"/"+ManifestName)
	if err != nil {
		return Manifest{}, err
	}
	m, err := ParseManifest(data)
	if err != nil {
		return Manifest{}, err
	}
	if m.Directory() != directory {
		return Manifest{}, fmt.Errorf("artworkkey: manifest at %s claims directory %s", directory, m.Directory())
	}
	if err := ValidateManifestObjects(ctx, m, read); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// maxManifestObjectBytes bounds what validation will pull into memory for one
// object. Cached variants are dimension-capped well below this.
const maxManifestObjectBytes = 32 * 1024 * 1024

func readManifestObject(ctx context.Context, read ManifestObjectReader, key string) ([]byte, error) {
	body, err := read(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("artworkkey: read %s: %w", key, err)
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, maxManifestObjectBytes+1))
	if err != nil {
		return nil, fmt.Errorf("artworkkey: read %s: %w", key, err)
	}
	if len(data) > maxManifestObjectBytes {
		return nil, fmt.Errorf("artworkkey: object %s exceeds %d bytes", key, maxManifestObjectBytes)
	}
	return data, nil
}
