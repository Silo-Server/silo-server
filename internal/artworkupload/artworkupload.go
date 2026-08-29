// Package artworkupload materializes administrator- and user-supplied images
// into the canonical artwork store.
//
// It is the raw-upload counterpart to internal/imagecache: the pipeline there
// downloads provider artwork, this one takes bytes that arrived on an HTTP
// request. Both end in the same place — an immutable, content-addressed
// artwork/v1 revision with a manifest — so a library poster, a collection
// backdrop, and a TMDB still are stored, delivered, reconciled, and collected
// by exactly one mechanism.
//
// What an upload does *not* share with provider artwork is a source it can be
// fetched from again. Losing an uploaded object means an administrator has to
// upload it again, which is why upload revisions are protected from the
// reconciler's provider-reset path and from safe purge.
package artworkupload

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Silo-Server/silo-server/internal/artworkadopt"
	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkmetrics"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/imageutil"
)

// Store is the subset of artworkstore.Store this package needs. Every backend
// satisfies it, so upload handlers stay free of buckets, roots, and prefixes.
type Store interface {
	WriteImmutable(ctx context.Context, key string, data []byte, meta artworkstore.ObjectMetadata) error
	Matches(ctx context.Context, key string, data []byte) (bool, error)
}

// RevisionTracker persists the exact object manifest for an immutable revision
// before any object is written, so a crash mid-write leaves reclaimable objects
// rather than orphans. It is satisfied by catalog.ArtworkRevisionTracker.
type RevisionTracker interface {
	TrackArtworkRevision(ctx context.Context, originalPath, imageType string, objectKeys []string) error
	RecordArtworkRevision(ctx context.Context, originalPath, sourceClass string, objects []artworkstore.ObjectInfo) error
}

// UntrackedRevisionRetainer disarms an existing candidate when a user-store
// surface that PostgreSQL cannot inspect publishes it. The row remains
// inventory, but GC must never infer that the invisible reference is absent.
type UntrackedRevisionRetainer interface {
	RetainUntrackedArtworkRevision(ctx context.Context, originalPath string) error
}

var (
	// ErrStorageUnavailable reports that no artwork store is configured, so
	// uploads cannot be accepted. Handlers translate it into 503.
	ErrStorageUnavailable = errors.New("artworkupload: artwork storage is not configured")

	// ErrInvalidImage reports bytes the image pipeline could not decode or
	// re-encode. It separates "the upload is bad" from "storage is bad", which
	// is the difference between a 400 the uploader can act on and a 500 they
	// cannot.
	ErrInvalidImage = errors.New("artworkupload: image could not be processed")
)

// Materializer writes uploaded images into the artwork store.
type Materializer struct {
	store   Store
	tracker RevisionTracker
}

// NewMaterializer returns a Materializer over the canonical artwork store. A nil
// store yields a nil Materializer, so callers gate on Available rather than
// carrying a store that cannot write.
func NewMaterializer(store Store) *Materializer {
	if store == nil {
		return nil
	}
	return &Materializer{store: store}
}

// SetRevisionTracker wires durable revision lifecycle tracking. Only requests
// that opt in with Request.Track use it; see that field for why.
func (m *Materializer) SetRevisionTracker(tracker RevisionTracker) {
	if m != nil {
		m.tracker = tracker
	}
}

// Available reports whether uploads can be stored.
func (m *Materializer) Available() bool { return m != nil && m.store != nil }

// Request describes one image to materialize.
type Request struct {
	// ImageType is the upload artwork type; see artworkkey.UploadImageTypes.
	// It selects the variant ladder and namespaces the stored revision.
	ImageType string

	// Data is the uploaded image, exactly as received.
	Data []byte

	// Square generates center-cropped square variants instead of
	// width-preserving ones. Avatars use it.
	Square bool

	// Track registers the revision with artwork garbage collection before the
	// first object is written.
	//
	// Only set this for a surface whose owning column the collector's reference
	// union can actually see (internal/metadata.artworkReferenceSurfaces).
	// Tracking a revision the union cannot find schedules a live image for
	// deletion once its grace period expires, so the default is deliberately
	// "do not track": an untracked upload leaks a few objects, a wrongly
	// tracked one loses them.
	Track bool
}

// Result describes a materialized revision.
type Result struct {
	// ImageType is the normalized upload artwork type.
	ImageType string
	// Revision is the content digest shared by every variant.
	Revision string
	// Directory is the revision directory holding every object.
	Directory string
	// OriginalKey is the logical key the owning row is pointed at.
	OriginalKey string
	// ManifestKey is the completeness marker, written last.
	ManifestKey string
	// VariantKeys maps each ladder name to its logical key.
	VariantKeys map[string]string
	// Ext is the output extension including its dot.
	Ext string
	// MediaType is the output media type of every variant.
	MediaType string
	// Thumbhash is the base64 placeholder for the uploaded image.
	Thumbhash string
	// WrittenObjects and ExistingObjects split the revision's objects into
	// those this call stored and those an identical earlier upload already had.
	WrittenObjects  int
	ExistingObjects int
}

