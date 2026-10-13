package workercontrolcore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const maxOutputAuthorities = 4096
const maxOutputAuthorityBytes = 1 << 20
const maxOutputTokenBytes = 64 << 10

// OutputAuthorityRecord is retained only at the core. Snapshots expose its
// opaque ID; the original customer token never crosses the worker boundary.
type OutputAuthorityRecord struct {
	ID         string `json:"id"`
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
}

func outputAuthorityID(r OutputAuthorityRecord) string {
	value, _ := json.Marshal([]any{"silo/worker-output-authority/v1", r.Token, r.NodeID, r.WorkID, r.Generation, r.Epoch})
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
func validOutputAuthority(r OutputAuthorityRecord, now time.Time) bool {
	return r.NodeID > 0 && validWork(r.WorkID) && r.Generation > 0 && r.Generation < 1<<53 && r.Epoch != "" && len(r.Epoch) <= 128 && r.Token != "" && len(r.Token) <= maxOutputTokenBytes && r.ExpiresAt > now.Unix() && r.ExpiresAt < 1<<53 && (r.ID == "" || r.ID == outputAuthorityID(r))
}

type outputRecords struct {
	values map[string]OutputAuthorityRecord
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
		entries = &outputRecords{values: make(map[string]OutputAuthorityRecord)}
		s.nodes[node] = entries
	}
	for id, record := range entries.values {
		if record.ExpiresAt <= now.Unix() {
			encoded, _ := json.Marshal(record)
			entries.bytes -= len(encoded)
			delete(entries.values, id)
		}
	}
	return entries
}
func (s *MemoryOutputAuthorities) Register(ctx context.Context, record OutputAuthorityRecord) (string, error) {
	if ctx.Err() != nil || !validOutputAuthority(record, s.now()) {
		return "", ErrDenied
	}
	record.ID = outputAuthorityID(record)
	encoded, _ := json.Marshal(record)
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.prune(record.NodeID, s.now())
	if _, exists := entries.values[record.ID]; exists {
		return record.ID, nil
	}
	if len(entries.values) >= maxOutputAuthorities || entries.bytes+len(encoded) > maxOutputAuthorityBytes {
		return "", ErrUnavailable
	}
	entries.values[record.ID] = record
	entries.bytes += len(encoded)
	return record.ID, nil
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
	return []string{base + ":records", base + ":expiry", base + ":bytes"}
}

const pruneOutputsLua = `
local count=redis.call('HLEN',KEYS[1])
if count>0 and (not redis.call('GET',KEYS[3]) or redis.call('EXISTS',KEYS[2])==0) then
 return redis.error_reply('output authority accounting unavailable')
end
if count==0 then redis.call('DEL',KEYS[2],KEYS[3]) end
local stale=redis.call('ZRANGEBYSCORE',KEYS[2],'-inf',ARGV[1])
for _,id in ipairs(stale) do
 local value=redis.call('HGET',KEYS[1],id)
 if value then redis.call('DECRBY',KEYS[3],string.len(value)) end
 redis.call('HDEL',KEYS[1],id)
 redis.call('ZREM',KEYS[2],id)
end
`

var registerOutputScript = redis.NewScript(pruneOutputsLua + `
if redis.call('HEXISTS',KEYS[1],ARGV[2])==1 then return 1 end
local held=tonumber(redis.call('GET',KEYS[3]) or '0')
if redis.call('HLEN',KEYS[1])>=tonumber(ARGV[5]) or held+string.len(ARGV[3])>tonumber(ARGV[6]) then return 0 end
redis.call('HSET',KEYS[1],ARGV[2],ARGV[3])
redis.call('ZADD',KEYS[2],ARGV[4],ARGV[2])
redis.call('INCRBY',KEYS[3],string.len(ARGV[3]))
local last=redis.call('ZREVRANGE',KEYS[2],0,0,'WITHSCORES')
local ttl=(tonumber(last[2])-tonumber(ARGV[1]))*1000+1000
for _,key in ipairs(KEYS) do redis.call('PEXPIRE',key,ttl) end
return 1
`)
var listOutputScript = redis.NewScript(pruneOutputsLua + `
if redis.call('HLEN',KEYS[1])>tonumber(ARGV[2]) or tonumber(redis.call('GET',KEYS[3]) or '0')>tonumber(ARGV[3]) then return redis.error_reply('output authority bounds exceeded') end
return redis.call('HVALS',KEYS[1])
`)

func (s *RedisOutputAuthorities) Register(ctx context.Context, record OutputAuthorityRecord) (string, error) {
	if s == nil || s.client == nil {
		return "", ErrUnavailable
	}
	if ctx.Err() != nil || !validOutputAuthority(record, s.now()) {
		return "", ErrDenied
	}
	record.ID = outputAuthorityID(record)
	encoded, _ := json.Marshal(record)
	ok, err := registerOutputScript.Run(ctx, s.client, s.keys(record.NodeID), s.now().Unix(), record.ID, string(encoded), record.ExpiresAt, maxOutputAuthorities, maxOutputAuthorityBytes).Int()
	if err != nil || ok != 1 {
		return "", ErrUnavailable
	}
	return record.ID, nil
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

func (s *Service) preparedOutputs(ctx context.Context, node Node) (map[string]bool, error) {
	out := make(map[string]bool)
	if s.options.OutputAuthorities == nil {
		return out, nil
	}
	records, err := s.options.OutputAuthorities.List(ctx, node.ID)
	if err != nil {
		return nil, err
	}
	if len(records) > 0 && s.options.VerifyToken == nil {
		return nil, ErrUnavailable
	}
	for _, record := range records {
		claims, err := s.options.VerifyToken(ctx, record.Token)
		if err != nil || claims == nil || claims.DownloadArtifactID == "" || claims.TranscodeNode != node.URL || claims.RoutingExecutionNodeID != 0 && claims.RoutingExecutionNodeID != node.ID || ExtractOutputWork(claims) != record.WorkID || claims.ExpiresAt == nil || claims.ExpiresAt.Unix() != record.ExpiresAt || !s.WorkActive(ctx, node.ID, record.WorkID, record.Generation) {
			continue
		}
		out[record.ID] = true
	}
	return out, nil
}
