package artworkstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// Options selects and configures the canonical artwork store.
type Options struct {
	// Backend is the administrator's artwork.storage_backend value:
	// BackendAuto, BackendLocal, or BackendS3.
	Backend string

	// LocalPath is the filesystem store root. Required whenever the
	// filesystem backend can be selected.
	LocalPath string

	// S3 is the configured public bucket client, or nil when the deployment
	// has none. Its presence is what BackendAuto resolves on.
	S3 S3Client

	// Settings is the durable store holding the backend pin. Required: an
	// unpinnable store would let a later configuration change silently
	// reinterpret live catalog keys against different storage.
	Settings SettingsStore
}

// Handle is an opened, probed, and pin-verified canonical artwork store.
type Handle struct {
	// Store is what the pipeline and lifecycle code write through. It is the
	// selected backend wrapped so the first successful materialization
	// records the pin.
	Store Store

	// Backend is what Options.Backend resolved to: BackendLocal or BackendS3.
	Backend string

	// Generation identifies the physical store copy, or is empty for
	// backends that do not have one. See Pin.
	Generation   string
	generationMu sync.RWMutex
	pinned       bool

	local       *FilesystemStore
	s3          *S3Store
	settings    SettingsStore
	health      *healthTracker
	probeSignal chan struct{}

	// checkMu serializes readiness probes and guards the cached verdict below.
	// /ready is public and unrate-limited, and a filesystem probe is a real
	// write plus fsync on the canonical store, so uncached probing would let an
	// anonymous request loop turn health checking into an I/O amplification
	// attack on the artwork volume.
	checkMu   sync.Mutex
	checkedAt time.Time
	checkErr  error

	zeroPinCheckMu      sync.Mutex
	zeroPinCheckAt      time.Time
	zeroPinCheckArtwork bool
}

func (h *Handle) GenerationID() string {
	if h == nil {
		return ""
	}
	h.generationMu.RLock()
	defer h.generationMu.RUnlock()
	return h.Generation
}

func (h *Handle) setGeneration(generation string) {
	h.generationMu.Lock()
	h.Generation = generation
	h.generationMu.Unlock()
}

// IsPinned reports whether a durable store pin has been recorded. A local
// bootstrap generation exists before first materialization, so GenerationID
// alone cannot answer this question.
func (h *Handle) IsPinned() bool {
	if h == nil {
		return false
	}
	h.generationMu.RLock()
	defer h.generationMu.RUnlock()
	return h.pinned
}

func (h *Handle) markPinned() {
	h.generationMu.Lock()
	h.pinned = true
	h.generationMu.Unlock()
}

func (h *Handle) resolvedPin() Pin {
	if h == nil {
		return Pin{}
	}
	h.generationMu.RLock()
	defer h.generationMu.RUnlock()
	return Pin{Backend: h.Backend, Generation: h.Generation}
}

// replaceGenerationPin keeps the durable generation and the live generation
// observed by write pinning on the same side of the generation lock. A writer
// therefore sees either the old pair or the new pair, never a durable pin that
// disagrees with the handle solely because recovery is between the two writes.
func (h *Handle) replaceGenerationPin(ctx context.Context, generation string) error {
	h.generationMu.Lock()
	defer h.generationMu.Unlock()
	if err := replacePin(ctx, h.settings, Pin{Backend: h.Backend, Generation: generation}); err != nil {
		return err
	}
	h.Generation = generation
	h.pinned = true
	return nil
}

func (h *Handle) recordGenerationPinIfAbsent(ctx context.Context, generation string) (Pin, error) {
	h.generationMu.Lock()
	defer h.generationMu.Unlock()
	resolved := Pin{Backend: h.Backend, Generation: generation}
	encoded, err := encodePin(resolved)
	if err != nil {
		return Pin{}, err
	}
	if _, err := h.settings.SetIfAbsent(ctx, StorePinSettingKey, encoded); err != nil {
		return Pin{}, err
	}
	recorded, err := ReadPin(ctx, h.settings)
	if err != nil {
		return Pin{}, err
	}
	if err := VerifyPin(recorded, resolved); err != nil {
		return Pin{}, err
	}
	h.Generation = generation
	h.pinned = true
	return recorded, nil
}

// checkCacheTTL bounds how often Check re-probes the backing store. Readiness
// consumers tolerate staleness of this order; a swapped mount or unwritable
// root still surfaces within seconds.
const checkCacheTTL = 10 * time.Second

