package directconn

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
	"github.com/redis/go-redis/v9"
)

const (
	redisKeyPrefix = "silo:direct:"
	// storeAttempts bounds the read-then-script loop of PutDevice and
	// DeleteDevice when the stored node key changes between the two steps.
	storeAttempts = 3
	// confIndexBatch is how many confirmation keys one DeleteHost script call
	// removes.
	confIndexBatch = 500
)

// Script results. The scripts return these strings so the Go side never
// depends on Lua number conversion.
const (
	scriptOK       = "OK"
	scriptReplaced = "REPLACED"
	scriptRetry    = "RETRY"
	scriptConflict = "CONFLICT"
	scriptLimit    = "LIMIT"
	scriptGone     = "GONE"
	scriptFenced   = "FENCED"
)

// redisNowLua opens every script that decides expiry. It reads Redis time into
// now, in Unix milliseconds. Redis before 5.0 refuses a write after TIME unless
// the script first switches to effects replication; from 5.0 the call is a
// no-op, and the guard keeps the script valid where the function is absent.
const redisNowLua = `
if redis.replicate_commands then redis.replicate_commands() end
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
`

// putDeviceScript enrolls or renews one device.
// KEYS: dev, nk_new, acct, nk_old, reg.
// ARGV: expected_old_key, uid:did, node_key, policy, lifetime_ms, max, revision.
// The revision names the write: the script stores it with the result of the write
// it applied, and a run that names the revision the entry already holds returns
// that stored result and changes nothing. A client that lost the reply to an
// applied write and sent it again therefore gets the original result, which for a
// replacement says that a key was replaced. An empty revision names no write.
// The lease ends lifetime_ms after Redis time. The stored lease_end_ms, the
// account score and every TTL carry that one deadline, and the account index is
// pruned at Redis time.
var putDeviceScript = redis.NewScript(redisNowLua + `
if ARGV[7] ~= '' and redis.call('HGET', KEYS[1], 'rev') == ARGV[7] then
  return redis.call('HGET', KEYS[1], 'res')
end
local old = redis.call('HGET', KEYS[1], 'node_key') or ''
if old ~= ARGV[1] then return 'RETRY' end
redis.call('ZREMRANGEBYSCORE', KEYS[3], '-inf', now)
local owner = redis.call('GET', KEYS[2])
if owner and owner ~= ARGV[2] then return 'CONFLICT' end
local did = string.sub(ARGV[2], string.find(ARGV[2], ':') + 1)
if not redis.call('ZSCORE', KEYS[3], did) and redis.call('ZCARD', KEYS[3]) >= tonumber(ARGV[6]) then
  return 'LIMIT'
end
local replaced = old ~= '' and old ~= ARGV[3]
if replaced then
  redis.call('DEL', KEYS[4])
  redis.call('DEL', KEYS[5])
end
local ttl = tonumber(ARGV[5])
local lease_end = now + ttl
if ttl <= 0 then
  redis.call('DEL', KEYS[1], KEYS[2])
  redis.call('ZREM', KEYS[3], did)
else
  redis.call('HSET', KEYS[1], 'node_key', ARGV[3], 'policy', ARGV[4], 'lease_end_ms', lease_end,
    'rev', ARGV[7], 'res', replaced and 'REPLACED' or 'OK')
  redis.call('PEXPIRE', KEYS[1], ttl)
  redis.call('SET', KEYS[2], ARGV[2], 'PX', ttl)
  redis.call('ZADD', KEYS[3], lease_end, did)
  if redis.call('PTTL', KEYS[3]) < ttl then
    redis.call('PEXPIRE', KEYS[3], ttl)
  end
end
if replaced then return 'REPLACED' end
return 'OK'
`)

// withdrawDeviceScript removes one device and its indices.
// KEYS: dev, nk, acct, reg, then one confirmation key per host label.
// ARGV: expected_key, did.
// The account's other members stay in acct.
var withdrawDeviceScript = redis.NewScript(`
local cur = redis.call('HGET', KEYS[1], 'node_key') or ''
if cur ~= ARGV[1] then return 'RETRY' end
redis.call('DEL', KEYS[1], KEYS[2], KEYS[4])
for i = 5, #KEYS do
  redis.call('DEL', KEYS[i])
end
redis.call('ZREM', KEYS[3], ARGV[2])
return 'OK'
`)

