package directconn

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
	"github.com/darthShadow/shunpike/engine"
)

const (
	// confirmationLifetime is how long the store keeps a confirmation that is not
	// renewed. The path tracker applies the same bound to its own copy.
	confirmationLifetime = 90 * time.Second

	pathClassDirect = engine.PathClass("direct")
)

// pathRecord is what this replica knows about one device's path to the API host.
type pathRecord struct {
	class      engine.PathClass
	endpoint   netip.AddrPort
	since      time.Time // when the class last changed
	generation uint64    // random per path, never 0
}

// pathTracker keeps the replica's in-process path records. The engine has no
// per-peer change signal, so every read compares the engine's path with the
// stored one and draws a new generation on a difference. It stores only the
// keyed hash of a peer's address, never the address itself.
type pathTracker struct {
	mu        sync.Mutex
	secret    []byte
	clock     func() time.Time
	records   map[protocol.NodeKey]pathRecord
	refs      map[server.DeviceRef]protocol.NodeKey
	confirmed map[protocol.NodeKey]time.Time
}

func newPathTracker(secret string, clock func() time.Time) *pathTracker {
	return &pathTracker{secret: []byte(secret), clock: clock, records: make(map[protocol.NodeKey]pathRecord),
		refs: make(map[server.DeviceRef]protocol.NodeKey), confirmed: make(map[protocol.NodeKey]time.Time)}
}

// observe returns the device's record after comparing it with the engine's
// current path.
func (t *pathTracker) observe(k protocol.NodeKey, status engine.PathStatus) pathRecord {
	t.mu.Lock()
	defer t.mu.Unlock()
	record, ok := t.records[k]
	if ok && record.class == status.Class && record.endpoint == status.Endpoint {
		return record
	}
	since := t.clock()
	if ok && record.class == status.Class {
		since = record.since
	}
	record = pathRecord{class: status.Class, endpoint: status.Endpoint, since: since, generation: newGeneration()}
	t.records[k] = record
	return record
}

// newGeneration draws a nonzero random path generation.
func newGeneration() uint64 {
	var b [8]byte
	for {
		// crypto/rand.Read does not fail on supported platforms.
		_, _ = rand.Read(b[:])
		if g := binary.BigEndian.Uint64(b[:]); g != 0 {
			return g
		}
	}
}

// connectionKey is "direct:" plus a keyed hash of the peer's address, following
// the rule of the playback connection key: IPv4 addresses hash whole and IPv6
// addresses hash by their /64. It returns "" for an unresolved address.
func (t *pathTracker) connectionKey(endpoint netip.AddrPort) string {
	addr := endpoint.Addr().Unmap()
	if !addr.IsValid() || len(t.secret) == 0 {
		return ""
	}
	var data []byte
	if addr.Is4() {
		b := addr.As4()
		data = b[:]
	} else {
		prefix, err := addr.Prefix(64)
		if err != nil {
			return ""
		}
		b := prefix.Addr().As16()
		data = b[:]
	}
	mac := hmac.New(sha256.New, t.secret)
	mac.Write([]byte("playback-connection-v1\x00"))
	mac.Write(data)
	return "direct:" + hex.EncodeToString(mac.Sum(nil))
}

// reset forgets every path record and confirmation, which belong to the engine
// that observed and published them. The device map stays: it follows the peer
// cache, not an engine.
func (t *pathTracker) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	clear(t.records)
	clear(t.confirmed)
}

// setDevices replaces the (user, device) to node key map with the peer cache's
// devices and drops records of devices that left it.
func (t *pathTracker) setDevices(entries []server.PeerEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refs = make(map[server.DeviceRef]protocol.NodeKey, len(entries))
	held := make(map[protocol.NodeKey]bool, len(entries))
	for _, e := range entries {
		t.refs[e.Ref] = e.NodeKey
		held[e.NodeKey] = true
	}
	for k := range t.records {
		if !held[k] {
			delete(t.records, k)
		}
	}
	for k := range t.confirmed {
		if !held[k] {
			delete(t.confirmed, k)
		}
	}
}

func (t *pathTracker) keyFor(ref server.DeviceRef) (protocol.NodeKey, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	k, ok := t.refs[ref]
	return k, ok
}

// setConfirmed records that the API host published, or stopped publishing, its
// confirmation of the device.
func (t *pathTracker) setConfirmed(k protocol.NodeKey, live bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if live {
		t.confirmed[k] = t.clock()
	} else {
		delete(t.confirmed, k)
	}
}

func (t *pathTracker) confirmedLive(k protocol.NodeKey) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	at, ok := t.confirmed[k]
	return ok && t.clock().Sub(at) < confirmationLifetime
}

// enginePeers converts peer cache entries into the engine's peer set. A device's
// netmap confirmation of this host is not part of the entry, and nothing in the
// engine's path rule reads it.
func enginePeers(entries []server.PeerEntry) []engine.Peer {
	peers := make([]engine.Peer, 0, len(entries))
	for _, e := range entries {
		peers = append(peers, engine.Peer{NodeKey: e.NodeKey, DiscoKey: e.DiscoKey, Endpoints: e.Endpoints, Expires: e.Expires})
	}
	return peers
}

// policyOf reads the connection path a device enrolled with from the opaque
// policy bytes. Unreadable bytes give "".
func policyOf(policy []byte) string {
	var p devicePolicy
	if json.Unmarshal(policy, &p) != nil {
		return ""
	}
	return p.ConnectionPath
}

