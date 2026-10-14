package workercontrolcore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type authorityFinder interface {
	Find(context.Context, int, string) (OutputAuthorityRecord, bool, error)
}

func TestOutputAuthorityLookupKeyBindsFullClaimsAndAssignment(t *testing.T) {
	claims := streamtoken.Claims{OriginCredential: streamtoken.OriginCredential{OriginLoginSessionID: "login"}, UserID: 7, ProfileID: "profile", MediaFileID: 8, SessionID: "session", TranscodeNode: "https://worker.example.test", DownloadArtifactID: "artifact", DownloadArtifactRowID: "row", DownloadArtifactSize: 100, DownloadExecutionFingerprint: "fingerprint"}
	key := OutputAuthorityLookupKey(claims, 9, "artifact", 1, "epoch")
	changedTime := claims
	changedTime.IssuedAt, changedTime.ExpiresAt = jwt.NewNumericDate(time.Now()), jwt.NewNumericDate(time.Now().Add(time.Hour))
	changedTime.TranscodeNode = "HTTPS://WORKER.example.test:0443/"
	if OutputAuthorityLookupKey(changedTime, 9, "artifact", 1, "epoch") != key {
		t.Fatal("JWT times or equivalent canonical origin fragmented lookup")
	}
	for name, mutate := range map[string]func(*streamtoken.Claims){
		"login":       func(c *streamtoken.Claims) { c.OriginLoginSessionID += "other" },
		"api_key":     func(c *streamtoken.Claims) { c.OriginAPIKeyID = 123 },
		"user":        func(c *streamtoken.Claims) { c.UserID++ },
		"profile":     func(c *streamtoken.Claims) { c.ProfileID += "other" },
		"file":        func(c *streamtoken.Claims) { c.MediaFileID++ },
		"session":     func(c *streamtoken.Claims) { c.SessionID += "other" },
		"origin":      func(c *streamtoken.Claims) { c.TranscodeNode = "https://other.example.test" },
		"row":         func(c *streamtoken.Claims) { c.DownloadArtifactRowID += "other" },
		"size":        func(c *streamtoken.Claims) { c.DownloadArtifactSize++ },
		"fingerprint": func(c *streamtoken.Claims) { c.DownloadExecutionFingerprint += "other" },
	} {
		other := claims
		mutate(&other)
		if OutputAuthorityLookupKey(other, 9, "artifact", 1, "epoch") == key {
			t.Fatal("lookup omitted claim field", name)
		}
	}
	for _, candidate := range []string{OutputAuthorityLookupKey(claims, 10, "artifact", 1, "epoch"), OutputAuthorityLookupKey(claims, 9, "other", 1, "epoch"), OutputAuthorityLookupKey(claims, 9, "artifact", 2, "epoch"), OutputAuthorityLookupKey(claims, 9, "artifact", 1, "rotated")} {
		if candidate == key {
			t.Fatal("lookup omitted assignment or signing epoch")
		}
	}
}

func lookupRedis(t *testing.T) (*RedisOutputAuthorities, *redis.Client) {
	t.Helper()
	raw := os.Getenv("SILO_TEST_REDIS_URL")
	if raw == "" {
		t.Skip("SILO_TEST_REDIS_URL is not set")
	}
	options, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	prefix := "silo:output-lookup-test:" + uuid.NewString() + ":"
	t.Cleanup(func() {
		keys, err := client.Keys(context.Background(), prefix+"*").Result()
		if err == nil && len(keys) != 0 {
			_ = client.Del(context.Background(), keys...).Err()
		}
	})
	return NewRedisOutputAuthorities(client, prefix), client
}

func TestOutputAuthorityLookupIndexLifecycle(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		s := NewMemoryOutputAuthorities()
		now := time.Now()
		s.now = func() time.Time { return now }
		testOutputLookupLifecycle(t, s, &now)
	})
	t.Run("redis", func(t *testing.T) {
		s, client := lookupRedis(t)
		now := time.Now()
		s.now = func() time.Time { return now }
		testOutputLookupLifecycle(t, s, &now)
		for _, key := range s.keys(7) {
			if exists, err := client.Exists(t.Context(), key).Result(); err != nil || exists != 0 {
				t.Fatalf("last-record expiry stranded registry key: %s exists=%d err=%v", key, exists, err)
			}
		}
	})
}