// swapDevicePolicyScript replaces the policy of a lease that is live at Redis
// time and holds the expected node key and policy. KEYS: dev. ARGV: node_key,
// expected_policy, policy.
var swapDevicePolicyScript = redis.NewScript(redisNowLua + `
local cur = redis.call('HMGET', KEYS[1], 'node_key', 'policy', 'lease_end_ms')
if cur[1] ~= ARGV[1] or (cur[2] or '') ~= ARGV[2] or tonumber(cur[3] or '0') <= now then
  return 'GONE'
end
redis.call('HSET', KEYS[1], 'policy', ARGV[3])
return 'OK'
`)

// deleteDeviceIfScript removes a lease that is live at Redis time and holds the
// expected node key and policy, with its indices.
// KEYS: dev, nk, acct, reg, then one confirmation key per host label.
// ARGV: node_key, policy, did.
var deleteDeviceIfScript = redis.NewScript(redisNowLua + `
local cur = redis.call('HMGET', KEYS[1], 'node_key', 'policy', 'lease_end_ms')
if cur[1] ~= ARGV[1] or (cur[2] or '') ~= ARGV[2] or tonumber(cur[3] or '0') <= now then
  return 'GONE'
end
redis.call('DEL', KEYS[1], KEYS[2], KEYS[4])
for i = 5, #KEYS do
  redis.call('DEL', KEYS[i])
end
redis.call('ZREM', KEYS[3], ARGV[3])
return 'OK'
`)

// putRegistrationScript stores a registration for a lease that is live at Redis
// time and holds the same node key. KEYS: dev, reg. ARGV: node_key,
// registration_json, presence_ms. The stored value is a storedRegistration whose
// presence deadline and PX come from presence_ms and the same Redis time.
var putRegistrationScript = redis.NewScript(redisNowLua + `
local cur = redis.call('HGET', KEYS[1], 'node_key')
local lease_end = tonumber(redis.call('HGET', KEYS[1], 'lease_end_ms') or '0')
if cur ~= ARGV[1] or lease_end <= now then return 'GONE' end
local presence = tonumber(ARGV[3])
if presence <= 0 then
  redis.call('DEL', KEYS[2])
else
  local value = '{"presence_end_ms":' .. (now + presence) .. ',"registration":' .. ARGV[2] .. '}'
  redis.call('SET', KEYS[2], value, 'PX', presence)
end
return 'OK'
`)

// fencedSetScript writes or deletes one key while the engine lease holds the
// fence. KEYS: lease, key. ARGV: fence, value, ttl_ms; a ttl of 0 or less
// deletes the key.
var fencedSetScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 'FENCED' end
if tonumber(ARGV[3]) <= 0 then
  redis.call('DEL', KEYS[2])
else
  redis.call('SET', KEYS[2], ARGV[2], 'PX', ARGV[3])
end
return 'OK'
`)

// fencedDeleteScript deletes keys while the engine lease holds the fence.
// KEYS: lease, then the keys to delete. ARGV: fence.
var fencedDeleteScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 'FENCED' end
for i = 2, #KEYS do
  redis.call('DEL', KEYS[i])
end
return 'OK'
`)

// putConfirmationScript writes or clears one confirmation and its entry in the
// host's index while the engine lease holds the fence.
// KEYS: lease, confirmation, index. ARGV: fence, value, ttl_ms; a ttl of 0 or
// less clears the confirmation.
// The index is a sorted set of the host's confirmation keys scored by their
// expiry, which DeleteHost reads to find them. Every write drops the members
// whose expiry has passed, and the index expires with its longest member, so an
// idle host leaves none. Scores, pruning and the index's lifetime are on Redis
// time, the clock the confirmation key itself expires on, so a caller whose
// clock steps cannot drop a live confirmation from the index.
var putConfirmationScript = redis.NewScript(redisNowLua + `
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 'FENCED' end
redis.call('ZREMRANGEBYSCORE', KEYS[3], '-inf', now)
if tonumber(ARGV[3]) <= 0 then
  redis.call('DEL', KEYS[2])
  redis.call('ZREM', KEYS[3], KEYS[2])
else
  redis.call('SET', KEYS[2], ARGV[2], 'PX', ARGV[3])
  redis.call('ZADD', KEYS[3], now + tonumber(ARGV[3]), KEYS[2])
end
local last = redis.call('ZRANGE', KEYS[3], -1, -1, 'WITHSCORES')
if #last > 0 then
  redis.call('PEXPIRE', KEYS[3], math.max(1, math.floor(tonumber(last[2]) - now)))
end
return 'OK'
`)

