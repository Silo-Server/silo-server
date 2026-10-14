package workercontrolcore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

const maxOutputAuthorities = 4096
const maxOutputAuthorityBytes = 1 << 20
const maxOutputTokenBytes = 64 << 10

// OutputAuthorityRecord is retained only at the core. Snapshots expose its
// opaque ID; the original customer token never crosses the worker boundary.
type OutputAuthorityRecord struct {
	ID         string `json:"id"`
	Revision   string `json:"revision,omitempty"`
	LookupKey  string `json:"lookup_key,omitempty"`
	Token      string `json:"token"`
	WorkID     string `json:"work_id"`
	Epoch      string `json:"epoch"`
	NodeID     int    `json:"node_id"`
	Generation uint64 `json:"generation"`
	ExpiresAt  int64  `json:"expires_at"`
}

type OutputAuthorities interface {
	Register(context.Context, OutputAuthorityRecord) (string, error)
	List(context.Context, int) ([]OutputAuthorityRecord, error)
	Delete(context.Context, OutputAuthorityRecord) error
}

// OutputAuthorityFinder is an optional optimization. A found record still
// requires its retained token and complete source/work identity to be checked.
type OutputAuthorityFinder interface {
	Find(context.Context, int, string) (OutputAuthorityRecord, bool, error)
}

