package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// SignInSourcePolicy bounds password attempts at a locked name from one
// client address the account is known to sign in from: the same ten
// attempts, then fifteen minutes locked, as the name's own budget.
var SignInSourcePolicy = AttemptPolicy{MaxAttempts: 10, Window: 15 * time.Minute}

// SignInTrustTTL is how long a client address stays known for a name after
// the account last signed in or refreshed a session from it.
const SignInTrustTTL = 30 * 24 * time.Hour

// SignInTrustMaxAddresses is how many known addresses one name keeps: the
// ones it signed in or refreshed from most recently. The cap bounds what
// refreshes can store, since a client on a network the server trusts as a
// proxy can name a new address on each one, and those refreshes only write
// their own account's names.
const SignInTrustMaxAddresses = 32

// signInTrustCallTimeout bounds one trust lookup or write, so a Redis outage
// delays a sign-in or refresh by at most this much.
const signInTrustCallTimeout = time.Second

// SignInLimiter is the password sign-in budget (auth.SignInAttemptLimiter).
//
// Every attempt spends the name's budget (SignInNamePolicy) while it lasts.
// Once it is used the name is locked for attempts from unknown addresses,
// which bounds guessing at one name from many addresses. An address the
// account signed in or refreshed a session from within SignInTrustTTL then
// spends a budget of its own (SignInSourcePolicy), so a stranger who keeps the
// name locked does not lock its people out where they usually sign in, and
// guessing from such an address stays bounded too. Addresses count as the
// per-address request budgets do (SourceKey).
//
// Counts and known addresses live in Redis when the deployment has Redis,
// shared by every node, and in process memory only on a Redis-less (single
// node) deployment, where a restart forgets known addresses until the
// account's devices sign in or refresh again.
type SignInLimiter struct {
	names   *AttemptLimiter
	sources *AttemptLimiter
	trust   signInTrustBackend
}

var _ auth.SignInAttemptLimiter = (*SignInLimiter)(nil)

// signInTrustBackend keeps each name's known addresses: for a name key, a
// set of address entries (signInSourcePair), each with an expiry, at most
// SignInTrustMaxAddresses of them, dropping the one that expires first.
type signInTrustBackend interface {
	known(ctx context.Context, entry signInTrustEntry) (bool, error)
	remember(ctx context.Context, entries []signInTrustEntry, ttl time.Duration) error
}

// signInTrustEntry is one address known for one name.
type signInTrustEntry struct {
	nameKey string
	pair    string
}

// NewSignInLimiter returns the per-name password sign-in limiter, shared
// through Redis on the same terms as the profile PIN limiter. A process
// builds one and gives it to every auth.Service it runs, so the native,
// Jellyfin and Audiobookshelf listeners spend one count per name.
func NewSignInLimiter(client *redis.Client) *SignInLimiter {
	if client != nil {
		return &SignInLimiter{
			names:   NewRedisAttemptLimiter(client, SignInNamePolicy),
			sources: NewRedisAttemptLimiter(client, SignInSourcePolicy),
			trust:   redisSignInTrust{client: client, now: time.Now},
		}
	}
	return NewMemorySignInLimiter(SignInNamePolicy, SignInSourcePolicy)
}

// NewMemorySignInLimiter returns a process-local SignInLimiter with the given
// name and known-address policies.
func NewMemorySignInLimiter(names, sources AttemptPolicy) *SignInLimiter {
	return &SignInLimiter{
		names:   NewMemoryAttemptLimiter(names),
		sources: NewMemoryAttemptLimiter(sources),
		trust:   newMemorySignInTrust(time.Now),
	}
}

// Reserve spends one attempt at nameKey from source. See SignInLimiter.
func (l *SignInLimiter) Reserve(ctx context.Context, nameKey, source string) (auth.SignInLane, time.Duration, bool) {
	nameRetry, ok := l.names.Reserve(ctx, nameKey)
	if ok {
		return auth.SignInLaneName, 0, true
	}
	if ctx.Err() != nil {
		return auth.SignInLaneName, 0, false
	}
	pair := signInSourcePair(nameKey, source)
	if pair == "" || !l.known(ctx, signInTrustEntry{nameKey: nameKey, pair: pair}) {
		return auth.SignInLaneName, nameRetry, false
	}
	sourceRetry, ok := l.sources.Reserve(ctx, "signin_source:"+pair)
	if !ok {
		// The name may unlock before this address does.
		return auth.SignInLaneSource, min(nameRetry, sourceRetry), false
	}
	return auth.SignInLaneSource, 0, true
}

// Reset clears the count lane spent at nameKey from source.
func (l *SignInLimiter) Reset(ctx context.Context, nameKey, source string, lane auth.SignInLane) {
	if lane == auth.SignInLaneName {
		l.names.Reset(ctx, nameKey)
		return
	}
	if pair := signInSourcePair(nameKey, source); pair != "" {
		l.sources.Reset(ctx, "signin_source:"+pair)
	}
}

