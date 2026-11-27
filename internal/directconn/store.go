package directconn

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"time"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
	"github.com/redis/go-redis/v9"
)

// coordinationHostLabel is the label of the API host in the coordination store.
const coordinationHostLabel = "api"

// CoordinationStore is the coordination store plus a write that names the key it
// replaced and two compare-before-write operations on one device entry. The two
// compare-before-write operations act only while the live entry still holds
// exactly the node key and policy the caller names, so a request never changes
// an entry that another request has replaced, renewed or audited since. The
// enrollment audit and the withdrawals use them; see Coordination.Enroll.
type CoordinationStore interface {
	server.Store
	// PutDeviceReplacing is PutDevice, and returns the node key its write replaced
	// instead of whether it replaced one. The read of that key and the write are
	// one atomic step, so a concurrent enrollment's key is never missed. The key
	// is zero when the device had no live entry or kept its key.
	PutDeviceReplacing(ctx context.Context, d server.Device, maxPerAccount int, now time.Time) (protocol.NodeKey, error)
	// SwapDevicePolicy replaces the policy of ref's live entry with policy when
	// the entry holds nodeKey and expected. It reports whether it wrote; the
	// lease, the indices and the registration stay as they are.
	SwapDevicePolicy(ctx context.Context, ref server.DeviceRef, nodeKey protocol.NodeKey, expected, policy []byte, now time.Time) (bool, error)
	// DeleteDeviceIf removes ref's live entry, its indices and its confirmations
	// when the entry holds nodeKey and policy. It reports whether it removed.
	DeleteDeviceIf(ctx context.Context, ref server.DeviceRef, nodeKey protocol.NodeKey, policy []byte, now time.Time) (bool, error)
}

// NewCoordinationStore returns the coordination store: the Redis adapter when
// client is non-nil, else the module memory store. A memory store is private to
// one process, which is correct only without Redis.
//
// fence is this process's presence ID, the value the engine lease holds while
// this process runs the engine. The Redis adapter writes host entries and
// confirmations only while the lease holds fence; the memory store has no lease
// and ignores it.
func NewCoordinationStore(client *redis.Client, fence string) CoordinationStore {
	if client == nil {
		return newMemoryStore()
	}
	return newRedisStore(client, []string{coordinationHostLabel}, fence)
}

// memoryStore adds the conditional operations to the module memory store. Its
// lock covers every write that changes an entry's node key or policy, so a
// conditional operation's read and write see no such write between them. Other
// operations go to the module store, which is safe for concurrent use.
type memoryStore struct {
	server.Store
	mu sync.Mutex
}

var _ CoordinationStore = (*memoryStore)(nil)

func newMemoryStore() *memoryStore {
	return &memoryStore{Store: server.NewMemoryStore()}
}

func (m *memoryStore) PutDevice(ctx context.Context, d server.Device, maxPerAccount int, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Store.PutDevice(ctx, d, maxPerAccount, now)
}

func (m *memoryStore) PutDeviceReplacing(ctx context.Context, d server.Device, maxPerAccount int, now time.Time) (protocol.NodeKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, err := m.Device(ctx, d.Ref, now)
	if err != nil && !errors.Is(err, server.ErrNotFound) {
		return protocol.NodeKey{}, err
	}
	replaced, err := m.Store.PutDevice(ctx, d, maxPerAccount, now)
	if err != nil || !replaced {
		return protocol.NodeKey{}, err
	}
	return stored.NodeKey, nil
}

func (m *memoryStore) DeleteDevice(ctx context.Context, ref server.DeviceRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Store.DeleteDevice(ctx, ref)
}

// matching returns ref's live entry when it holds nodeKey and policy.
func (m *memoryStore) matching(ctx context.Context, ref server.DeviceRef, nodeKey protocol.NodeKey, policy []byte, now time.Time) (server.Device, bool, error) {
	d, err := m.Device(ctx, ref, now)
	if errors.Is(err, server.ErrNotFound) {
		return server.Device{}, false, nil
	}
	if err != nil {
		return server.Device{}, false, err
	}
	return d, d.NodeKey == nodeKey && bytes.Equal(d.Policy, policy), nil
}

func (m *memoryStore) SwapDevicePolicy(ctx context.Context, ref server.DeviceRef, nodeKey protocol.NodeKey, expected, policy []byte, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok, err := m.matching(ctx, ref, nodeKey, expected, now)
	if err != nil || !ok {
		return false, err
	}
	// Writing the entry back with the same node key keeps its registration, and
	// an entry that already exists is never refused by the account cap.
	d.Policy = policy
	if _, err := m.Store.PutDevice(ctx, d, maxDirectDevicesPerAccount, now); err != nil {
		return false, err
	}
	return true, nil
}

func (m *memoryStore) DeleteDeviceIf(ctx context.Context, ref server.DeviceRef, nodeKey protocol.NodeKey, policy []byte, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok, err := m.matching(ctx, ref, nodeKey, policy, now)
	if err != nil || !ok {
		return false, err
	}
	if err := m.Store.DeleteDevice(ctx, ref); err != nil {
		return false, err
	}
	return true, nil
}