// OutputAuthorityLookupKey binds all claim fields except JWT issue/expiry times
// to one assignment and signing epoch. It never authorizes a request itself.
func OutputAuthorityLookupKey(claims streamtoken.Claims, node int, work string, generation uint64, epoch string) string {
	claims.IssuedAt, claims.ExpiresAt = nil, nil
	if canonical, err := workeridentity.ValidateOrigin(claims.TranscodeNode, true); err == nil {
		claims.TranscodeNode = canonical
	}
	encoded, _ := json.Marshal([]any{"silo/worker-output-lookup/v1", claims, node, work, generation, epoch})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func validOutputLookupKey(key string) bool {
	decoded, err := hex.DecodeString(key)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == key
}

func outputAuthorityID(r OutputAuthorityRecord) string {
	value, _ := json.Marshal([]any{"silo/worker-output-authority/v1", r.Token, r.NodeID, r.WorkID, r.Generation, r.Epoch})
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
func validOutputAuthority(r OutputAuthorityRecord, now time.Time) bool {
	return r.NodeID > 0 && validWork(r.WorkID) && r.Generation > 0 && r.Generation < 1<<53 && r.Epoch != "" && len(r.Epoch) <= 128 && r.Token != "" && len(r.Token) <= maxOutputTokenBytes && len(r.Revision) <= 64 && (r.LookupKey == "" || validOutputLookupKey(r.LookupKey)) && r.ExpiresAt > now.Unix() && r.ExpiresAt < 1<<53 && (r.ID == "" || r.ID == outputAuthorityID(r))
}

// ValidForLookup checks the retained record independently of its index pointer.
func (r OutputAuthorityRecord) ValidForLookup(node int, key string, now time.Time) bool {
	return r.ID != "" && r.NodeID == node && r.LookupKey == key && validOutputAuthority(r, now)
}

type outputRecords struct {
	values map[string]OutputAuthorityRecord
	lookup map[string]string
	bytes  int
}
type MemoryOutputAuthorities struct {
	mu    sync.Mutex
	nodes map[int]*outputRecords
	now   func() time.Time
}

func NewMemoryOutputAuthorities() *MemoryOutputAuthorities {
	return &MemoryOutputAuthorities{nodes: make(map[int]*outputRecords), now: time.Now}
}
func (s *MemoryOutputAuthorities) prune(node int, now time.Time) *outputRecords {
	entries := s.nodes[node]
	if entries == nil {
		entries = &outputRecords{values: make(map[string]OutputAuthorityRecord), lookup: make(map[string]string)}
		s.nodes[node] = entries
	}
	for id, record := range entries.values {
		if record.ExpiresAt <= now.Unix() {
			entries.remove(id, record)
		}
	}
	return entries
}

func (s *outputRecords) removeIndex(id string, record OutputAuthorityRecord) {
	if record.LookupKey != "" && s.lookup[record.LookupKey] == id {
		delete(s.lookup, record.LookupKey)
	}
}

func (s *outputRecords) remove(id string, record OutputAuthorityRecord) {
	encoded, _ := json.Marshal(record)
	s.bytes -= len(encoded)
	s.removeIndex(id, record)
	delete(s.values, id)
}
func (s *MemoryOutputAuthorities) Register(ctx context.Context, record OutputAuthorityRecord) (string, error) {
	if ctx.Err() != nil || !validOutputAuthority(record, s.now()) {
		return "", ErrDenied
	}
	record.ID = outputAuthorityID(record)
	record.Revision = rand.Text()
	encoded, _ := json.Marshal(record)
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.prune(record.NodeID, s.now())
	previous, exists := entries.values[record.ID]
	previousBytes := 0
	if exists {
		encoded, _ := json.Marshal(previous)
		previousBytes = len(encoded)
	}
	if !exists && len(entries.values) >= maxOutputAuthorities || entries.bytes+len(encoded)-previousBytes > maxOutputAuthorityBytes {
		return "", ErrUnavailable
	}
	if exists {
		entries.removeIndex(record.ID, previous)
	}
	entries.values[record.ID] = record
	if record.LookupKey != "" {
		entries.lookup[record.LookupKey] = record.ID
	}
	entries.bytes += len(encoded) - previousBytes
	return record.ID, nil
}

func (s *MemoryOutputAuthorities) Find(ctx context.Context, node int, key string) (OutputAuthorityRecord, bool, error) {
	if ctx.Err() != nil || node <= 0 || !validOutputLookupKey(key) {
		return OutputAuthorityRecord{}, false, ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.nodes[node]
	if entries == nil {
		return OutputAuthorityRecord{}, false, nil
	}
	id := entries.lookup[key]
	if id == "" {
		return OutputAuthorityRecord{}, false, nil
	}
	record, found := entries.values[id]
	if !found {
		delete(entries.lookup, key)
		return OutputAuthorityRecord{}, false, nil
	}
	if record.ExpiresAt <= s.now().Unix() {
		entries.remove(id, record)
		return OutputAuthorityRecord{}, false, nil
	}
	if record.LookupKey == "" {
		delete(entries.lookup, key)
		return OutputAuthorityRecord{}, false, nil
	}
	if record.LookupKey != key || record.ID != id || record.NodeID != node || !validOutputAuthority(record, s.now()) {
		return OutputAuthorityRecord{}, false, ErrUnavailable
	}
	return record, true, nil
}
func (s *MemoryOutputAuthorities) List(ctx context.Context, node int) ([]OutputAuthorityRecord, error) {
	if ctx.Err() != nil || node <= 0 {
		return nil, ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.prune(node, s.now())
	out := make([]OutputAuthorityRecord, 0, len(entries.values))
	for _, record := range entries.values {
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Delete removes only the exact observation. A concurrent re-registration has
// a new revision and survives cleanup from an older core snapshot.
func (s *MemoryOutputAuthorities) Delete(ctx context.Context, record OutputAuthorityRecord) error {
	if ctx.Err() != nil || record.ID == "" || record.ID != outputAuthorityID(record) {
		return ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.prune(record.NodeID, s.now())
	if current, ok := entries.values[record.ID]; ok && current == record {
		entries.remove(record.ID, current)
	}
	return nil
}

type RedisOutputAuthorities struct {
	client *redis.Client
	prefix string
	now    func() time.Time
}

func NewRedisOutputAuthorities(client *redis.Client, prefix string) *RedisOutputAuthorities {
	if prefix == "" {
		prefix = "silo:http-worker-output:"
	}
	return &RedisOutputAuthorities{client: client, prefix: prefix, now: time.Now}
}
func (s *RedisOutputAuthorities) keys(node int) []string {
	base := s.prefix + strconv.Itoa(node)
	return []string{base + ":records", base + ":expiry", base + ":bytes", base + ":lookup", base + ":lookup-reverse"}
}

const outputIndexLua = `
local function unindex(id)
 local key=redis.call('HGET',KEYS[5],id)
 if key and redis.call('HGET',KEYS[4],key)==id then redis.call('HDEL',KEYS[4],key) end
 redis.call('HDEL',KEYS[5],id)
end
`

const pruneOutputsLua = outputIndexLua + `
local count=redis.call('HLEN',KEYS[1])
if count>0 and (not redis.call('GET',KEYS[3]) or redis.call('EXISTS',KEYS[2])==0) then
 return redis.error_reply('output authority accounting unavailable')
end
if count==0 then redis.call('DEL',KEYS[2],KEYS[3],KEYS[4],KEYS[5]) end
local stale=redis.call('ZRANGEBYSCORE',KEYS[2],'-inf',ARGV[1])
for _,id in ipairs(stale) do
 local value=redis.call('HGET',KEYS[1],id)
 if value then redis.call('DECRBY',KEYS[3],string.len(value)) end
 unindex(id)
 redis.call('HDEL',KEYS[1],id)
 redis.call('ZREM',KEYS[2],id)
end
if redis.call('HLEN',KEYS[1])==0 then redis.call('DEL',unpack(KEYS)) end
`

var registerOutputScript = redis.NewScript(pruneOutputsLua + `
local old=redis.call('HGET',KEYS[1],ARGV[2])
local held=tonumber(redis.call('GET',KEYS[3]) or '0')
local delta=string.len(ARGV[3])-(old and string.len(old) or 0)
if (not old and redis.call('HLEN',KEYS[1])>=tonumber(ARGV[5])) or held+delta>tonumber(ARGV[6]) then return 0 end
-- Older replicas remove records using only the original three keys. Discard
-- optional stale indices before they can grow beyond the bounded record map.
-- Keep this after quota acceptance so a refused registration cannot reset it.
local live=redis.call('HLEN',KEYS[1])
if redis.call('HLEN',KEYS[4])>live or redis.call('HLEN',KEYS[5])>live then
 redis.call('DEL',KEYS[4],KEYS[5])
end
unindex(ARGV[2])
redis.call('HSET',KEYS[1],ARGV[2],ARGV[3])
if ARGV[7]~='' then
 redis.call('HSET',KEYS[4],ARGV[7],ARGV[2])
 redis.call('HSET',KEYS[5],ARGV[2],ARGV[7])
end
redis.call('ZADD',KEYS[2],ARGV[4],ARGV[2])
redis.call('INCRBY',KEYS[3],delta)
local last=redis.call('ZREVRANGE',KEYS[2],0,0,'WITHSCORES')
local ttl=(tonumber(last[2])-tonumber(ARGV[1]))*1000+1000
for _,key in ipairs(KEYS) do redis.call('PEXPIRE',key,ttl) end
return 1
`)
var listOutputScript = redis.NewScript(pruneOutputsLua + `
if redis.call('HLEN',KEYS[1])>tonumber(ARGV[2]) or tonumber(redis.call('GET',KEYS[3]) or '0')>tonumber(ARGV[3]) then return redis.error_reply('output authority bounds exceeded') end
return redis.call('HVALS',KEYS[1])
`)

var deleteOutputScript = redis.NewScript(outputIndexLua + `
local current=redis.call('HGET',KEYS[1],ARGV[1])
if not current or current~=ARGV[2] then return 0 end
if not redis.call('GET',KEYS[3]) or redis.call('EXISTS',KEYS[2])==0 then
 return redis.error_reply('output authority accounting unavailable')
end
unindex(ARGV[1])
redis.call('HDEL',KEYS[1],ARGV[1])
redis.call('ZREM',KEYS[2],ARGV[1])
redis.call('DECRBY',KEYS[3],string.len(current))
if redis.call('HLEN',KEYS[1])==0 then redis.call('DEL',KEYS[1],KEYS[2],KEYS[3],KEYS[4],KEYS[5]) end
return 1
`)

// Find touches one index entry and one record, without a node-wide expiry scan
// or JSON decoding in Lua. Registration and snapshot sweeps prune the remainder.
var findOutputScript = redis.NewScript(outputIndexLua + `
local id=redis.call('HGET',KEYS[4],ARGV[2])
if not id then return false end
local value=redis.call('HGET',KEYS[1],id)
if not value then
 if redis.call('HGET',KEYS[4],ARGV[2])==id then redis.call('HDEL',KEYS[4],ARGV[2]) end
 unindex(id)
 return false
end
local count=redis.call('HLEN',KEYS[1])
local held=redis.call('GET',KEYS[3])
local expires=redis.call('ZSCORE',KEYS[2],id)
if not held or not expires then return redis.error_reply('output authority accounting unavailable') end
if count>tonumber(ARGV[3]) or tonumber(held)>tonumber(ARGV[4]) then return redis.error_reply('output authority bounds exceeded') end
if tonumber(expires)<=tonumber(ARGV[1]) then
 unindex(id)
 redis.call('HDEL',KEYS[1],id)
 redis.call('ZREM',KEYS[2],id)
 redis.call('DECRBY',KEYS[3],string.len(value))
 if count==1 then redis.call('DEL',unpack(KEYS)) end
 return false
end
if redis.call('HGET',KEYS[5],id)~=ARGV[2] then
 if redis.call('HGET',KEYS[4],ARGV[2])==id then redis.call('HDEL',KEYS[4],ARGV[2]) end
 return false
end
return {id,value}
`)

func (s *RedisOutputAuthorities) Delete(ctx context.Context, record OutputAuthorityRecord) error {
	if s == nil || s.client == nil {
		return ErrUnavailable
	}
	if ctx.Err() != nil || record.ID == "" || record.ID != outputAuthorityID(record) {
		return ErrDenied
	}
	encoded, _ := json.Marshal(record)
	return deleteOutputScript.Run(ctx, s.client, s.keys(record.NodeID), record.ID, string(encoded)).Err()
}

func (s *RedisOutputAuthorities) Register(ctx context.Context, record OutputAuthorityRecord) (string, error) {
	if s == nil || s.client == nil {
		return "", ErrUnavailable
	}
	if ctx.Err() != nil || !validOutputAuthority(record, s.now()) {
		return "", ErrDenied
	}
	record.ID = outputAuthorityID(record)
	record.Revision = rand.Text()
	encoded, _ := json.Marshal(record)
	ok, err := registerOutputScript.Run(ctx, s.client, s.keys(record.NodeID), s.now().Unix(), record.ID, string(encoded), record.ExpiresAt, maxOutputAuthorities, maxOutputAuthorityBytes, record.LookupKey).Int()
	if err != nil || ok != 1 {
		return "", ErrUnavailable
	}
	return record.ID, nil
}

func (s *RedisOutputAuthorities) Find(ctx context.Context, node int, key string) (OutputAuthorityRecord, bool, error) {
	if s == nil || s.client == nil {
		return OutputAuthorityRecord{}, false, ErrUnavailable
	}
	if ctx.Err() != nil || node <= 0 || !validOutputLookupKey(key) {
		return OutputAuthorityRecord{}, false, ErrDenied
	}
	values, err := findOutputScript.Run(ctx, s.client, s.keys(node), s.now().Unix(), key, maxOutputAuthorities, maxOutputAuthorityBytes).StringSlice()
	if errors.Is(err, redis.Nil) {
		return OutputAuthorityRecord{}, false, nil
	}
	if err != nil || len(values) != 2 || len(values[1]) > maxOutputAuthorityBytes {
		return OutputAuthorityRecord{}, false, ErrUnavailable
	}
	var record OutputAuthorityRecord
	if err = json.Unmarshal([]byte(values[1]), &record); err != nil {
		return OutputAuthorityRecord{}, false, ErrUnavailable
	}
	// An older rolling writer may replace an indexed record with its legacy
	// JSON, which intentionally has no lookup key. Treat it as an indexed miss.
	if record.LookupKey == "" && record.ID == values[0] && record.NodeID == node && validOutputAuthority(record, s.now()) {
		return OutputAuthorityRecord{}, false, nil
	}
	if record.LookupKey != key || record.ID != values[0] || record.NodeID != node || !validOutputAuthority(record, s.now()) {
		return OutputAuthorityRecord{}, false, ErrUnavailable
	}
	return record, true, nil
}
func (s *RedisOutputAuthorities) List(ctx context.Context, node int) ([]OutputAuthorityRecord, error) {
	if s == nil || s.client == nil {
		return nil, ErrUnavailable
	}
	if ctx.Err() != nil || node <= 0 {
		return nil, ErrDenied
	}
	values, err := listOutputScript.Run(ctx, s.client, s.keys(node), s.now().Unix(), maxOutputAuthorities, maxOutputAuthorityBytes).StringSlice()
	if err != nil {
		return nil, ErrUnavailable
	}
	out := make([]OutputAuthorityRecord, 0, len(values))
	var bytes int
	for _, value := range values {
		bytes += len(value)
		if bytes > maxOutputAuthorityBytes {
			return nil, ErrUnavailable
		}
		var record OutputAuthorityRecord
		if err := json.Unmarshal([]byte(value), &record); err != nil || record.NodeID != node || !validOutputAuthority(record, s.now()) || record.ID == "" {
			return nil, errors.New("invalid stored output authority")
		}
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Service) currentOutputs(ctx context.Context, node Node) (map[string]bool, error) {
	if s.options.OutputAuthorities == nil {
		return map[string]bool{}, nil
	}
	snapshot, err := s.readOutputSnapshot(ctx, node)
	if err != nil {
		return nil, err
	}
	return s.outputsForSnapshot(ctx, node, snapshot)
}

// Registration normally needs no sweep. On a hard quota failure, reclaim
// definitively retired authorities once before refusing a new viewer.
func (s *Service) registerOutputAuthority(ctx context.Context, node Node, record OutputAuthorityRecord) (string, error) {
	id, err := s.options.OutputAuthorities.Register(ctx, record)
	if !errors.Is(err, ErrUnavailable) {
		return id, err
	}
	if _, sweepErr := s.currentOutputs(ctx, node); sweepErr != nil {
		return "", sweepErr
	}
	return s.options.OutputAuthorities.Register(ctx, record)
}