// SetCoordination attaches the coordination service whose peer cache feeds the
// engine and whose store receives the host entry and confirmations. Call it once,
// before the first SettingsChanged. It starts the goroutines that keep the peer
// cache fresh, apply its changes to the engine and close the streams of revoked
// devices; shutdown joins them.
func (s *Service) SetCoordination(c *Coordination) {
	l := s.lifecycle
	l.mu.Lock()
	l.coord = c
	l.paths = newPathTracker(c.cfg.AuthSecret, c.clock)
	l.mu.Unlock()
	// One slot: wakes that arrive during a pass coalesce into the next pass.
	sweepWake := make(chan struct{}, 1)
	l.coordWorkers.Add(3)
	go func() {
		defer l.coordWorkers.Done()
		c.run(s.ctx)
	}()
	go func() {
		defer l.coordWorkers.Done()
		s.directoryEvents(s.ctx, c.directory, sweepWake)
	}()
	go func() {
		defer l.coordWorkers.Done()
		c.sweepRevocations(s.ctx, sweepWake)
	}()
}

func (s *Service) coordination() (*Coordination, *pathTracker) {
	l := s.lifecycle
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.coord, l.paths
}

// directoryEvents is the sole reader of the peer cache's Changed channel. It
// hands the cache's devices to the engine and to the path tracker on each change,
// then wakes the revocation sweep, which closes the signaling stream of a device
// whose lease ended on another replica: the cache's refresh drops that device,
// and the store confirms it. It reads no store, so a slow store never holds a
// peer update. A wake while one is pending is dropped.
func (s *Service) directoryEvents(ctx context.Context, d *server.Directory, sweepWake chan<- struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.Changed():
			entries := d.Devices()
			if _, paths := s.coordination(); paths != nil {
				paths.setDevices(entries)
			}
			if err := s.SetPeers(enginePeers(entries)); err != nil {
				s.cfg.Logger.WarnContext(ctx, "direct connections: peer update failed", "component", "directconn", "error", err)
			}
			select {
			case sweepWake <- struct{}{}:
			default:
			}
		}
	}
}

// IsPeer reports whether the running engine holds k as a peer.
func (s *Service) IsPeer(k protocol.NodeKey) bool {
	l := s.lifecycle
	l.mu.Lock()
	e := l.current
	l.mu.Unlock()
	return e != nil && e.IsPeer(k)
}

// devicePath returns the device's current path to the API host. It reads the
// engine, so a stale record is never served.
func (s *Service) devicePath(k protocol.NodeKey) (pathRecord, bool) {
	l := s.lifecycle
	l.mu.Lock()
	e, paths := l.current, l.paths
	l.mu.Unlock()
	if e == nil || paths == nil {
		return pathRecord{}, false
	}
	record := paths.observe(k, e.Path(k))
	return record, record.class == pathClassDirect
}

// Device implements DirectState. It reads the store once and the engine for the
// path. It answers ok=false once the service has read its settings and found
// direct connections off, from the setting it already holds, so a caller needs
// no setting read of its own. Before the first settings read the toggle is
// unknown, and the store answers: a device another replica enrolled direct_only
// must not be planned as a regular one.
func (s *Service) Device(ctx context.Context, userID int, deviceID string) (DeviceDirect, bool, error) {
	c, paths := s.coordination()
	if c == nil || s.disabled() {
		return DeviceDirect{}, false, nil
	}
	device, err := c.cfg.Store.Device(ctx, deviceRef(userID, deviceID), c.clock())
	if errors.Is(err, server.ErrNotFound) {
		return DeviceDirect{}, false, nil
	}
	if err != nil {
		return DeviceDirect{}, false, err //nolint:wrapcheck // The caller classifies store errors.
	}
	out := DeviceDirect{Policy: policyOf(device.Policy)}
	if record, direct := s.devicePath(device.NodeKey); direct {
		out.ConnectionKey = paths.connectionKey(record.endpoint)
		if paths.confirmedLive(device.NodeKey) {
			out.APIReady, out.PathGeneration = true, record.generation
		}
	}
	return out, true, nil
}

// disabled reports that the service has read its settings and direct
// connections are off. It is false until the first settings read.
func (s *Service) disabled() bool {
	l := s.lifecycle
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ready && !l.values.enabled
}

// PathGeneration implements DirectState.
func (s *Service) PathGeneration(userID int, deviceID string) uint64 {
	_, paths := s.coordination()
	if paths == nil {
		return 0
	}
	k, ok := paths.keyFor(deviceRef(userID, deviceID))
	if !ok {
		return 0
	}
	if record, direct := s.devicePath(k); direct {
		return record.generation
	}
	return 0
}

// PathObservation implements DirectState. It reads the engine and the path
// tracker in one hold of the lifecycle lock, the one devicePath takes, so the
// answer is one instant: the engine is running here or it is not, and the
// generation belongs to that engine.
func (s *Service) PathObservation(userID int, deviceID string) (bool, uint64) {
	l := s.lifecycle
	l.mu.Lock()
	e, paths := l.current, l.paths
	l.mu.Unlock()
	if e == nil {
		return false, 0
	}
	if paths == nil {
		return true, 0
	}
	k, ok := paths.keyFor(deviceRef(userID, deviceID))
	if !ok {
		return true, 0
	}
	if record := paths.observe(k, e.Path(k)); record.class == pathClassDirect {
		return true, record.generation
	}
	return true, 0
}

// Enrolled implements DirectState.
func (s *Service) Enrolled(ctx context.Context, userID int, deviceID string) (bool, error) {
	_, ok, err := s.Device(ctx, userID, deviceID)
	return ok, err
}

var _ DirectState = (*Service)(nil)

// accountNumber parses a peer cache entry's account into the numeric user ID.
func accountNumber(ref server.DeviceRef) (int, bool) {
	id, err := strconv.Atoi(string(ref.Account))
	return id, err == nil && id > 0
}
