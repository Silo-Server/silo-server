package pluginhost

import (
	"context"
	"errors"
)

// Limits of the per-instance state store exposed through
// RuntimeHost.ReadInstanceState / WriteInstanceState. They size the store for
// tsnet's ipn.StateStore (node keys, profile state) and stop a plugin from
// using it as general storage. The store in internal/plugins enforces the
// same limits at the row.
const (
	InstanceStateMaxKeyBytes   = 256
	InstanceStateMaxValueBytes = 256 << 10
	InstanceStateMaxKeys       = 256
)

var (
	// ErrInstanceStateKeyTooLong reports a key over InstanceStateMaxKeyBytes.
	ErrInstanceStateKeyTooLong = errors.New("instance state key exceeds 256 bytes")
	// ErrInstanceStateValueTooLarge reports a value over InstanceStateMaxValueBytes.
	ErrInstanceStateValueTooLarge = errors.New("instance state value exceeds 256 KiB")
	// ErrInstanceStateTooManyKeys reports a write that would put a scope over
	// InstanceStateMaxKeys.
	ErrInstanceStateTooManyKeys = errors.New("instance state scope holds the maximum of 256 keys")
	// ErrInstanceStateUnavailable reports an instance that has no state
	// scope: config test-runs use negative installation ids and get none.
	ErrInstanceStateUnavailable = errors.New("instance state is not available for this plugin instance")
	// ErrInstanceStateVersionConflict reports a conditional write whose
	// expected version no longer matches the key: another write got there
	// first, so the caller must read again.
	ErrInstanceStateVersionConflict = errors.New("instance state key is not at the expected version")
)

// InstanceStateStore persists a plugin instance's private state. The host
// scope (api host or one proxy node) is fixed by the implementation, so a
// plugin only ever names installation-relative keys.
//
// Every key carries a version: 1 after its first write, one more after each
// later write. Several API servers run their own process of a plugin against
// one shared scope, so a read-modify-write passes the version it read as
// expectedVersion to detect a concurrent write.
type InstanceStateStore interface {
	// ReadInstanceState returns the stored value and its version; version 0
	// means no value is stored under key.
	ReadInstanceState(ctx context.Context, installationID int, key string) (value []byte, version int64, err error)
	// WriteInstanceState stores value and returns its new version. A nil
	// expectedVersion writes unconditionally. Otherwise the write lands only
	// while the key is at *expectedVersion (0: the key must not exist) and
	// fails with ErrInstanceStateVersionConflict, changing nothing, if not.
	WriteInstanceState(ctx context.Context, installationID int, key string, value []byte, expectedVersion *int64) (version int64, err error)
}

// ValidateInstanceStateKey applies the key limit shared by the RPC layer and
// the store.
func ValidateInstanceStateKey(key string) error {
	if key == "" {
		return errors.New("instance state key is required")
	}
	if len(key) > InstanceStateMaxKeyBytes {
		return ErrInstanceStateKeyTooLong
	}
	return nil
}