// zeroPinArtworkCheckTTL bounds the full-bucket emptiness proof used only by
// readiness checks before a store is pinned. Open always performs a fresh proof.
const zeroPinArtworkCheckTTL = 5 * time.Minute

// Open selects the artwork backend, proves it is usable, and verifies it
// against the recorded pin.
//
// Configuration and reachable-store identity mismatches are fatal. Operational
// outages are represented by the health state so a transient bucket, mount, or
// permission failure cannot crash-loop an otherwise correctly configured
// server. The health wrapper refuses unsafe writes until probes recover.
func Open(ctx context.Context, opts Options) (*Handle, error) {
	if opts.Settings == nil {
		return nil, errors.New("artworkstore: a settings store is required to open artwork storage")
	}

	recorded, err := ReadPin(ctx, opts.Settings)
	if err != nil {
		return nil, err
	}

	backend, err := resolveBackend(opts.Backend, opts.S3 != nil, recorded.Backend)
	if err != nil {
		return nil, err
	}
	if !recorded.IsZero() && recorded.Backend != backend {
		return nil, &PinMismatchError{
			Recorded: recorded,
			Resolved: Pin{Backend: backend, Generation: recorded.Generation},
		}
	}

	handle := &Handle{
		Backend: backend, settings: opts.Settings,
		health: newHealthTracker(backend, HealthHealthy), probeSignal: make(chan struct{}, 1),
	}
	handle.pinned = !recorded.IsZero()
	switch backend {
	case BackendS3:
		store, err := NewS3Store(opts.S3)
		if err != nil {
			return nil, err
		}
		handle.s3 = store
		handle.Store = store
		if err := store.Probe(ctx); err != nil {
			handle.setGeneration(recorded.Generation)
			handle.health.force(HealthUnavailable)
			break
		}
		// Phase-1 S3 pins named only the backend. That durable pin is the one
		// safe upgrade case where an already-populated bucket may receive the
		// new split sentinels; after this succeeds the pin is immediately
		// upgraded to the random copy generation. Every subsequent open requires
		// the sentinels and exact generation.
		legacyPinnedS3 := !recorded.IsZero() && recorded.Backend == BackendS3 && recorded.Generation == ""
		zeroPinArtwork := false
		if recorded.IsZero() {
			zeroPinArtwork, err = store.hasArtworkObjects(ctx)
			if err != nil {
				handle.setGeneration(recorded.Generation)
				handle.health.force(HealthUnavailable)
				break
			}
		}
		generation, initialized, err := store.ensureSentinels(ctx, legacyPinnedS3 || recorded.IsZero())
		if err != nil {
			if errors.Is(err, ErrStoreIdentity) {
				return nil, err
			}
			handle.setGeneration(recorded.Generation)
			handle.health.force(HealthUnavailable)
			break
		}
		if recorded.IsZero() && zeroPinArtwork {
			recorded, err = handle.recordGenerationPinIfAbsent(ctx, generation)
			if err != nil {
				return nil, fmt.Errorf("artworkstore: adopt existing s3 artwork store: %w", err)
			}
		} else if !recorded.IsZero() && recorded.Generation == "" {
			// Upgrade the phase-1 backend-only S3 pin once the reachable bucket
			// has acquired its copy marker.
			if err := handle.replaceGenerationPin(ctx, generation); err != nil {
				return nil, err
			}
			recorded = Pin{Backend: BackendS3, Generation: generation}
		} else if initialized && !recorded.IsZero() && recorded.Generation != generation {
			// A reachable bucket with no logical artwork objects is
			// authoritatively empty. Rebind its newly initialized generation and
			// let the repair coordinator rebuild it.
			if err := handle.replaceGenerationPin(ctx, generation); err != nil {
				return nil, err
			}
			recorded = Pin{Backend: BackendS3, Generation: generation}
			handle.health.force(HealthEmptyRebuilding)
		} else {
			handle.setGeneration(generation)
		}

	case BackendLocal:
		store, err := NewFilesystemStore(opts.LocalPath)
		if err != nil {
			return nil, err
		}
		handle.local = store
		handle.Store = store
		// A pinned local mount must prove both sentinels before any operation
		// that can create the configured directory or write into it. Otherwise
		// an unavailable path could be replaced by a writable host
		// mountpoint and the startup probe would contaminate the wrong disk.
		if !recorded.IsZero() {
			handle.setGeneration(recorded.Generation)
			store.setPinnedGeneration(recorded.Generation)
			if _, err := os.Stat(store.Root()); err != nil {
				handle.health.force(HealthUnavailable)
				break
			}
			if err := store.HasFormatMarker(ctx); err != nil {
				handle.health.force(HealthWrongMount)
				break
			}
			marker, err := store.ReadMarker(ctx)
			if err != nil {
				handle.health.force(HealthWrongMount)
				break
			}
			if err := VerifyPin(recorded, Pin{Backend: backend, Generation: marker.ID}); err != nil {
				handle.health.force(HealthWrongMount)
				break
			}
			if err := store.Probe(ctx); err != nil {
				handle.health.force(HealthUnavailable)
			}
			break
		}
		if err := store.Probe(ctx); err != nil {
			handle.setGeneration(recorded.Generation)
			handle.health.force(HealthUnavailable)
			break
		}
		if err := store.EnsureFormatMarker(ctx); err != nil {
			handle.setGeneration(recorded.Generation)
			handle.health.force(HealthUnavailable)
			break
		}
		marker, markerErr := store.ReadMarker(ctx)
		switch {
		case markerErr == nil:
			handle.setGeneration(marker.ID)
		case errors.Is(markerErr, ErrNoMarker):
			marker, _, markerErr = store.EnsureMarker(ctx)
			if markerErr != nil {
				handle.setGeneration(recorded.Generation)
				handle.health.force(HealthUnavailable)
				break
			}
			if !recorded.IsZero() {
				handle.health.force(HealthEmptyRebuilding)
				if err := handle.replaceGenerationPin(ctx, marker.ID); err != nil {
					_ = store.Close()
					return nil, err
				}
				recorded = Pin{Backend: BackendLocal, Generation: marker.ID}
			} else {
				handle.setGeneration(marker.ID)
			}
		default:
			handle.setGeneration(recorded.Generation)
			handle.health.force(HealthUnavailable)
		}
	}

	resolved := handle.resolvedPin()
	state, _ := handle.Health()
	if state != HealthUnavailable && state != HealthWrongMount {
		if err := VerifyPin(recorded, resolved); err != nil {
			_ = handle.Close()
			return nil, err
		}
	} else if !recorded.IsZero() && recorded.Backend != resolved.Backend {
		_ = handle.Close()
		return nil, &PinMismatchError{Recorded: recorded, Resolved: resolved}
	}

	handle.Store = &healthStore{
		Store: observeStore(newPinningStore(
			handle.Store, handle.resolvedPin, opts.Settings, !recorded.IsZero(),
			func(pin Pin) {
				handle.markPinned()
				if handle.local != nil {
					handle.local.setPinnedGeneration(pin.Generation)
				}
			},
		), handle.Backend),
		handle: handle,
	}
	return handle, nil
}

