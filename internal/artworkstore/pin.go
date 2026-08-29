package artworkstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// StorePinSettingKey is the server_settings key holding the artwork store pin.
// It is machine-managed durable state, not an administrator setting: the
// administrator chooses artwork.storage_backend, and this row records what the
// catalog's live artwork keys were actually written against.
const StorePinSettingKey = "artwork.store_pin"

// Backend identifiers. They are the accepted values of artwork.storage_backend
// and the values recorded in the pin (never "auto" — a pin records what auto
// resolved to, not that it was allowed to resolve).
const (
	BackendAuto  = "auto"
	BackendLocal = "local"
	BackendS3    = "s3"
)

// pinFormatVersion versions the pin document so a future reader can recognize
// an incompatible one instead of misreading it.
const pinFormatVersion = 1

// SettingsStore is the durable key/value surface the pin needs. It is a narrow
// interface so this package stays free of database and catalog dependencies;
// *catalog.ServerSettingsRepo and its encrypting decorator both satisfy it.
type SettingsStore interface {
	// Get returns the stored value, or the empty string when no row exists.
	Get(ctx context.Context, key string) (string, error)
	// SetIfAbsent writes only when the key has no value yet, reporting
	// whether this caller won the write.
	SetIfAbsent(ctx context.Context, key, value string) (bool, error)
}

type settingsUpdater interface {
	Set(ctx context.Context, key, value string) error
}

func replacePin(ctx context.Context, settings SettingsStore, pin Pin) error {
	updater, ok := settings.(settingsUpdater)
	if !ok {
		return errors.New("artworkstore: settings store cannot record a recreated store generation")
	}
	encoded, err := encodePin(pin)
	if err != nil {
		return err
	}
	if err := updater.Set(ctx, StorePinSettingKey, encoded); err != nil {
		return fmt.Errorf("artworkstore: updating recreated store generation: %w", err)
	}
	return nil
}

// Pin is the recorded binding between the catalog's artwork keys and one
// physical store. It is written once, by the first successful materialization,
// and from then on every node verifies it at startup.
//
// Generation identifies the physical copy for backends that have one: the
// filesystem and S3 stores both record a random copy-marker id. A reachable
// non-empty store with a different id is a fatal configuration mismatch; an
// authoritatively empty S3 store may receive a new generation and be rebuilt;
// a pinned local store requires the explicit rebuild action.
type Pin struct {
	Version    int    `json:"version"`
	Backend    string `json:"backend"`
	Generation string `json:"generation,omitempty"`
}

// IsZero reports whether the pin is unset.
func (p Pin) IsZero() bool {
	return p.Backend == ""
}

// describe renders the pin for an operator-facing message.
func (p Pin) describe() string {
	if p.Generation == "" {
		return p.Backend
	}
	return p.Backend + " (store " + p.Generation + ")"
}

// PinMismatchError reports that this process resolved a different artwork store
// than the one the catalog's live artwork keys were written against. It is
// always fatal: continuing would either serve 404s from an empty backend or
// materialize a second, divergent copy of every image.
type PinMismatchError struct {
	Recorded Pin
	Resolved Pin
}

// Error names the recovery an operator actually has. The pin's backend is
// immutable — Open rejects a backend change before anything else runs, the
// admin settings API refuses writes to this key, and no task or endpoint
// rebinds it — so the only in-place recovery for a same-backend mismatch is to
// put the pinned copy back under the configured location. Copying the tree
// works for that because the copy carries both markers, and therefore the
// pinned generation, with it.
func (e *PinMismatchError) Error() string {
	if e.Recorded.Backend != e.Resolved.Backend {
		return fmt.Sprintf(
			"artwork storage is pinned to %s but this node resolved %s; "+
				"set artwork.storage_backend=%s to keep the pinned backend. Changing the "+
				"artwork backend of a pinned installation is not supported in place: the pin "+
				"records the backend and nothing rebinds it",
			e.Recorded.describe(), e.Resolved.describe(), e.Recorded.Backend,
		)
	}
	return fmt.Sprintf(
		"artwork storage is pinned to %s but this node opened %s; the configured location "+
			"holds a different store copy — restore the pinned copy, or copy the whole tree "+
			"including its %s and %s markers to the configured location so the copy keeps the "+
			"pinned generation. An object-empty local root can instead be re-initialized with "+
			"POST /api/v1/admin/artwork/rebuild",
		e.Recorded.describe(), e.Resolved.describe(), markerFileName, formatMarkerFileName,
	)
}

// ReadPin loads the recorded pin. A zero Pin with a nil error means the store
// has never been pinned, which is the only state in which "auto" may resolve
// freely.
func ReadPin(ctx context.Context, settings SettingsStore) (Pin, error) {
	if settings == nil {
		return Pin{}, errors.New("artworkstore: settings store is required to read the artwork pin")
	}
	raw, err := settings.Get(ctx, StorePinSettingKey)
	if err != nil {
		return Pin{}, fmt.Errorf("artworkstore: reading the artwork store pin: %w", err)
	}
	return decodePin(raw)
}