// Materialize generates the variant ladder for an uploaded image, addresses it
// by content, and stores every object plus the manifest.
//
// The write order matches the provider pipeline and is load-bearing: produce
// every variant, register the complete object set (when tracking), write the
// image objects, and only then write manifest.json. A revision directory that
// has a manifest is complete.
//
// The caller points its owning row at Result.OriginalKey after this returns.
// Until it does, the revision is unreferenced and — if it was tracked — will be
// collected after the grace period, which is what makes a failed upload
// self-cleaning.
func (m *Materializer) Materialize(ctx context.Context, req Request) (*Result, error) {
	if !m.Available() {
		return nil, ErrStorageUnavailable
	}
	if len(req.Data) == 0 {
		return nil, fmt.Errorf("%w: no image data", ErrInvalidImage)
	}
	if !artworkkey.IsUploadImageType(req.ImageType) {
		return nil, fmt.Errorf("artworkupload: %q is not an upload image type", req.ImageType)
	}
	fingerprint, _ := artworkkey.ByteSourceFingerprint("upload", req.Data)
	if adopted, ok, err := m.tryAdopt(ctx, req, fingerprint); err != nil {
		return nil, err
	} else if ok {
		return adopted, nil
	}

	generate := imageutil.GenerateVariants
	if req.Square {
		generate = imageutil.GenerateSquareVariants
	}
	variants, err := generate(req.Data, artworkkey.VariantWidths(req.ImageType))
	if err != nil {
		return nil, fmt.Errorf("%w: generate variants: %w", ErrInvalidImage, err)
	}

	revision, err := buildRevision(req.ImageType, variants)
	if err != nil {
		return nil, err
	}
	thumbhash, err := imageutil.Thumbhash(req.Data)
	if err != nil {
		return nil, fmt.Errorf("%w: thumbhash: %w", ErrInvalidImage, err)
	}

	if req.Track {
		if err := m.track(ctx, revision); err != nil {
			return nil, err
		}
	}

	result := &Result{
		ImageType:   revision.ImageType,
		Revision:    revision.Revision,
		Directory:   revision.Directory,
		OriginalKey: revision.OriginalKey,
		ManifestKey: revision.ManifestKey,
		VariantKeys: revision.VariantKeys,
		Ext:         revision.Ext,
		MediaType:   revision.MediaType,
		Thumbhash:   thumbhash,
	}
	for _, variant := range variants.Variants {
		key := revision.VariantKeys[variant.Key]
		written, err := m.writeObject(ctx, key, variant.Data, revision.MediaType)
		if err != nil {
			return nil, err
		}
		if written {
			result.WrittenObjects++
		} else {
			result.ExistingObjects++
		}
	}
	// The manifest goes last, once every image object is durable, so its
	// presence means the revision is complete. Re-uploading identical bytes
	// therefore also heals a revision whose marker was lost, without touching
	// the image objects.
	if _, err := m.writeObject(ctx, revision.ManifestKey, revision.ManifestJSON,
		artworkstore.MediaTypeForKey(artworkkey.ManifestName)); err != nil {
		return nil, err
	}
	if !req.Track {
		if err := m.retainUntracked(ctx, revision.OriginalKey); err != nil {
			return nil, err
		}
	}
	if req.Track {
		if err := m.record(ctx, revision, variants); err != nil {
			return nil, err
		}
	}
	if store, ok := m.store.(artworkadopt.Store); ok {
		if err := artworkadopt.WriteIndex(ctx, store, fingerprint, revision.Manifest, revision.ManifestJSON); err != nil {
			slog.WarnContext(ctx, "artwork upload adoption index write failed", "component", "artwork", "error", err)
		}
	}
	artworkmetrics.Materialization("upload", "materialized")
	return result, nil
}

func (m *Materializer) tryAdopt(ctx context.Context, req Request, fingerprint string) (*Result, bool, error) {
	store, ok := m.store.(artworkadopt.Store)
	if !ok {
		return nil, false, nil
	}
	adopted, ok := artworkadopt.Try(ctx, store, fingerprint, req.ImageType)
	if !ok {
		return nil, false, nil
	}
	if req.Track && m.tracker != nil {
		if err := m.tracker.TrackArtworkRevision(ctx, adopted.OriginalKey, req.ImageType, adopted.Manifest.ObjectKeys()); err != nil {
			return nil, false, fmt.Errorf("artworkupload: track adopted revision: %w", err)
		}
		if err := m.tracker.RecordArtworkRevision(ctx, adopted.OriginalKey, "upload", adopted.Objects); err != nil {
			return nil, false, fmt.Errorf("artworkupload: record adopted revision: %w", err)
		}
	}
	thumbhash, thumbhashErr := imageutil.Thumbhash(adopted.OriginalData)
	if thumbhashErr != nil {
		return nil, false, fmt.Errorf("artworkupload: thumbhash adopted revision: %w", thumbhashErr)
	}
	if !req.Track && m.tracker != nil {
		if err := m.retainUntracked(ctx, adopted.OriginalKey); err != nil {
			return nil, false, err
		}
	}
	artworkmetrics.Materialization("upload", "adopted")
	variantKeys := make(map[string]string, len(adopted.Manifest.Variants))
	ext := ""
	for _, variant := range adopted.Manifest.Variants {
		variantKeys[variant.Name] = adopted.Manifest.Directory() + "/" + variant.Filename
		if variant.Name == artworkkey.OriginalVariant {
			if dot := strings.LastIndex(variant.Filename, "."); dot >= 0 {
				ext = variant.Filename[dot:]
			}
		}
	}
	return &Result{
		ImageType: adopted.Manifest.ImageType, Revision: adopted.Manifest.Revision,
		Directory: adopted.Manifest.Directory(), OriginalKey: adopted.OriginalKey,
		ManifestKey: adopted.Manifest.Directory() + "/" + artworkkey.ManifestName,
		VariantKeys: variantKeys, Ext: ext, MediaType: adopted.Manifest.MediaType,
		Thumbhash: thumbhash, ExistingObjects: len(adopted.Objects),
	}, true, nil
}

