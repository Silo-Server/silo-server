package artworkadopt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkmetrics"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
)

const readLimit = 32 * 1024 * 1024

type Store interface {
	Open(context.Context, string) (*artworkstore.Object, error)
	Stat(context.Context, string) (artworkstore.ObjectInfo, error)
	WriteImmutable(context.Context, string, []byte, artworkstore.ObjectMetadata) error
	DeleteObjects(context.Context, []string) (int, error)
}

type Result struct {
	Index        artworkkey.AdoptionIndex
	Manifest     artworkkey.Manifest
	ManifestJSON []byte
	OriginalKey  string
	OriginalData []byte
	Objects      []artworkstore.ObjectInfo
}

// Try validates an adoption hint and every object it names. A missing or
// invalid hint is a cache miss, never an error that blocks materialization.
func Try(ctx context.Context, store Store, fingerprint, imageType string) (*Result, bool) {
	if store == nil || fingerprint == "" {
		return nil, false
	}
	indexKey := artworkkey.AdoptionIndexKey(fingerprint, imageType, artworkkey.PortableRecipeVersion)
	indexBytes, err := read(ctx, store, indexKey)
	if err != nil {
		return nil, false
	}
	index, err := artworkkey.ParseAdoptionIndex(indexBytes)
	if err != nil || index.Fingerprint != fingerprint || index.ImageType != imageType {
		artworkmetrics.ManifestFailure("adoption_index")
		return nil, false
	}
	directory := artworkkey.PortableDirectory(index.ImageType, index.TargetRevision)
	manifestBytes, err := read(ctx, store, directory+"/"+artworkkey.ManifestName)
	if err != nil {
		return nil, false
	}
	digest := sha256.Sum256(manifestBytes)
	if hex.EncodeToString(digest[:]) != index.ManifestDigest {
		artworkmetrics.ManifestFailure("adoption_manifest_digest")
		return nil, false
	}
	manifest, err := artworkkey.ParseManifest(manifestBytes)
	if err != nil || manifest.Revision != index.TargetRevision || manifest.ImageType != imageType {
		artworkmetrics.ManifestFailure("adoption_manifest")
		return nil, false
	}
	reader := func(ctx context.Context, key string) (io.ReadCloser, error) {
		object, err := store.Open(ctx, key)
		if err != nil {
			return nil, err
		}
		return object.Body, nil
	}
	if err := artworkkey.ValidateManifestObjects(ctx, manifest, reader); err != nil {
		artworkmetrics.ManifestFailure("adoption_objects")
		return nil, false
	}
	objects := make([]artworkstore.ObjectInfo, 0, len(manifest.ObjectKeys()))
	for _, key := range manifest.ObjectKeys() {
		info, err := store.Stat(ctx, key)
		if err != nil {
			return nil, false
		}
		info.Key = key
		objects = append(objects, info)
	}
	var originalKey string
	for _, variant := range manifest.Variants {
		if variant.Name == artworkkey.OriginalVariant {
			originalKey = directory + "/" + variant.Filename
			break
		}
	}
	originalData, err := read(ctx, store, originalKey)
	if err != nil {
		return nil, false
	}
	return &Result{Index: index, Manifest: manifest, ManifestJSON: manifestBytes, OriginalKey: originalKey, OriginalData: originalData, Objects: objects}, true
}

func WriteIndex(ctx context.Context, store Store, fingerprint string, manifest artworkkey.Manifest, manifestJSON []byte) error {
	if store == nil || fingerprint == "" {
		return nil
	}
	index, err := artworkkey.NewAdoptionIndex(fingerprint, manifest, manifestJSON)
	if err != nil {
		return err
	}
	data, err := artworkkey.EncodeAdoptionIndex(index)
	if err != nil {
		return err
	}
	key := artworkkey.AdoptionIndexKey(fingerprint, manifest.ImageType, manifest.RecipeVersion)
	err = store.WriteImmutable(ctx, key, data, artworkstore.ObjectMetadata{MediaType: "application/json"})
	if errors.Is(err, artworkstore.ErrContentMismatch) {
		// The target for a stable source may rotate. Hints are deliberately
		// mutable and non-authoritative: a brief missing/racing hint only falls
		// back to ordinary materialization, while every reader still validates
		// the target manifest and bytes.
		if _, deleteErr := store.DeleteObjects(ctx, []string{key}); deleteErr != nil {
			return fmt.Errorf("artwork adoption: replace %s: %w", key, deleteErr)
		}
		err = store.WriteImmutable(ctx, key, data, artworkstore.ObjectMetadata{MediaType: "application/json"})
	}
	if err != nil {
		return fmt.Errorf("artwork adoption: write %s: %w", key, err)
	}
	return nil
}

func read(ctx context.Context, store Store, key string) ([]byte, error) {
	if strings.TrimSpace(key) == "" {
		return nil, artworkstore.ErrNotFound
	}
	object, err := store.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = object.Close() }()
	data, err := io.ReadAll(io.LimitReader(object.Body, readLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > readLimit {
		return nil, fmt.Errorf("artwork adoption: object exceeds %d bytes", readLimit)
	}
	return data, nil
}
