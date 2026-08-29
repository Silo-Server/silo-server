// Package artworkvariant selects a readable stored variant without confusing
// a ladder upgrade with object loss.
package artworkvariant

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
)

const (
	manifestReadLimit = 1 << 20
	cacheEntryLimit   = 4096
	presentTTL        = 24 * time.Hour
	absentTTL         = 15 * time.Minute
)

type objectOpener interface {
	Open(ctx context.Context, key string) (*artworkstore.Object, error)
}

type objectStatter interface {
	Stat(ctx context.Context, key string) (artworkstore.ObjectInfo, error)
}

type existenceEntry struct {
	exists  bool
	expires time.Time
}

// Selector caches immutable portable manifests and bounded legacy existence
// probes. Portable selection never probes individual variants: the manifest is
// the revision's complete inventory. Legacy keys have no manifest, so only the
// newly-added wide rung uses the short-lived probe cache.
type Selector struct {
	store objectOpener
	now   func() time.Time

	mu        sync.Mutex
	manifests map[string]artworkkey.Manifest
	exists    map[string]existenceEntry
}

func New(store objectOpener) *Selector {
	if store == nil {
		return nil
	}
	return &Selector{
		store: store, now: time.Now,
		manifests: make(map[string]artworkkey.Manifest),
		exists:    make(map[string]existenceEntry),
	}
}

// Select returns the key to serve. selectedPath is the catalog's selected
// revision (normally its original key), while requested is the variant carried
// by the target capability.
func (s *Selector) Select(ctx context.Context, selectedPath, imageType, requested string) (string, error) {
	if s == nil || s.store == nil || selectedPath == "" || requested == "" {
		return artworkkey.Variant(selectedPath, requested), nil
	}
	info, portable := artworkkey.ParsePortableKey(selectedPath)
	if portable {
		manifest, err := s.portableManifest(ctx, info.Directory)
		if err != nil {
			return "", err
		}
		if manifest.Revision != info.Revision || manifest.ImageType != info.ImageType {
			return "", artworkstore.ErrContentMismatch
		}
		variant, ok := manifest.SelectVariant(requested)
		if !ok {
			return info.Directory + "/" + requested + info.Ext, nil
		}
		return info.Directory + "/" + variant.Filename, nil
	}

	requestedKey := artworkkey.Variant(selectedPath, requested)
	if !artworkkey.IsLadderExtensionVariant(imageType, requested) {
		return requestedKey, nil
	}
	statter, ok := s.store.(objectStatter)
	if !ok {
		return requestedKey, nil
	}
	for _, candidate := range legacyCandidates(selectedPath, imageType, requested) {
		exists, err := s.objectExists(ctx, statter, candidate)
		if err != nil {
			return "", err
		}
		if exists {
			return candidate, nil
		}
	}
	// The original predates every ladder and is the terminal unchecked choice.
	return artworkkey.Variant(selectedPath, artworkkey.OriginalVariant), nil
}

func legacyCandidates(selectedPath, imageType, requested string) []string {
	wanted, ok := variantWidth(requested)
	if !ok {
		return []string{artworkkey.Variant(selectedPath, requested)}
	}
	keys := []string{artworkkey.Variant(selectedPath, requested)}
	for _, width := range artworkkey.VariantWidths(imageType) {
		if width < wanted {
			keys = append(keys, artworkkey.Variant(selectedPath, fmt.Sprintf("w%d", width)))
		}
	}
	return keys
}

func variantWidth(variant string) (int, bool) {
	var width int
	if _, err := fmt.Sscanf(variant, "w%d", &width); err != nil || width <= 0 || variant != fmt.Sprintf("w%d", width) {
		return 0, false
	}
	return width, true
}

func (s *Selector) portableManifest(ctx context.Context, directory string) (artworkkey.Manifest, error) {
	s.mu.Lock()
	manifest, ok := s.manifests[directory]
	s.mu.Unlock()
	if ok {
		return manifest, nil
	}
	object, err := s.store.Open(ctx, directory+"/"+artworkkey.ManifestName)
	if err != nil {
		return artworkkey.Manifest{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(object.Body, manifestReadLimit+1))
	closeErr := object.Close()
	if readErr != nil {
		return artworkkey.Manifest{}, readErr
	}
	if closeErr != nil {
		return artworkkey.Manifest{}, closeErr
	}
	if len(data) > manifestReadLimit {
		return artworkkey.Manifest{}, artworkstore.ErrContentMismatch
	}
	manifest, err = artworkkey.ParseManifest(data)
	if err != nil || manifest.Directory() != directory {
		return artworkkey.Manifest{}, artworkstore.ErrContentMismatch
	}
	s.mu.Lock()
	if len(s.manifests) >= cacheEntryLimit {
		for key := range s.manifests {
			delete(s.manifests, key)
			break
		}
	}
	s.manifests[directory] = manifest
	s.mu.Unlock()
	return manifest, nil
}

func (s *Selector) objectExists(ctx context.Context, store objectStatter, key string) (bool, error) {
	now := s.now()
	s.mu.Lock()
	entry, ok := s.exists[key]
	if ok && entry.expires.After(now) {
		s.mu.Unlock()
		return entry.exists, nil
	}
	if ok {
		delete(s.exists, key)
	}
	s.mu.Unlock()

	_, err := store.Stat(ctx, key)
	exists := err == nil
	if err != nil && !errors.Is(err, artworkstore.ErrNotFound) {
		return false, err
	}
	ttl := absentTTL
	if exists {
		ttl = presentTTL
	}
	s.mu.Lock()
	if len(s.exists) >= cacheEntryLimit {
		for cachedKey := range s.exists {
			delete(s.exists, cachedKey)
			break
		}
	}
	s.exists[key] = existenceEntry{exists: exists, expires: now.Add(ttl)}
	s.mu.Unlock()
	return exists, nil
}
