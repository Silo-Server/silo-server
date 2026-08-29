package artworkkey

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

const PortableIndexPrefix = PortableStorageFormat + "/index"

// AdoptionIndex is a non-authoritative hint from a stable source identity to
// an already materialized portable revision. It deliberately contains no URL,
// path, credential, installation identity, or catalog identity.
type AdoptionIndex struct {
	Fingerprint    string `json:"fingerprint"`
	ImageType      string `json:"image_type"`
	ManifestDigest string `json:"manifest_digest"`
	RecipeVersion  string `json:"recipe_version"`
	TargetRevision string `json:"target_revision"`
}

func SourceFingerprint(identityClass, identity string) (string, bool) {
	identityClass = strings.TrimSpace(identityClass)
	identity = strings.TrimSpace(identity)
	if identityClass == "" || identity == "" {
		return "", false
	}
	if parsed, err := url.Parse(identity); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		// HTTP URLs are resolved transport locations, not stable provider
		// identities. Query strings are especially likely to contain secrets.
		return "", false
	}
	h := sha256.New()
	_, _ = h.Write([]byte(identityClass))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(identity))
	return hex.EncodeToString(h.Sum(nil)), true
}

func ByteSourceFingerprint(identityClass string, data []byte) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	digest := sha256.Sum256(data)
	return SourceFingerprint(identityClass, hex.EncodeToString(digest[:]))
}

// GeneratedSourceFingerprint addresses deterministic generated artwork by the
// generator recipe and the exact ordered input revisions. Callers must omit
// adoption when either half of that identity is unavailable.
func GeneratedSourceFingerprint(generatorVersion string, inputObjectRevisions []string) (string, bool) {
	generatorVersion = strings.TrimSpace(generatorVersion)
	if generatorVersion == "" || len(inputObjectRevisions) == 0 {
		return "", false
	}
	identity := struct {
		GeneratorVersion     string   `json:"generator_version"`
		InputObjectRevisions []string `json:"input_object_revisions"`
	}{GeneratorVersion: generatorVersion, InputObjectRevisions: make([]string, len(inputObjectRevisions))}
	for i, revision := range inputObjectRevisions {
		identity.InputObjectRevisions[i] = strings.TrimSpace(revision)
		if identity.InputObjectRevisions[i] == "" {
			return "", false
		}
	}
	canonical, err := json.Marshal(identity)
	if err != nil {
		return "", false
	}
	return SourceFingerprint("generated", string(canonical))
}

func AdoptionIndexKey(fingerprint, imageType, recipeVersion string) string {
	fingerprint = strings.ToLower(strings.TrimSpace(fingerprint))
	imageType = strings.ToLower(strings.TrimSpace(imageType))
	recipeVersion = strings.TrimSpace(recipeVersion)
	if !isHexDigest(fingerprint) || validatePortableImageType(imageType) != nil || recipeVersion == "" || strings.Contains(recipeVersion, "/") {
		return ""
	}
	return PortableIndexPrefix + "/" + fingerprint[:portableShardLength] + "/" + fingerprint + "/" + imageType + "/" + recipeVersion + ".json"
}

func IsAdoptionIndexKey(key string) bool {
	parts := strings.Split(strings.TrimSpace(key), "/")
	if len(parts) != 7 || strings.Join(parts[:3], "/") != PortableIndexPrefix {
		return false
	}
	fingerprint := parts[4]
	if parts[3] != fingerprint[:min(len(fingerprint), portableShardLength)] || !isHexDigest(fingerprint) {
		return false
	}
	return AdoptionIndexKey(fingerprint, parts[5], strings.TrimSuffix(parts[6], ".json")) == key
}

func NewAdoptionIndex(fingerprint string, manifest Manifest, manifestBytes []byte) (AdoptionIndex, error) {
	if err := manifest.Validate(); err != nil {
		return AdoptionIndex{}, err
	}
	if !isHexDigest(strings.ToLower(strings.TrimSpace(fingerprint))) {
		return AdoptionIndex{}, fmt.Errorf("artworkkey: invalid adoption fingerprint")
	}
	digest := sha256.Sum256(manifestBytes)
	return AdoptionIndex{
		Fingerprint:    strings.ToLower(strings.TrimSpace(fingerprint)),
		ImageType:      manifest.ImageType,
		ManifestDigest: hex.EncodeToString(digest[:]),
		RecipeVersion:  manifest.RecipeVersion,
		TargetRevision: manifest.Revision,
	}, nil
}

func EncodeAdoptionIndex(index AdoptionIndex) ([]byte, error) {
	if err := index.Validate(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(index); err != nil {
		return nil, fmt.Errorf("artworkkey: encode adoption index: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func ParseAdoptionIndex(data []byte) (AdoptionIndex, error) {
	var index AdoptionIndex
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&index); err != nil {
		return AdoptionIndex{}, fmt.Errorf("artworkkey: decode adoption index: %w", err)
	}
	if err := index.Validate(); err != nil {
		return AdoptionIndex{}, err
	}
	canonical, err := EncodeAdoptionIndex(index)
	if err != nil {
		return AdoptionIndex{}, err
	}
	if !bytes.Equal(bytes.TrimRight(data, "\n"), canonical) {
		return AdoptionIndex{}, fmt.Errorf("artworkkey: adoption index is not canonically encoded")
	}
	return index, nil
}

func (i AdoptionIndex) Validate() error {
	if !isHexDigest(i.Fingerprint) || !isHexDigest(i.ManifestDigest) || !isHexDigest(i.TargetRevision) {
		return fmt.Errorf("artworkkey: adoption index contains an invalid digest")
	}
	if err := validatePortableImageType(i.ImageType); err != nil {
		return err
	}
	if i.RecipeVersion != PortableRecipeVersion {
		return fmt.Errorf("artworkkey: unsupported adoption recipe %q", i.RecipeVersion)
	}
	return nil
}