func testOutputLookupLifecycle(t *testing.T, registry OutputAuthorities, now *time.Time) {
	t.Helper()
	finder := registry.(OutputAuthorityFinder)
	keyA, keyB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	record := OutputAuthorityRecord{LookupKey: keyA, Token: "token-a", NodeID: 7, WorkID: "artifact", Generation: 1, Epoch: "epoch", ExpiresAt: now.Add(time.Hour).Unix()}
	register := func(r OutputAuthorityRecord) OutputAuthorityRecord {
		t.Helper()
		if _, err := registry.Register(t.Context(), r); err != nil {
			t.Fatal(err)
		}
		found, ok, err := finder.Find(t.Context(), r.NodeID, r.LookupKey)
		if err != nil || !ok {
			t.Fatalf("missing indexed registration: found=%v err=%v", ok, err)
		}
		return found
	}
	first := register(record)
	renewed := register(record)
	if first.Revision == renewed.Revision {
		t.Fatal("same-ID renewal lost exact CAS revision")
	}
	if err := registry.Delete(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if got, found, err := finder.Find(t.Context(), 7, keyA); err != nil || !found || got != renewed {
		t.Fatal("stale CAS cleanup erased the renewed index")
	}
	newToken := record
	newToken.Token = "token-b"
	newer := register(newToken)
	if err := registry.Delete(t.Context(), renewed); err != nil {
		t.Fatal(err)
	}
	if got, found, err := finder.Find(t.Context(), 7, keyA); err != nil || !found || got != newer {
		t.Fatal("deleting a prior token erased its successor's forward pointer")
	}
	newToken.LookupKey = keyB
	register(newToken)
	if _, found, err := finder.Find(t.Context(), 7, keyA); err != nil || found {
		t.Fatal("same-ID lookup-key change retained its old binding")
	}
	newToken.LookupKey = ""
	if _, err := registry.Register(t.Context(), newToken); err != nil {
		t.Fatal(err)
	}
	if _, found, err := finder.Find(t.Context(), 7, keyB); err != nil || found {
		t.Fatal("rolling registration without lookup key retained an old binding")
	}
	current := register(record)
	for range 8 {
		observed := current
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		wg.Go(func() { _, err := registry.Register(t.Context(), record); errs <- err })
		wg.Go(func() { errs <- registry.Delete(t.Context(), observed) })
		wg.Wait()
		for range 2 {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
		var found bool
		var err error
		current, found, err = finder.Find(t.Context(), 7, keyA)
		if err != nil || !found {
			t.Fatalf("concurrent registration/CAS cleanup lost index: found=%v err=%v", found, err)
		}
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := finder.Find(canceled, 7, keyA); err == nil {
		t.Fatal("canceled lookup succeeded")
	}
	*now = now.Add(2 * time.Hour)
	if _, found, err := finder.Find(t.Context(), 7, keyA); err != nil || found {
		t.Fatal("expired indexed record survived its customer horizon")
	}
	if records, err := registry.List(t.Context(), 7); err != nil || len(records) != 0 {
		t.Fatalf("expiry left retained records: count=%d err=%v", len(records), err)
	}
}

func TestRedisOutputAuthorityLookupNeverScansOrDecodesUnrelatedRecords(t *testing.T) {
	s, client := lookupRedis(t)
	record := OutputAuthorityRecord{LookupKey: strings.Repeat("a", 64), Token: "target-token", NodeID: 7, WorkID: "artifact", Generation: 1, Epoch: "epoch", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	if _, err := s.Register(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	for i := range 128 {
		other := record
		other.Token = fmt.Sprintf("unrelated-%d-%s", i, strings.Repeat("x", 1024))
		other.LookupKey = fmt.Sprintf("%064x", i+1)
		if _, err := s.Register(t.Context(), other); err != nil {
			t.Fatal(err)
		}
	}
	// Keep accounting exact while making one unrelated record undecodable.
	values, err := client.HGetAll(t.Context(), s.keys(7)[0]).Result()
	if err != nil {
		t.Fatal(err)
	}
	for id, value := range values {
		if id != outputAuthorityID(record) {
			if err := client.HSet(t.Context(), s.keys(7)[0], id, strings.Repeat("!", len(value))).Err(); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	username, password := "lookup-"+uuid.NewString(), uuid.NewString()
	if err := client.Do(t.Context(), "ACL", "SETUSER", username, "on", ">"+password, "~"+s.prefix+"*", "+@all", "-hvals", "-hgetall", "-hscan", "-zrange", "-zrangebyscore", "-zrevrange", "-scan", "-keys").Err(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Do(context.Background(), "ACL", "DELUSER", username).Err() }()
	options := *client.Options()
	options.Username, options.Password = username, password
	limited := redis.NewClient(&options)
	defer func() { _ = limited.Close() }()
	finder := NewRedisOutputAuthorities(limited, s.prefix)
	for range 32 {
		got, found, err := finder.Find(t.Context(), 7, record.LookupKey)
		if err != nil || !found || got.Token != record.Token {
			t.Fatalf("targeted lookup scanned or decoded unrelated private data: found=%v err=%v", found, err)
		}
	}
	if _, err := s.List(t.Context(), 7); err == nil {
		t.Fatal("malformed unrelated record fixture was not effective")
	}
	count, _ := client.HLen(t.Context(), s.keys(7)[0]).Result()
	for _, key := range s.keys(7)[3:] {
		indexed, err := client.HLen(t.Context(), key).Result()
		if err != nil || indexed > count {
			t.Fatalf("index cardinality exceeded bounded records: count=%d indexed=%d err=%v", count, indexed, err)
		}
	}
	for _, key := range s.keys(7) {
		ttl, err := client.PTTL(t.Context(), key).Result()
		if err != nil || ttl <= 0 || ttl > time.Hour+time.Second {
			t.Fatalf("index/accounting key lost bounded record TTL: ttl=%s err=%v", ttl, err)
		}
	}
}

func TestIssueOutputGrantPublishesVerifiedLookupIdentity(t *testing.T) {
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	public, _, _ := ed25519.GenerateKey(rand.Reader)
	node := Node{ID: 7, URL: "https://worker.example.test", PublicKey: public, Enabled: true}
	store := NewMemoryStore()
	a, err := store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: 7, WorkID: "artifact", Path: preparePath})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.BindInputs(t.Context(), 7, a.WorkID, a.Generation, map[string]string{receiptOriginal: "original", receiptWorker: "worker"}); err != nil {
		t.Fatal(err)
	}
	if err = store.Complete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err = store.Activate(t.Context(), 7, a.WorkID, a.Generation); err != nil {
		t.Fatal(err)
	}
	registry := NewMemoryOutputAuthorities()
	claims := streamtoken.Claims{OriginCredential: streamtoken.OriginCredential{OriginLoginSessionID: "login"}, DownloadArtifactID: a.WorkID, DownloadArtifactRowID: "row", DownloadExecutionFingerprint: "original", TranscodeNode: node.URL, RoutingExecutionNodeID: 7, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}
	service, err := New(Options{Origin: "https://core.example.test", PrivateKey: private, Nodes: testNodes{[]Node{node}}, Store: store, OutputAuthorities: registry, VerifyToken: func(context.Context, string) (*streamtoken.Claims, error) { copy := claims; return &copy, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.IssueOutputGrant(t.Context(), "verified-customer-token", node.URL, "secret"); err != nil {
		t.Fatal(err)
	}
	key := OutputAuthorityLookupKey(claims, 7, a.WorkID, a.Generation, workeridentity.OutputEpoch("secret"))
	if record, found, err := registry.Find(t.Context(), 7, key); err != nil || !found || record.Token != "verified-customer-token" {
		t.Fatalf("verified output grant omitted reusable exact index: %+v found=%v err=%v", record, found, err)
	}
}

func TestRedisOutputAuthorityLookupMissesAndCorruptionFailClosed(t *testing.T) {
	for _, name := range []string{"missing-forward", "missing-reverse", "missing-record", "legacy-record", "missing-bytes", "missing-expiry", "malformed-json", "wrong-record-id", "wrong-lookup-key", "wrong-node"} {
		t.Run(name, func(t *testing.T) {
			s, client := lookupRedis(t)
			r := OutputAuthorityRecord{LookupKey: strings.Repeat("a", 64), Token: "target-token", NodeID: 7, WorkID: "artifact", Generation: 1, Epoch: "epoch", ExpiresAt: time.Now().Add(time.Hour).Unix()}
			if _, err := s.Register(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			r, found, err := s.Find(t.Context(), 7, r.LookupKey)
			if err != nil || !found {
				t.Fatal("missing target fixture")
			}
			keys := s.keys(7)
			wantMiss := false
			switch name {
			case "missing-forward":
				err = client.Del(t.Context(), keys[3]).Err()
				wantMiss = true
			case "missing-reverse":
				err = client.Del(t.Context(), keys[4]).Err()
				wantMiss = true
			case "missing-record":
				err = client.HDel(t.Context(), keys[0], r.ID).Err()
				wantMiss = true
			case "missing-bytes":
				err = client.Del(t.Context(), keys[2]).Err()
			case "missing-expiry":
				err = client.Del(t.Context(), keys[1]).Err()
			default:
				copy := r
				switch name {
				case "legacy-record":
					copy.LookupKey = ""
					wantMiss = true
				case "wrong-record-id":
					copy.ID = strings.Repeat("0", 64)
				case "wrong-lookup-key":
					copy.LookupKey = strings.Repeat("b", 64)
				case "wrong-node":
					copy.NodeID++
				}
				encoded, _ := json.Marshal(copy)
				if name == "malformed-json" {
					encoded = []byte("!")
				}
				_, err = client.TxPipelined(t.Context(), func(pipe redis.Pipeliner) error {
					pipe.HSet(t.Context(), keys[0], r.ID, string(encoded))
					pipe.Set(t.Context(), keys[2], len(encoded), 0)
					return nil
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			_, found, err = s.Find(t.Context(), 7, r.LookupKey)
			if found || (wantMiss && err != nil) || (!wantMiss && err == nil) {
				t.Fatalf("unsafe indexed result: found=%v miss=%v err=%v", found, wantMiss, err)
			}
		})
	}
}

func TestOutputAuthorityLookupQuotaAndPrunePreserveSuccessor(t *testing.T) {
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			now := time.Now()
			var registry OutputAuthorities
			if backend == "memory" {
				s := NewMemoryOutputAuthorities()
				s.now = func() time.Time { return now }
				registry = s
			} else {
				s, _ := lookupRedis(t)
				s.now = func() time.Time { return now }
				registry = s
			}
			finder := registry.(OutputAuthorityFinder)
			r := OutputAuthorityRecord{LookupKey: strings.Repeat("a", 64), Token: "older-token", NodeID: 7, WorkID: "artifact", Generation: 1, Epoch: "epoch", ExpiresAt: now.Add(time.Minute).Unix()}
			if _, err := registry.Register(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			r.Token, r.ExpiresAt = "newer-token", now.Add(time.Hour).Unix()
			if _, err := registry.Register(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			now = now.Add(2 * time.Minute)
			if records, err := registry.List(t.Context(), 7); err != nil || len(records) != 1 {
				t.Fatalf("expiry did not prune prior token: count=%d err=%v", len(records), err)
			}
			if got, found, err := finder.Find(t.Context(), 7, r.LookupKey); err != nil || !found || got.Token != "newer-token" {
				t.Fatalf("pruning predecessor erased successor pointer: found=%v err=%v", found, err)
			}
			var denied string
			for i := range 32 {
				r.Token = fmt.Sprintf("%d-%s", i, strings.Repeat("x", 60<<10))
				r.Generation = uint64(i + 2)
				r.LookupKey = fmt.Sprintf("%064x", i+1)
				if _, err := registry.Register(t.Context(), r); err != nil {
					if !errors.Is(err, ErrUnavailable) {
						t.Fatal(err)
					}
					denied = r.LookupKey
					break
				}
			}
			if denied == "" {
				t.Fatal("indexed records bypassed encoded byte quota")
			}
			if _, found, err := finder.Find(t.Context(), 7, denied); err != nil || found {
				t.Fatalf("quota rejection partially published an index: found=%v err=%v", found, err)
			}
			records, err := registry.List(t.Context(), 7)
			if err != nil {
				t.Fatal(err)
			}
			var bytes int
			for _, record := range records {
				raw, _ := json.Marshal(record)
				bytes += len(raw)
			}
			if bytes > maxOutputAuthorityBytes {
				t.Fatal("index metadata escaped record byte accounting")
			}
			switch s := registry.(type) {
			case *MemoryOutputAuthorities:
				if len(s.nodes[7].lookup) > len(records) || s.nodes[7].bytes != bytes {
					t.Fatal("memory index/accounting grew beyond live records")
				}
			case *RedisOutputAuthorities:
				for _, key := range s.keys(7)[3:] {
					count, err := s.client.HLen(t.Context(), key).Result()
					if err != nil || count > int64(len(records)) {
						t.Fatalf("Redis index grew beyond live records: count=%d err=%v", count, err)
					}
				}
				held, err := s.client.Get(t.Context(), s.keys(7)[2]).Int()
				if err != nil || held != bytes {
					t.Fatalf("Redis indexed byte accounting changed: held=%d actual=%d err=%v", held, bytes, err)
				}
			}
		})
	}
}

func TestOutputAuthorityLookupUsesExactPrivateRecord(t *testing.T) {
	registry := NewMemoryOutputAuthorities()
	finder, ok := any(registry).(authorityFinder)
	if !ok {
		t.Fatal("bounded output authorities cannot perform a targeted lookup")
	}
	record := OutputAuthorityRecord{Token: "customer-token", NodeID: 7, WorkID: "artifact", Generation: 1, Epoch: "epoch", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	encoded, _ := json.Marshal(record)
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	key := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	fields["lookup_key"] = key
	encoded, _ = json.Marshal(fields)
	if err := json.Unmarshal(encoded, &record); err != nil {
		t.Fatal(err)
	}
	id, err := registry.Register(t.Context(), record)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := finder.Find(t.Context(), record.NodeID, key)
	if err != nil || !found || got.ID != id || got.Token != record.Token {
		t.Fatalf("targeted lookup lost exact retained record: %+v found=%v err=%v", got, found, err)
	}
}

// This is the exact three-key delete script from published 25df1944.
var lookupLegacyDeleteOutputScript = redis.NewScript(`
local current=redis.call('HGET',KEYS[1],ARGV[1])
if not current or current~=ARGV[2] then return 0 end
if not redis.call('GET',KEYS[3]) or redis.call('EXISTS',KEYS[2])==0 then
 return redis.error_reply('output authority accounting unavailable')
end
redis.call('HDEL',KEYS[1],ARGV[1])
redis.call('ZREM',KEYS[2],ARGV[1])
redis.call('DECRBY',KEYS[3],string.len(current))
if redis.call('HLEN',KEYS[1])==0 then redis.call('DEL',KEYS[1],KEYS[2],KEYS[3]) end
return 1
`)

// This combines the exact published three-key prune/register scripts.
var lookupLegacyRegisterOutputScript = redis.NewScript(`
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

local old=redis.call('HGET',KEYS[1],ARGV[2])
local held=tonumber(redis.call('GET',KEYS[3]) or '0')
local delta=string.len(ARGV[3])-(old and string.len(old) or 0)
if (not old and redis.call('HLEN',KEYS[1])>=tonumber(ARGV[5])) or held+delta>tonumber(ARGV[6]) then return 0 end
redis.call('HSET',KEYS[1],ARGV[2],ARGV[3])
redis.call('ZADD',KEYS[2],ARGV[4],ARGV[2])
redis.call('INCRBY',KEYS[3],delta)
local last=redis.call('ZREVRANGE',KEYS[2],0,0,'WITHSCORES')
local ttl=(tonumber(last[2])-tonumber(ARGV[1]))*1000+1000
for _,key in ipairs(KEYS) do redis.call('PEXPIRE',key,ttl) end
return 1
`)

func TestRedisOutputAuthorityLookupLegacyWritersKeepIndexBounded(t *testing.T) {
	registry, client := lookupRedis(t)
	ctx := t.Context()
	now := time.Now()
	anchor := OutputAuthorityRecord{LookupKey: strings.Repeat("f", 64), Token: "anchor", NodeID: 7, WorkID: "artifact", Generation: 1, Epoch: "epoch", ExpiresAt: now.Add(24 * time.Hour).Unix()}
	if _, err := registry.Register(ctx, anchor); err != nil {
		t.Fatal(err)
	}
	keys := registry.keys(7)
	anchorID := outputAuthorityID(anchor)
	anchorRaw, err := client.HGet(ctx, keys[0], anchorID).Result()
	if err != nil {
		t.Fatal(err)
	}
	for i := range maxOutputAuthorities + 1 {
		record := anchor
		record.Token = fmt.Sprintf("rolling-token-%d", i)
		record.LookupKey = fmt.Sprintf("%064x", i+1)
		id, err := registry.Register(ctx, record)
		if err != nil {
			t.Fatalf("new register %d: %v", i, err)
		}
		// An old replica freshly verifies this same token and re-registers it.
		record.ID = id
		record.Revision = fmt.Sprintf("old-writer-revision-%d", i)
		record.LookupKey = ""
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		registered, err := lookupLegacyRegisterOutputScript.Run(ctx, client, keys[:3], now.Unix(), id, string(encoded), record.ExpiresAt, maxOutputAuthorities, maxOutputAuthorityBytes).Int()
		if err != nil || registered != 1 {
			t.Fatalf("old register %d = %d, %v", i, registered, err)
		}
		// Its later exact-record sweep retires that record using the old script.
		removed, err := lookupLegacyDeleteOutputScript.Run(ctx, client, keys[:3], id, string(encoded)).Int()
		if err != nil || removed != 1 {
			t.Fatalf("old delete %d = %d, %v", i, removed, err)
		}
	}
	count, err := client.HLen(ctx, keys[0]).Result()
	if err != nil {
		t.Fatal(err)
	}
	forward, err := client.HLen(ctx, keys[3]).Result()
	if err != nil {
		t.Fatal(err)
	}
	reverse, err := client.HLen(ctx, keys[4]).Result()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("after actual old/new scripts: live records=%d forward=%d reverse=%d hard record limit=%d", count, forward, reverse, maxOutputAuthorities)
	if forward > maxOutputAuthorities || reverse > maxOutputAuthorities {
		t.Fatalf("mixed rolling writers let indexes exceed the hard record bound: records=%d forward=%d reverse=%d", count, forward, reverse)
	}
	if raw, err := client.HGet(ctx, keys[0], anchorID).Result(); err != nil || raw != anchorRaw {
		t.Fatalf("rolling index repair changed the long-open anchor authority: err=%v", err)
	}
	if held, err := client.Get(ctx, keys[2]).Int(); err != nil || held != len(anchorRaw) {
		t.Fatalf("rolling index repair changed retained byte accounting: held=%d actual=%d err=%v", held, len(anchorRaw), err)
	}
	if expiry, err := client.ZScore(ctx, keys[1], anchorID).Result(); err != nil || int64(expiry) != anchor.ExpiresAt {
		t.Fatalf("rolling index repair changed long-open authority expiry: expires=%v err=%v", expiry, err)
	}
	current := anchor
	current.Token, current.LookupKey = "current-managed-token", strings.Repeat("e", 64)
	if _, err := registry.Register(ctx, current); err != nil {
		t.Fatal(err)
	}
	if got, found, err := registry.Find(ctx, 7, current.LookupKey); err != nil || !found || got.Token != current.Token {
		t.Fatalf("managed publication after rolling cleanup was not indexed: found=%v err=%v", found, err)
	}
	for _, key := range keys[3:] {
		if indexed, err := client.HLen(ctx, key).Result(); err != nil || indexed > 2 {
			t.Fatalf("managed publication left more index entries than live records: indexed=%d err=%v", indexed, err)
		}
	}
}

func TestRedisOutputAuthorityLookupRejectedRegistrationDoesNotResetIndex(t *testing.T) {
	registry, client := lookupRedis(t)
	ctx := t.Context()
	r := OutputAuthorityRecord{Token: strings.Repeat("q", 60<<10), NodeID: 7, WorkID: "artifact", Generation: 1, Epoch: "epoch", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	for i := range 32 {
		r.Generation = uint64(i + 1)
		r.LookupKey = fmt.Sprintf("%064x", i+1)
		if _, err := registry.Register(ctx, r); err != nil {
			if !errors.Is(err, ErrUnavailable) {
				t.Fatal(err)
			}
			break
		}
	}
	keys := registry.keys(7)
	// Model the optional entries left behind by an old three-key deletion.
	if err := client.HSet(ctx, keys[3], strings.Repeat("f", 64), "retired-id").Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.HSet(ctx, keys[4], "retired-id", strings.Repeat("f", 64)).Err(); err != nil {
		t.Fatal(err)
	}
	forward, err := client.HGetAll(ctx, keys[3]).Result()
	if err != nil {
		t.Fatal(err)
	}
	reverse, err := client.HGetAll(ctx, keys[4]).Result()
	if err != nil {
		t.Fatal(err)
	}
	r.Generation += 100
	r.LookupKey = strings.Repeat("e", 64)
	if _, err := registry.Register(ctx, r); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("fixture did not reject the over-budget managed registration: %v", err)
	}
	afterForward, err := client.HGetAll(ctx, keys[3]).Result()
	if err != nil {
		t.Fatal(err)
	}
	afterReverse, err := client.HGetAll(ctx, keys[4]).Result()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(forward, afterForward) || !reflect.DeepEqual(reverse, afterReverse) {
		t.Fatal("quota refusal reset optional indices before accepting a publication")
	}
}