// Trust makes source known for every name in nameKeys for SignInTrustTTL.
// A failure is logged: the names then lock for source like for any other
// address.
func (l *SignInLimiter) Trust(ctx context.Context, nameKeys []string, source string) {
	entries := make([]signInTrustEntry, 0, len(nameKeys))
	for _, nameKey := range nameKeys {
		if pair := signInSourcePair(nameKey, source); pair != "" {
			entries = append(entries, signInTrustEntry{nameKey: nameKey, pair: pair})
		}
	}
	if len(entries) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, signInTrustCallTimeout)
	defer cancel()
	if err := l.trust.remember(ctx, entries, SignInTrustTTL); err != nil {
		slog.WarnContext(ctx, "sign-in known address write failed", "component", "ratelimit", "error", err)
	}
}

// known reports whether entry's address is known for its name. A backend
// error counts as unknown: the name's lock then holds, as it did before the
// address was known.
func (l *SignInLimiter) known(ctx context.Context, entry signInTrustEntry) bool {
	ctx, cancel := context.WithTimeout(ctx, signInTrustCallTimeout)
	defer cancel()
	ok, err := l.trust.known(ctx, entry)
	if err != nil {
		slog.WarnContext(ctx, "sign-in known address lookup failed", "component", "ratelimit", "error", err)
		return false
	}
	return ok
}

// signInSourcePair keys one name and one client address together, hashed so
// neither appears in keys or logs. It is empty when the address is unknown.
func signInSourcePair(nameKey, source string) string {
	if source == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(nameKey + "\x00" + SourceKey(source)))
	return hex.EncodeToString(sum[:])
}

// --- memory backend ---

type memorySignInTrust struct {
	mu        sync.Mutex
	names     map[string]map[string]time.Time // name key -> pair -> expiry
	now       func() time.Time
	lastSweep time.Time
}

func newMemorySignInTrust(now func() time.Time) *memorySignInTrust {
	return &memorySignInTrust{names: map[string]map[string]time.Time{}, now: now}
}

func (m *memorySignInTrust) known(_ context.Context, entry signInTrustEntry) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now().Before(m.names[entry.nameKey][entry.pair]), nil
}

func (m *memorySignInTrust) remember(_ context.Context, entries []signInTrustEntry, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	// Names whose addresses all expired are dropped lazily, at most once a
	// day, so the map stays bounded without a background goroutine.
	if now.Sub(m.lastSweep) >= 24*time.Hour {
		for nameKey, pairs := range m.names {
			dropExpired(pairs, now)
			if len(pairs) == 0 {
				delete(m.names, nameKey)
			}
		}
		m.lastSweep = now
	}
	for _, entry := range entries {
		pairs := m.names[entry.nameKey]
		if pairs == nil {
			pairs = map[string]time.Time{}
			m.names[entry.nameKey] = pairs
		}
		pairs[entry.pair] = now.Add(ttl)
		dropExpired(pairs, now)
		for len(pairs) > SignInTrustMaxAddresses {
			delete(pairs, firstToExpire(pairs))
		}
	}
	return nil
}

func dropExpired(pairs map[string]time.Time, now time.Time) {
	for pair, expires := range pairs {
		if !now.Before(expires) {
			delete(pairs, pair)
		}
	}
}

func firstToExpire(pairs map[string]time.Time) string {
	var first string
	var firstExpires time.Time
	for pair, expires := range pairs {
		if first == "" || expires.Before(firstExpires) {
			first, firstExpires = pair, expires
		}
	}
	return first
}

// --- Redis backend ---

// redisSignInTrust keeps each name's known addresses in one sorted set,
// scored by expiry in Unix milliseconds.
type redisSignInTrust struct {
	client *redis.Client
	now    func() time.Time
}

func redisSignInTrustKey(nameKey string) string { return "silo:signin_trust:" + nameKey }

func (r redisSignInTrust) known(ctx context.Context, entry signInTrustEntry) (bool, error) {
	expires, err := r.client.ZScore(ctx, redisSignInTrustKey(entry.nameKey), entry.pair).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return expires > float64(r.now().UnixMilli()), nil
}

func (r redisSignInTrust) remember(ctx context.Context, entries []signInTrustEntry, ttl time.Duration) error {
	now := r.now()
	expires := float64(now.Add(ttl).UnixMilli())
	pipe := r.client.Pipeline()
	for _, entry := range entries {
		key := redisSignInTrustKey(entry.nameKey)
		// GT: a write that lands after a later one never shortens the expiry.
		pipe.ZAddGT(ctx, key, redis.Z{Score: expires, Member: entry.pair})
		pipe.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(now.UnixMilli(), 10))
		// Keep the addresses that expire last.
		pipe.ZRemRangeByRank(ctx, key, 0, -SignInTrustMaxAddresses-1)
		pipe.Expire(ctx, key, ttl)
	}
	_, err := pipe.Exec(ctx)
	return err
}