func (m *Materializer) retainUntracked(ctx context.Context, originalPath string) error {
	if m == nil || m.tracker == nil {
		return nil
	}
	retainer, ok := m.tracker.(UntrackedRevisionRetainer)
	if !ok {
		return fmt.Errorf("artworkupload: revision tracker cannot retain untracked revisions")
	}
	if err := retainer.RetainUntrackedArtworkRevision(ctx, originalPath); err != nil {
		return fmt.Errorf("artworkupload: retain untracked revision: %w", err)
	}
	return nil
}

// buildRevision addresses the produced variant set: the revision digest, every
// logical key, and the canonical manifest bytes.
func buildRevision(imageType string, variants *imageutil.VariantResult) (*artworkkey.PortableRevision, error) {
	bytes := make([]artworkkey.VariantBytes, 0, len(variants.Variants))
	for _, variant := range variants.Variants {
		bytes = append(bytes, artworkkey.VariantBytes{Name: variant.Key, Data: variant.Data})
	}
	revision, err := artworkkey.BuildPortableRevision(artworkkey.RevisionInput{
		ImageType: imageType,
		MediaType: artworkstore.MediaTypeForKey(artworkkey.OriginalVariant + variants.Ext),
		Ext:       variants.Ext,
		Variants:  bytes,
	})
	if err != nil {
		return nil, fmt.Errorf("artworkupload: address revision: %w", err)
	}
	return revision, nil
}

// writeObject stores one immutable object, skipping the write when the store
// already holds exactly these bytes. It reports whether bytes were written.
func (m *Materializer) writeObject(ctx context.Context, key string, data []byte, mediaType string) (bool, error) {
	matches, err := m.store.Matches(ctx, key, data)
	if err != nil {
		return false, fmt.Errorf("artworkupload: check existing %s: %w", key, err)
	}
	if matches {
		artworkmetrics.Variant("matched", int64(len(data)))
		return false, nil
	}
	if err := m.store.WriteImmutable(ctx, key, data, artworkstore.ObjectMetadata{MediaType: mediaType}); err != nil {
		return false, fmt.Errorf("artworkupload: write %s: %w", key, err)
	}
	artworkmetrics.Variant("written", int64(len(data)))
	return true, nil
}

// track registers every object the revision will occupy, manifest included,
// before the first write.
func (m *Materializer) track(ctx context.Context, revision *artworkkey.PortableRevision) error {
	if m.tracker == nil {
		// Tracking was requested but no tracker is wired (a node without a
		// catalog database). The upload still succeeds; its predecessor simply
		// waits for a node that can collect it.
		slog.DebugContext(ctx, "artwork upload revision tracking is not configured",
			"component", "artwork", "image_type", revision.ImageType)
		return nil
	}
	if err := m.tracker.TrackArtworkRevision(ctx, revision.OriginalKey, revision.ImageType, revision.ObjectKeys()); err != nil {
		return fmt.Errorf("artworkupload: track artwork revision: %w", err)
	}
	return nil
}

func (m *Materializer) record(ctx context.Context, revision *artworkkey.PortableRevision, variants *imageutil.VariantResult) error {
	if m.tracker == nil {
		return nil
	}
	objects := make([]artworkstore.ObjectInfo, 0, len(variants.Variants)+1)
	for _, variant := range variants.Variants {
		objects = append(objects, artworkstore.ObjectInfo{
			Key:       revision.VariantKeys[variant.Key],
			SizeBytes: int64(len(variant.Data)),
			MediaType: revision.MediaType,
		})
	}
	objects = append(objects, artworkstore.ObjectInfo{
		Key:       revision.ManifestKey,
		SizeBytes: int64(len(revision.ManifestJSON)),
		MediaType: artworkstore.MediaTypeForKey(revision.ManifestKey),
	})
	if err := m.tracker.RecordArtworkRevision(ctx, revision.OriginalKey, "upload", objects); err != nil {
		return fmt.Errorf("artworkupload: record artwork inventory: %w", err)
	}
	return nil
}
