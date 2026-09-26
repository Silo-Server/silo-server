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
)

// InstanceStateStore persists a plugin instance's private state. The host
// scope (api host or one proxy node) is fixed by the implementation, so a
// plugin only ever names installation-relative keys.
type InstanceStateStore interface {
	ReadInstanceState(ctx context.Context, installationID int, key string) (value []byte, found bool, err error)
	WriteInstanceState(ctx context.Context, installationID int, key string, value []byte) error
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