// resolveBackend applies the selection rule. "auto" prefers a configured public
// bucket, because an existing S3 install must keep using it, and otherwise
// selects the local filesystem so a deployment without object storage works
// with no choice to make.
//
// Note that auto resolves freely only before the first materialization pins
// the store. Once pinned, auto means "whatever this install's store is": an
// install that materialized against the local store must not flip to S3 merely
// because a bucket was later configured for subtitles or branding — before this
// honored the pin, that flip surfaced as a fatal PinMismatchError at the next
// boot, taking the server down over an unrelated bucket configuration.
func resolveBackend(configured string, s3Configured bool, pinned string) (string, error) {
	switch configured {
	case "", BackendAuto:
		switch pinned {
		case BackendLocal:
			return BackendLocal, nil
		case BackendS3:
			if !s3Configured {
				return "", errors.New(
					"the artwork store is pinned to S3 but no public S3 bucket is configured; " +
						"restore the bucket configuration or migrate the store to local first")
			}
			return BackendS3, nil
		}
		if s3Configured {
			return BackendS3, nil
		}
		return BackendLocal, nil
	case BackendLocal:
		return BackendLocal, nil
	case BackendS3:
		if !s3Configured {
			return "", errors.New(
				"artwork.storage_backend=s3 but no public S3 bucket is configured; " +
					"configure the public bucket or set artwork.storage_backend=local")
		}
		return BackendS3, nil
	default:
		return "", fmt.Errorf("artwork.storage_backend %q is not one of auto, local, s3", configured)
	}
}

// Local returns the filesystem store, or nil when another backend is selected.
// It exists for operations that only the filesystem backend has — temp-file
// sweeps, root reporting, marker checks.
func (h *Handle) Local() *FilesystemStore {
	if h == nil {
		return nil
	}
	return h.local
}