// DecodePin parses a stored artwork.store_pin value. An empty value decodes to
// the zero Pin. It exists for callers that validate prospective settings
// against the pin without holding a SettingsStore (see ReadPin otherwise).
func DecodePin(raw string) (Pin, error) {
	return decodePin(raw)
}

func decodePin(raw string) (Pin, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Pin{}, nil
	}
	var pin Pin
	if err := json.Unmarshal([]byte(raw), &pin); err != nil {
		return Pin{}, fmt.Errorf("artworkstore: decoding the artwork store pin: %w", err)
	}
	if pin.Version != pinFormatVersion {
		return Pin{}, fmt.Errorf("artworkstore: unsupported artwork store pin version %d", pin.Version)
	}
	switch pin.Backend {
	case BackendLocal, BackendS3:
	default:
		return Pin{}, fmt.Errorf("artworkstore: artwork store pin names unknown backend %q", pin.Backend)
	}
	return pin, nil
}

func encodePin(pin Pin) (string, error) {
	pin.Version = pinFormatVersion
	encoded, err := json.Marshal(pin)
	if err != nil {
		return "", fmt.Errorf("artworkstore: encoding the artwork store pin: %w", err)
	}
	return string(encoded), nil
}

// VerifyPin compares what this process resolved against what is recorded. An
// unset recorded pin always verifies: nothing has been materialized yet.
func VerifyPin(recorded, resolved Pin) error {
	if recorded.IsZero() {
		return nil
	}
	if recorded.Backend != resolved.Backend || recorded.Generation != resolved.Generation {
		return &PinMismatchError{Recorded: recorded, Resolved: resolved}
	}
	return nil
}

// pinningStore records the pin on the first successful immutable write. Pinning
// on materialization rather than on startup is what lets "auto" keep resolving
// freely on an install that has never stored an artwork object, while making
// the very first stored object bind the catalog to a backend for good.
type pinningStore struct {
	Store
	pin      func() Pin
	settings SettingsStore
	pinned   atomic.Bool
	onPinned func(Pin)
	// pinOnce serializes the pin attempt so concurrent variant writes make
	// one settings round trip, not one per object.
	pinOnce sync.Mutex
}

// newPinningStore wraps store so writes pin it. A nil settings store or an
// already-verified pin returns the store unchanged.
func newPinningStore(store Store, pin func() Pin, settings SettingsStore, alreadyPinned bool, onPinned func(Pin)) Store {
	if store == nil || pin == nil || settings == nil {
		return store
	}
	wrapped := &pinningStore{Store: store, pin: pin, settings: settings, onPinned: onPinned}
	wrapped.pinned.Store(alreadyPinned)
	return wrapped
}

// WriteImmutable writes through, then ensures the pin exists. A failure to pin
// fails the write: immutable writes are idempotent, so the caller's retry
// re-attempts the pin, and materializing artwork that nothing is bound to is
// exactly the silent state this mechanism exists to prevent.
func (s *pinningStore) WriteImmutable(ctx context.Context, key string, data []byte, metadata ObjectMetadata) error {
	if err := s.Store.WriteImmutable(ctx, key, data, metadata); err != nil {
		return err
	}
	return s.ensurePinned(ctx)
}

func (s *pinningStore) ensurePinned(ctx context.Context) error {
	if s.pinned.Load() {
		return nil
	}
	s.pinOnce.Lock()
	defer s.pinOnce.Unlock()
	if s.pinned.Load() {
		return nil
	}

	pin := s.pin()
	encoded, err := encodePin(pin)
	if err != nil {
		return err
	}
	if _, err := s.settings.SetIfAbsent(ctx, StorePinSettingKey, encoded); err != nil {
		return fmt.Errorf("artworkstore: pinning the artwork store: %w", err)
	}
	// Read back unconditionally: another node may have won the write with a
	// different resolution, and that disagreement must surface here rather
	// than after both nodes have materialized into separate stores.
	recorded, err := ReadPin(ctx, s.settings)
	if err != nil {
		return err
	}
	if err := VerifyPin(recorded, pin); err != nil {
		return err
	}
	s.pinned.Store(true)
	if s.onPinned != nil {
		s.onPinned(pin)
	}
	return nil
}

func (s *pinningStore) Root() string {
	if rooted, ok := s.Store.(interface{ Root() string }); ok {
		return rooted.Root()
	}
	return ""
}

func (s *pinningStore) FreeSpaceBytes(ctx context.Context) (int64, error) {
	if capacity, ok := s.Store.(CapacityProvider); ok {
		return capacity.FreeSpaceBytes(ctx)
	}
	return 0, ErrNotFound
}

func (s *pinningStore) CleanTempFiles(ctx context.Context, olderThan time.Duration) (int, error) {
	cleaner, ok := s.Store.(interface {
		CleanTempFiles(context.Context, time.Duration) (int, error)
	})
	if !ok {
		return 0, ErrNotFound
	}
	return cleaner.CleanTempFiles(ctx, olderThan)
}
