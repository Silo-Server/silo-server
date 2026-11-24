package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
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

type signInTrustBackend interface {
	known(ctx context.Context, key string) (bool, error)
	remember(ctx context.Context, keys []string, ttl time.Duration) error
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
			trust:   redisSignInTrust{client: client},
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
	if pair == "" || !l.known(ctx, pair) {
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
	keys := make([]string, 0, len(nameKeys))
	for _, nameKey := range nameKeys {
		if pair := signInSourcePair(nameKey, source); pair != "" {
			keys = append(keys, pair)
		}
	}
	if len(keys) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, signInTrustCallTimeout)
	defer cancel()
	if err := l.trust.remember(ctx, keys, SignInTrustTTL); err != nil {
		slog.WarnContext(ctx, "sign-in known address write failed", "component", "ratelimit", "error", err)
	}
}

// known reports whether pair's address is known for its name. A backend
// error counts as unknown: the name's lock then holds, as it did before the
// address was known.
func (l *SignInLimiter) known(ctx context.Context, pair string) bool {
	ctx, cancel := context.WithTimeout(ctx, signInTrustCallTimeout)
	defer cancel()
	ok, err := l.trust.known(ctx, pair)
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
	expires   map[string]time.Time
	now       func() time.Time
	lastSweep time.Time
}

func newMemorySignInTrust(now func() time.Time) *memorySignInTrust {
	return &memorySignInTrust{expires: map[string]time.Time{}, now: now}
}

func (m *memorySignInTrust) known(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now().Before(m.expires[key]), nil
}

func (m *memorySignInTrust) remember(_ context.Context, keys []string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	// Expired entries are dropped lazily, at most once a day, so the map
	// stays bounded without a background goroutine.
	if now.Sub(m.lastSweep) >= 24*time.Hour {
		for k, exp := range m.expires {
			if !now.Before(exp) {
				delete(m.expires, k)
			}
		}
		m.lastSweep = now
	}
	for _, key := range keys {
		m.expires[key] = now.Add(ttl)
	}
	return nil
}

// --- Redis backend ---

type redisSignInTrust struct {
	client *redis.Client
}

func redisSignInTrustKey(key string) string { return "silo:signin_trust:" + key }

func (r redisSignInTrust) known(ctx context.Context, key string) (bool, error) {
	n, err := r.client.Exists(ctx, redisSignInTrustKey(key)).Result()
	return n > 0, err
}

func (r redisSignInTrust) remember(ctx context.Context, keys []string, ttl time.Duration) error {
	pipe := r.client.Pipeline()
	for _, key := range keys {
		pipe.Set(ctx, redisSignInTrustKey(key), "1", ttl)
	}
	_, err := pipe.Exec(ctx)
	return err
}