// LocalRoot returns the filesystem store root, or "" for other backends. It is
// deployment configuration for admin status and log lines, never a client URL.
func (h *Handle) LocalRoot() string {
	if h == nil || h.local == nil {
		return ""
	}
	return h.local.Root()
}

// Check re-verifies the store for a readiness probe: the backend is reachable
// and writable, and — on the filesystem — the marker under the configured root
// still identifies the same physical store this process opened. A swapped or
// re-created mount is reported rather than served from.
//
// Verdicts are cached for checkCacheTTL so a hot health-check loop performs a
// bounded number of real store operations. A probe cut short by the caller's
// own context is not a store verdict and is never cached.
func (h *Handle) Check(ctx context.Context) error {
	if h == nil {
		return errors.New("artworkstore: artwork storage is not configured")
	}
	h.checkMu.Lock()
	defer h.checkMu.Unlock()
	if !h.checkedAt.IsZero() && time.Since(h.checkedAt) < checkCacheTTL {
		return h.checkErr
	}
	err := h.check(ctx)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if err != nil {
		h.reportProbeFailure(err)
	} else if state, _ := h.Health(); state != HealthEmptyRebuilding && state != HealthDegraded {
		h.health.observe(HealthHealthy)
	}
	h.checkErr = err
	h.checkedAt = time.Now()
	return err
}

// ProbeNow bypasses the readiness cache and feeds the debounced health state.
// It still serializes on checkMu: an unlocked probe races RebuildEmpty's
// marker/pin rotation and can write the pre-rebuild generation back over the
// fresh one, then misreport the swap as a wrong mount.
func (h *Handle) ProbeNow(ctx context.Context) error {
	if h == nil {
		return ErrBackendUnavailable
	}
	h.checkMu.Lock()
	defer h.checkMu.Unlock()
	err := h.check(ctx)
	if err != nil {
		h.reportProbeFailure(err)
		return err
	}
	state, _ := h.Health()
	if state != HealthEmptyRebuilding && state != HealthDegraded {
		h.health.observe(HealthHealthy)
	}
	return nil
}

// expireCheckCacheForTest discards the cached readiness verdict so tests can
// observe a fresh probe without waiting out checkCacheTTL.
func (h *Handle) expireCheckCacheForTest() {
	h.checkMu.Lock()
	defer h.checkMu.Unlock()
	h.checkedAt = time.Time{}
	h.checkErr = nil
}

func (h *Handle) check(ctx context.Context) error {
	recorded, err := ReadPin(ctx, h.settings)
	if err != nil {
		return err
	}
	if !recorded.IsZero() && recorded.Backend != h.Backend {
		return &PinMismatchError{Recorded: recorded, Resolved: Pin{Backend: h.Backend, Generation: h.GenerationID()}}
	}
	if h.local != nil {
		if err := h.local.ReopenRoot(); err != nil {
			return fmt.Errorf("artworkstore: refresh local store root: %w", err)
		}
		if !recorded.IsZero() {
			h.setGeneration(recorded.Generation)
			h.local.setPinnedGeneration(recorded.Generation)
			if _, err := os.Stat(h.local.Root()); err != nil {
				return ErrBackendUnavailable
			}
			if err := h.local.HasFormatMarker(ctx); err != nil {
				return ErrWrongMount
			}
			marker, err := h.local.ReadMarker(ctx)
			if err != nil {
				return ErrWrongMount
			}
			generation := recorded.Generation
			if marker.ID != generation {
				return &PinMismatchError{
					Recorded: Pin{Backend: h.Backend, Generation: generation},
					Resolved: Pin{Backend: h.Backend, Generation: marker.ID},
				}
			}
			return h.local.Probe(ctx)
		}
		if err := h.local.Probe(ctx); err != nil {
			return err
		}
		if err := h.local.HasFormatMarker(ctx); err != nil {
			if err := h.local.EnsureFormatMarker(ctx); err != nil {
				return err
			}
		}
		marker, err := h.local.ReadMarker(ctx)
		if err != nil {
			if !errors.Is(err, ErrNoMarker) {
				return err
			}
			marker, _, err = h.local.EnsureMarker(ctx)
			if err != nil {
				return err
			}
			h.health.force(HealthEmptyRebuilding)
			if err := h.replaceGenerationPin(ctx, marker.ID); err != nil {
				return err
			}
			h.local.setPinnedGeneration(marker.ID)
			recorded = Pin{Backend: h.Backend, Generation: marker.ID}
		}
		generation := h.GenerationID()
		if !recorded.IsZero() {
			generation = recorded.Generation
			h.setGeneration(generation)
		}
		if marker.ID != generation {
			return &PinMismatchError{
				Recorded: Pin{Backend: h.Backend, Generation: generation},
				Resolved: Pin{Backend: h.Backend, Generation: marker.ID},
			}
		}
		return nil
	}
	if h.s3 != nil {
		if err := h.s3.Probe(ctx); err != nil {
			return err
		}
		zeroPinArtwork := false
		if recorded.IsZero() {
			zeroPinArtwork, err = h.checkZeroPinArtwork(ctx)
			if err != nil {
				return err
			}
		}
		generation, initialized, err := h.s3.ensureSentinels(ctx, recorded.IsZero() || recorded.Generation == "")
		if err != nil {
			return err
		}
		resolved := Pin{Backend: h.Backend, Generation: generation}
		if recorded.IsZero() && zeroPinArtwork {
			recorded, err = h.recordGenerationPinIfAbsent(ctx, generation)
			if err != nil {
				return err
			}
		}
		if !recorded.IsZero() && recorded.Generation == "" {
			if err := h.replaceGenerationPin(ctx, generation); err != nil {
				return err
			}
			recorded = resolved
		}
		currentGeneration := h.GenerationID()
		if !recorded.IsZero() {
			currentGeneration = recorded.Generation
			h.setGeneration(currentGeneration)
		}
		if initialized && generation != currentGeneration {
			if err := h.replaceGenerationPin(ctx, generation); err != nil {
				return err
			}
			h.health.force(HealthEmptyRebuilding)
			return nil
		}
		if generation != currentGeneration {
			return &PinMismatchError{
				Recorded: Pin{Backend: h.Backend, Generation: currentGeneration},
				Resolved: Pin{Backend: h.Backend, Generation: generation},
			}
		}
		return nil
	}
	return errors.New("artworkstore: artwork storage is not configured")
}