// dropConfirmationsScript deletes confirmation keys and removes them from the
// host's index while the engine lease holds the fence.
// KEYS: lease, index, then the confirmation keys. ARGV: fence.
var dropConfirmationsScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 'FENCED' end
for i = 3, #KEYS do
  redis.call('DEL', KEYS[i])
  redis.call('ZREM', KEYS[2], KEYS[i])
end
return 'OK'
`)

// storedRegistration is the registration value. The presence deadline is in
// Redis time and travels with it, so a read judges presence at the same Redis
// time as the lease.
type storedRegistration struct {
	PresenceEndMS int64                 `json:"presence_end_ms"`
	Registration  protocol.Registration `json:"registration"`
}

type storedHost struct {
	NodeKey       protocol.NodeKey  `json:"node_key"`
	DiscoKey      protocol.DiscoKey `json:"disco_key"`
	Endpoints     []netip.AddrPort  `json:"endpoints"`
	Origin        string            `json:"origin,omitempty"`
	PresenceEndMS int64             `json:"presence_end_ms"`
}

type storedConfirmation struct {
	ExpiresMS    int64                 `json:"expires_ms"`
	Confirmation protocol.Confirmation `json:"confirmation"`
}

// redisStore implements CoordinationStore on one Redis client. Every Redis error
// surfaces as server.ErrUnavailable.
//
// Redis time decides device leases and registration presence, so every replica
// agrees on which enrollments are live whatever its own clock reads. A method
// uses its now argument, the caller's clock reading, only to turn each deadline
// it is given into a lifetime from now, and returns each deadline it reads as
// that now plus the time left at Redis time. This is not server.Store's
// "Expiry at now is absent": an entry is absent once Redis time reaches its
// deadline, whatever the caller's now. Host entries and confirmations stay on
// the caller's clock, because the engine lease holder is their one writer and
// reader; the confirmation index is on Redis time (putConfirmationScript).
//
// PutHost, PutConfirmation and DeleteHost write only while the engine lease
// (engineLeaseKey) holds the store's fence, and otherwise return
// errNotLeaseHolder without writing, so a process that lost the lease cannot
// replace or remove its successor's entries. The check holds against one Redis
// primary only. Every script declares each key it touches in KEYS.
type redisStore struct {
	client *redis.Client
	// labels lists the host labels the store serves, sorted. Hosts and
	// Confirmations read these labels by key; no read scans the keyspace.
	labels []string
	// fence is the engine lease value under which this store may write host
	// entries and confirmations: the process's presence ID.
	fence string
}

var _ CoordinationStore = (*redisStore)(nil)

// errNotLeaseHolder refuses a host or confirmation write because the engine
// lease does not hold the store's fence value: another process runs the engine,
// or none does. Nothing was written. It is not server.ErrUnavailable, because
// Redis answered.
var errNotLeaseHolder = errors.New("this process does not hold the direct engine lease")

func newRedisStore(client *redis.Client, labels []string, fence string) *redisStore {
	labels = slices.Clone(labels)
	sort.Strings(labels)
	return &redisStore{client: client, labels: labels, fence: fence}
}

// encodeDeviceID returns the unpadded base64url form (RFC 4648 section 5) of a
// device ID. A device ID may contain ':', which the encoded form never does.
func encodeDeviceID(id server.DeviceID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id))
}

func decodeDeviceID(s string) (server.DeviceID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return "", fmt.Errorf("malformed device id: %w", err)
	}
	return server.DeviceID(raw), nil
}

func devKey(ref server.DeviceRef) string {
	return redisKeyPrefix + "dev:" + string(ref.Account) + ":" + encodeDeviceID(ref.Device)
}
func regKey(ref server.DeviceRef) string {
	return redisKeyPrefix + "reg:" + string(ref.Account) + ":" + encodeDeviceID(ref.Device)
}
func confKey(label string, ref server.DeviceRef) string {
	return redisKeyPrefix + "conf:" + label + ":" + string(ref.Account) + ":" + encodeDeviceID(ref.Device)
}
func acctKey(account server.AccountID) string { return redisKeyPrefix + "acct:" + string(account) }
func hostKey(label string) string             { return redisKeyPrefix + "host:" + label }

// confIndexKey is the sorted set that lists the confirmation keys host label
// wrote, scored by expiry (putConfirmationScript).
func confIndexKey(label string) string { return redisKeyPrefix + "confidx:" + label }

func nkKey(k protocol.NodeKey) string { return nkKeyHex(hex.EncodeToString(k[:])) }
func nkKeyHex(h string) string        { return redisKeyPrefix + "nk:" + h }

// ownerValue is the node-key index value "{uid}:{did}". The account is a
// decimal user ID, so the first ':' separates the two parts.
func ownerValue(ref server.DeviceRef) string {
	return string(ref.Account) + ":" + encodeDeviceID(ref.Device)
}

func parseOwner(v string) (server.DeviceRef, error) {
	account, did, ok := strings.Cut(v, ":")
	if !ok || account == "" || did == "" {
		return server.DeviceRef{}, errors.New("malformed node key owner")
	}
	id, err := decodeDeviceID(did)
	if err != nil {
		return server.DeviceRef{}, err
	}
	return server.DeviceRef{Account: server.AccountID(account), Device: id}, nil
}

func unavailable(op string, err error) error {
	return fmt.Errorf("%w: %s: %w", server.ErrUnavailable, op, err)
}

func malformed(what string) error {
	return fmt.Errorf("%w: malformed %s", server.ErrUnavailable, what)
}

func (s *redisStore) checkLabel(label string) error {
	if !slices.Contains(s.labels, label) {
		return fmt.Errorf("host label %q is not served by this store", label)
	}
	return nil
}

func (s *redisStore) confKeys(ref server.DeviceRef) []string {
	keys := make([]string, len(s.labels))
	for i, label := range s.labels {
		keys[i] = confKey(label, ref)
	}
	return keys
}

// storedNodeKey reads the node key the device hash holds, "" when absent.
func (s *redisStore) storedNodeKey(ctx context.Context, ref server.DeviceRef) (string, error) {
	got, err := s.client.HGet(ctx, devKey(ref), "node_key").Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	if err != nil {
		return "", unavailable("read device node key", err)
	}
	return got, nil
}

func (s *redisStore) PutDevice(ctx context.Context, d server.Device, maxPerAccount int, now time.Time) (bool, error) {
	_, replaced, err := s.putDevice(ctx, d, maxPerAccount, now)
	return replaced, err
}

// PutDeviceReplacing returns the node key the script itself replaced: the script
// writes only while the stored key is still the one read before it, so that key
// is the one the write removed.
func (s *redisStore) PutDeviceReplacing(ctx context.Context, d server.Device, maxPerAccount int, now time.Time) (protocol.NodeKey, error) {
	old, _, err := s.putDevice(ctx, d, maxPerAccount, now)
	return old, err
}

// putDevice runs the put script and reports whether it replaced the device's
// node key and, if so, which key. The stored key is decoded before the script
// runs, so nothing can fail after the write. The policy's revision names the
// write to the script, so a replay of an applied write by the Redis client
// reports the replacement again and not a renewal of the key it installed.
func (s *redisStore) putDevice(ctx context.Context, d server.Device, maxPerAccount int, now time.Time) (protocol.NodeKey, bool, error) {
	nkNew := nkKey(d.NodeKey)
	for range storeAttempts {
		old, err := s.storedNodeKey(ctx, d.Ref)
		if err != nil {
			return protocol.NodeKey{}, false, err
		}
		var oldKey protocol.NodeKey
		nkOld := nkNew
		if old != "" {
			raw, err := hex.DecodeString(old)
			if err != nil || len(raw) != len(oldKey) {
				return protocol.NodeKey{}, false, malformed("device node key")
			}
			copy(oldKey[:], raw)
			nkOld = nkKeyHex(old)
		}
		keys := []string{devKey(d.Ref), nkNew, acctKey(d.Ref.Account), nkOld, regKey(d.Ref)}
		res, err := putDeviceScript.Run(ctx, s.client, keys,
			old, ownerValue(d.Ref), hex.EncodeToString(d.NodeKey[:]), string(d.Policy),
			d.LeaseExpires.Sub(now).Milliseconds(), maxPerAccount, policyRevision(d.Policy)).Text()
		if err != nil {
			return protocol.NodeKey{}, false, unavailable("put device", err)
		}
		switch res {
		case scriptOK:
			return protocol.NodeKey{}, false, nil
		case scriptReplaced:
			return oldKey, true, nil
		case scriptConflict:
			return protocol.NodeKey{}, false, server.ErrNodeKeyConflict
		case scriptLimit:
			return protocol.NodeKey{}, false, server.ErrDeviceLimit
		case scriptRetry:
		default:
			return protocol.NodeKey{}, false, malformed("put device result")
		}
	}
	return protocol.NodeKey{}, false, unavailable("put device", errors.New("node key kept changing"))
}

// rebase returns a deadline in Redis time on the caller's clock: the caller's
// now plus the time left at Redis time.
func rebase(now time.Time, deadlineMS, redisNowMS int64) time.Time {
	return now.Add(time.Duration(deadlineMS-redisNowMS) * time.Millisecond).UTC()
}

// redisNowMS reads a TIME reply as Unix milliseconds.
func redisNowMS(cmd *redis.TimeCmd) (int64, error) {
	t, err := cmd.Result()
	if err != nil {
		return 0, unavailable("read redis time", err)
	}
	return t.UnixMilli(), nil
}

// parseDevice builds a Device from a device hash and the optional registration
// value. Liveness is judged at redisNow; the returned deadlines are on the
// caller's clock now. It returns server.ErrNotFound for an absent or lapsed
// lease.
func parseDevice(ref server.DeviceRef, hash map[string]string, regRaw string, now time.Time, redisNow int64) (server.Device, error) {
	if len(hash) == 0 {
		return server.Device{}, server.ErrNotFound
	}
	leaseEnd, err := strconv.ParseInt(hash["lease_end_ms"], 10, 64)
	if err != nil {
		return server.Device{}, malformed("device lease")
	}
	if leaseEnd <= redisNow {
		return server.Device{}, server.ErrNotFound
	}
	raw, err := hex.DecodeString(hash["node_key"])
	if err != nil || len(raw) != len(protocol.NodeKey{}) {
		return server.Device{}, malformed("device node key")
	}
	d := server.Device{Ref: ref, LeaseExpires: rebase(now, leaseEnd, redisNow)}
	copy(d.NodeKey[:], raw)
	if policy := hash["policy"]; policy != "" {
		d.Policy = []byte(policy)
	}
	if regRaw != "" {
		var stored storedRegistration
		if err := json.Unmarshal([]byte(regRaw), &stored); err != nil {
			return server.Device{}, malformed("registration")
		}
		if stored.PresenceEndMS > redisNow {
			d.Registration = &stored.Registration
			d.PresenceExpires = rebase(now, stored.PresenceEndMS, redisNow)
		}
	}
	return d, nil
}

// readDevice reads Redis time, the device hash and the registration in one
// pipeline.
func (s *redisStore) readDevice(ctx context.Context, ref server.DeviceRef, now time.Time) (server.Device, error) {
	pipe := s.client.Pipeline()
	clock := pipe.Time(ctx)
	hash := pipe.HGetAll(ctx, devKey(ref))
	reg := pipe.Get(ctx, regKey(ref))
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return server.Device{}, unavailable("read device", err)
	}
	redisNow, err := redisNowMS(clock)
	if err != nil {
		return server.Device{}, err
	}
	regRaw, err := reg.Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return server.Device{}, unavailable("read registration", err)
	}
	return parseDevice(ref, hash.Val(), regRaw, now, redisNow)
}

func (s *redisStore) Device(ctx context.Context, ref server.DeviceRef, now time.Time) (server.Device, error) {
	return s.readDevice(ctx, ref, now)
}

func (s *redisStore) DeviceByNodeKey(ctx context.Context, k protocol.NodeKey, now time.Time) (server.Device, error) {
	owner, err := s.client.Get(ctx, nkKey(k)).Result()
	if errors.Is(err, redis.Nil) {
		return server.Device{}, server.ErrNotFound
	}
	if err != nil {
		return server.Device{}, unavailable("read node key owner", err)
	}
	ref, err := parseOwner(owner)
	if err != nil {
		return server.Device{}, malformed("node key owner")
	}
	d, err := s.readDevice(ctx, ref, now)
	if err != nil {
		return server.Device{}, err
	}
	if d.NodeKey != k {
		return server.Device{}, server.ErrNotFound
	}
	return d, nil
}

// ListDevices judges liveness at the Redis time of its first round trip, which
// reads the account index; a hash that is gone by the second is dropped.
func (s *redisStore) ListDevices(ctx context.Context, account server.AccountID, now time.Time) ([]server.Device, error) {
	index := s.client.Pipeline()
	clock := index.Time(ctx)
	scored := index.ZRangeWithScores(ctx, acctKey(account), 0, -1)
	if _, err := index.Exec(ctx); err != nil {
		return nil, unavailable("list account", err)
	}
	redisNow, err := redisNowMS(clock)
	if err != nil {
		return nil, err
	}
	members := scored.Val()
	refs := make([]server.DeviceRef, 0, len(members))
	for _, m := range members {
		if int64(m.Score) <= redisNow {
			continue
		}
		member, ok := m.Member.(string)
		if !ok {
			return nil, malformed("account member")
		}
		id, err := decodeDeviceID(member)
		if err != nil {
			return nil, malformed("account member")
		}
		refs = append(refs, server.DeviceRef{Account: account, Device: id})
	}
	pipe := s.client.Pipeline()
	hashes := make([]*redis.MapStringStringCmd, len(refs))
	regs := make([]*redis.StringCmd, len(refs))
	for i, ref := range refs {
		hashes[i] = pipe.HGetAll(ctx, devKey(ref))
		regs[i] = pipe.Get(ctx, regKey(ref))
	}
	if len(refs) > 0 {
		if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
			return nil, unavailable("read account devices", err)
		}
	}
	result := make([]server.Device, 0, len(refs))
	for i, ref := range refs {
		regRaw, err := regs[i].Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return nil, unavailable("read registration", err)
		}
		d, err := parseDevice(ref, hashes[i].Val(), regRaw, now, redisNow)
		if errors.Is(err, server.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	slices.SortFunc(result, func(a, b server.Device) int { return strings.Compare(string(a.Ref.Device), string(b.Ref.Device)) })
	return result, nil
}

func (s *redisStore) DeleteDevice(ctx context.Context, ref server.DeviceRef) error {
	for range storeAttempts {
		expected, err := s.storedNodeKey(ctx, ref)
		if err != nil {
			return err
		}
		nk := devKey(ref)
		if expected != "" {
			nk = nkKeyHex(expected)
		}
		keys := append([]string{devKey(ref), nk, acctKey(ref.Account), regKey(ref)}, s.confKeys(ref)...)
		res, err := withdrawDeviceScript.Run(ctx, s.client, keys, expected, encodeDeviceID(ref.Device)).Text()
		if err != nil {
			return unavailable("delete device", err)
		}
		switch res {
		case scriptOK:
			return nil
		case scriptRetry:
		default:
			return malformed("delete device result")
		}
	}
	return unavailable("delete device", errors.New("node key kept changing"))
}

// conditionalResult maps a GONE-or-OK script result to whether it wrote.
func conditionalResult(op string, res string, err error) (bool, error) {
	if err != nil {
		return false, unavailable(op, err)
	}
	switch res {
	case scriptOK:
		return true, nil
	case scriptGone:
		return false, nil
	default:
		return false, malformed(op + " result")
	}
}

// SwapDevicePolicy and DeleteDeviceIf judge liveness at Redis time; they
// ignore now.
func (s *redisStore) SwapDevicePolicy(ctx context.Context, ref server.DeviceRef, nodeKey protocol.NodeKey, expected, policy []byte, _ time.Time) (bool, error) {
	res, err := swapDevicePolicyScript.Run(ctx, s.client, []string{devKey(ref)},
		hex.EncodeToString(nodeKey[:]), string(expected), string(policy)).Text()
	return conditionalResult("swap device policy", res, err)
}

func (s *redisStore) DeleteDeviceIf(ctx context.Context, ref server.DeviceRef, nodeKey protocol.NodeKey, policy []byte, _ time.Time) (bool, error) {
	keys := append([]string{devKey(ref), nkKey(nodeKey), acctKey(ref.Account), regKey(ref)}, s.confKeys(ref)...)
	res, err := deleteDeviceIfScript.Run(ctx, s.client, keys,
		hex.EncodeToString(nodeKey[:]), string(policy), encodeDeviceID(ref.Device)).Text()
	return conditionalResult("delete device if unchanged", res, err)
}

func (s *redisStore) PutRegistration(ctx context.Context, ref server.DeviceRef, reg protocol.Registration, presenceExpires, now time.Time) error {
	value, err := json.Marshal(reg)
	if err != nil {
		return fmt.Errorf("encode registration: %w", err)
	}
	res, err := putRegistrationScript.Run(ctx, s.client, []string{devKey(ref), regKey(ref)},
		hex.EncodeToString(reg.NodeKey[:]), string(value), presenceExpires.Sub(now).Milliseconds()).Text()
	if err != nil {
		return unavailable("put registration", err)
	}
	switch res {
	case scriptOK:
		return nil
	case scriptGone:
		return server.ErrNotFound
	default:
		return malformed("put registration result")
	}
}

// fencedResult maps a fenced script's result.
func fencedResult(op string, res string, err error) error {
	if err != nil {
		return unavailable(op, err)
	}
	switch res {
	case scriptOK:
		return nil
	case scriptFenced:
		return fmt.Errorf("%s: %w", op, errNotLeaseHolder)
	default:
		return malformed(op + " result")
	}
}

// fencedSet writes key with ttl, or deletes it when ttl is under a millisecond,
// while the engine lease holds the store's fence.
func (s *redisStore) fencedSet(ctx context.Context, op, key, value string, ttl time.Duration) error {
	res, err := fencedSetScript.Run(ctx, s.client, []string{engineLeaseKey, key}, s.fence, value, ttl.Milliseconds()).Text()
	return fencedResult(op, res, err)
}

// fencedDelete deletes keys while the engine lease holds the store's fence.
func (s *redisStore) fencedDelete(ctx context.Context, op string, keys ...string) error {
	res, err := fencedDeleteScript.Run(ctx, s.client, append([]string{engineLeaseKey}, keys...), s.fence).Text()
	return fencedResult(op, res, err)
}

func (s *redisStore) PutHost(ctx context.Context, h server.HostEntry, now time.Time) error {
	if err := s.checkLabel(h.Label); err != nil {
		return err
	}
	ttl := h.PresenceExpires.Sub(now)
	if ttl.Milliseconds() <= 0 {
		return s.fencedSet(ctx, "delete expired host", hostKey(h.Label), "", 0)
	}
	value, err := json.Marshal(storedHost{NodeKey: h.NodeKey, DiscoKey: h.DiscoKey, Endpoints: h.Endpoints, Origin: h.Origin, PresenceEndMS: h.PresenceExpires.UnixMilli()})
	if err != nil {
		return fmt.Errorf("encode host: %w", err)
	}
	return s.fencedSet(ctx, "put host", hostKey(h.Label), string(value), ttl)
}

// DeleteHost removes the host entry, then every confirmation that host wrote,
// one batch of the host's index at a time; each step is fenced, so a refusal can
// leave a later batch in place. It reads only the index, so its cost follows the
// host's confirmations, not the size of the keyspace the store shares with
// other applications.
func (s *redisStore) DeleteHost(ctx context.Context, label string) error {
	if err := s.checkLabel(label); err != nil {
		return err
	}
	if err := s.fencedDelete(ctx, "delete host", hostKey(label)); err != nil {
		return err
	}
	index := confIndexKey(label)
	for {
		members, err := s.client.ZRange(ctx, index, 0, confIndexBatch-1).Result()
		if err != nil {
			return unavailable("read host confirmation index", err)
		}
		if len(members) == 0 {
			return nil
		}
		res, err := dropConfirmationsScript.Run(ctx, s.client, append([]string{engineLeaseKey, index}, members...), s.fence).Text()
		if err = fencedResult("delete host confirmations", res, err); err != nil {
			return err
		}
	}
}

func (s *redisStore) Hosts(ctx context.Context, now time.Time) ([]server.HostEntry, error) {
	result := make([]server.HostEntry, 0, len(s.labels))
	if len(s.labels) == 0 {
		return result, nil
	}
	keys := make([]string, len(s.labels))
	for i, label := range s.labels {
		keys[i] = hostKey(label)
	}
	values, err := s.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, unavailable("read hosts", err)
	}
	for i, v := range values {
		raw, ok := v.(string)
		if !ok {
			continue
		}
		var stored storedHost
		if err := json.Unmarshal([]byte(raw), &stored); err != nil {
			return nil, malformed("host")
		}
		if stored.PresenceEndMS <= now.UnixMilli() {
			continue
		}
		result = append(result, server.HostEntry{
			Label: s.labels[i], NodeKey: stored.NodeKey, DiscoKey: stored.DiscoKey, Endpoints: stored.Endpoints,
			Origin: stored.Origin, PresenceExpires: time.UnixMilli(stored.PresenceEndMS).UTC(),
		})
	}
	return result, nil
}

func (s *redisStore) PutConfirmation(ctx context.Context, hostLabel string, ref server.DeviceRef, c *protocol.Confirmation, expires, now time.Time) error {
	if err := s.checkLabel(hostLabel); err != nil {
		return err
	}
	key := confKey(hostLabel, ref)
	ttl := expires.Sub(now)
	if c == nil || ttl.Milliseconds() <= 0 {
		return s.putConfirmation(ctx, "delete confirmation", hostLabel, key, "", 0)
	}
	value, err := json.Marshal(storedConfirmation{ExpiresMS: expires.UnixMilli(), Confirmation: *c})
	if err != nil {
		return fmt.Errorf("encode confirmation: %w", err)
	}
	return s.putConfirmation(ctx, "put confirmation", hostLabel, key, string(value), ttl)
}

// putConfirmation runs putConfirmationScript for one confirmation of host label.
func (s *redisStore) putConfirmation(ctx context.Context, op, label, key, value string, ttl time.Duration) error {
	res, err := putConfirmationScript.Run(ctx, s.client, []string{engineLeaseKey, key, confIndexKey(label)},
		s.fence, value, ttl.Milliseconds()).Text()
	return fencedResult(op, res, err)
}

func (s *redisStore) Confirmations(ctx context.Context, ref server.DeviceRef, now time.Time) (map[string]protocol.Confirmation, error) {
	result := make(map[string]protocol.Confirmation)
	if len(s.labels) == 0 {
		return result, nil
	}
	values, err := s.client.MGet(ctx, s.confKeys(ref)...).Result()
	if err != nil {
		return nil, unavailable("read confirmations", err)
	}
	for i, v := range values {
		raw, ok := v.(string)
		if !ok {
			continue
		}
		var stored storedConfirmation
		if err := json.Unmarshal([]byte(raw), &stored); err != nil {
			return nil, malformed("confirmation")
		}
		if stored.ExpiresMS > now.UnixMilli() {
			result[s.labels[i]] = stored.Confirmation
		}
	}
	return result, nil
}