// RebuildEmpty explicitly replaces an unavailable empty local store with a
// fresh physical generation. S3 has no local root to recreate and keeps its
// existing authoritative-empty recovery behavior.
//
// recordIntent, when non-nil, runs after the rebuild is validated (local
// backend, empty root) and before the durable marker/pin rotation. Callers use
// it to persist the empty_rebuilding recovery intent at exactly that point: a
// rejected rebuild must not leave durable intent behind (RunRecovery would
// later force a healthy store into bulk recovery), while a crash after the
// intent lands is the harmless resume-a-rebuild state RunRecovery expects.
func (h *Handle) RebuildEmpty(ctx context.Context, recordIntent func(context.Context) error) error {
	if h == nil || h.local == nil {
		return ErrRebuildUnsupported
	}
	h.checkMu.Lock()
	defer h.checkMu.Unlock()
	previousHealth, _ := h.Health()
	h.health.force(HealthUnavailable)
	if err := h.local.prepareEmptyRebuild(ctx); err != nil {
		h.health.force(previousHealth)
		return err
	}
	if recordIntent != nil {
		// The root is already empty and its sentinels are gone, so on failure
		// the store stays unavailable and the ordinary missing-marker recovery
		// converges it; do not restore the pre-rebuild health.
		if err := recordIntent(ctx); err != nil {
			return err
		}
	}
	if err := h.local.Probe(ctx); err != nil {
		return err
	}
	if err := h.local.EnsureFormatMarker(ctx); err != nil {
		return err
	}
	marker, _, err := h.local.EnsureMarker(ctx)
	if err != nil {
		return err
	}
	if err := h.replaceGenerationPin(ctx, marker.ID); err != nil {
		return err
	}
	h.local.setPinnedGeneration(marker.ID)
	h.health.force(HealthEmptyRebuilding)
	h.checkedAt = time.Time{}
	h.checkErr = nil
	return nil
}

func (h *Handle) checkZeroPinArtwork(ctx context.Context) (bool, error) {
	h.zeroPinCheckMu.Lock()
	defer h.zeroPinCheckMu.Unlock()
	if !h.zeroPinCheckAt.IsZero() && time.Since(h.zeroPinCheckAt) < zeroPinArtworkCheckTTL {
		return h.zeroPinCheckArtwork, nil
	}
	hasArtwork, err := h.s3.hasArtworkObjects(ctx)
	if err != nil {
		return false, err
	}
	h.zeroPinCheckArtwork = hasArtwork
	h.zeroPinCheckAt = time.Now()
	return hasArtwork, nil
}

// Close releases backend resources.
func (h *Handle) Close() error {
	if h == nil || h.local == nil {
		return nil
	}
	return h.local.Close()
}
