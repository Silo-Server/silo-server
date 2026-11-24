package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/netaccess"
)

// SignInLane names the budget a password attempt spent.
type SignInLane uint8

const (
	// SignInLaneName is the budget shared by every attempt at a name.
	SignInLaneName SignInLane = iota
	// SignInLaneSource is the budget of one client address the name is
	// known to sign in from, spent only while the name's own budget is used.
	SignInLaneSource
)

// SignInAttemptLimiter bounds password attempts per sign-in name
// (*ratelimit.SignInLimiter; this package cannot import ratelimit).
//
// Every attempt spends the name's budget while it lasts. Once it is used,
// the name is locked for attempts from unknown addresses, but an address the
// account is known to sign in from (Trust) spends a budget of its own
// instead. Guessing stays bounded per name and per known address, and
// whoever spends the name's budget from elsewhere cannot lock the account's
// people out where they usually sign in.
type SignInAttemptLimiter interface {
	// Reserve counts one attempt at the name keyed nameKey from the client
	// address source ("" when unknown) before its password is checked. It
	// reports the lane spent, or false with the time left on the lock.
	Reserve(ctx context.Context, nameKey, source string) (lane SignInLane, retryAfter time.Duration, ok bool)
	// Reset clears the lane's count after the password matched.
	Reset(ctx context.Context, nameKey, source string, lane SignInLane)
	// Trust records that the names keyed nameKeys sign in from source.
	Trust(ctx context.Context, nameKeys []string, source string)
}

// SignInThrottledError refuses a password attempt for a name that has used
// its attempt budget. No password was checked. Every name, with or without
// an account, gets the same budget and the same refusal, so the refusal does
// not tell a stranger which names exist.
type SignInThrottledError struct {
	// RetryAfter is the time left on the name's lock.
	RetryAfter time.Duration
}

func (e *SignInThrottledError) Error() string {
	return "too many failed sign-ins for this name"
}

// SetSignInAttempts makes password sign-in spend l's per-name budget. One
// process gives every Service it runs the same limiter. Nil turns the budget
// off.
func (s *Service) SetSignInAttempts(l SignInAttemptLimiter) {
	s.signInAttempts = l
}

// signInNameKeys returns the budget keys of names, in order. Names match
// accounts trimmed and without regard to case (usernames and emails are
// citext), so they count that way too; a name typed as an email and the same
// account's username are two names. The database folds the case, with the
// lower() that citext compares by: Go's case tables cannot match every
// collation (the C collation folds ASCII letters only, a Turkish one folds
// "I" to "ı"), and two accounts must never share a key. It reports false when
// the database could not fold them; the keys then fold in Go, which still
// counts every attempt but must not decide whose budget a name is. The hash
// keeps keys short and names out of the limiter's logs.
func (s *Service) signInNameKeys(ctx context.Context, names ...string) ([]string, bool) {
	trimmed := make([]string, len(names))
	for i, name := range names {
		trimmed[i] = NormalizeUsername(name)
	}
	folded, ok := s.foldSignInNames(ctx, trimmed)
	if !ok {
		folded = make([]string, len(trimmed))
		for i, name := range trimmed {
			folded[i] = strings.ToLower(name)
		}
	}
	keys := make([]string, len(folded))
	for i, name := range folded {
		keys[i] = signInKey(name)
	}
	return keys, ok
}

// signInKey is the budget key of a name the database folded.
func signInKey(folded string) string {
	sum := sha256.Sum256([]byte(folded))
	return "signin_name:" + hex.EncodeToString(sum[:])
}

func (s *Service) foldSignInNames(ctx context.Context, names []string) ([]string, bool) {
	if s.users == nil || len(names) == 0 {
		return nil, false
	}
	if folded, ok := s.signInFolds.get(names); ok {
		return folded, true
	}
	ctx, cancel := context.WithTimeout(ctx, signInLookupTimeout)
	defer cancel()
	var folded []string
	err := s.users.pool.QueryRow(ctx,
		`SELECT array_agg(lower(name) ORDER BY i) FROM unnest($1::text[]) WITH ORDINALITY AS t(name, i)`,
		names).Scan(&folded)
	if err != nil || len(folded) != len(names) {
		slog.WarnContext(ctx, "sign-in budget names could not be folded by the database", "component", "auth", "error", err)
		return nil, false
	}
	s.signInFolds.put(names, folded)
	return folded, true
}

// signInLookupTimeout bounds each database lookup a sign-in's budget makes,
// which can run detached from the request after the password matched.
const signInLookupTimeout = 2 * time.Second

// signInFoldCacheMax bounds the folded names a Service remembers, and
// signInFoldCacheMaxName the length of a name it remembers: usernames and
// emails are at most 255 characters, so a longer name finds no account and
// is folded each time instead of pinning memory.
const (
	signInFoldCacheMax     = 4096
	signInFoldCacheMaxName = 256
)

// signInFoldCache remembers how the database folded names, so attempts that
// keep hitting one locked name do not each query it. A name's fold never
// changes; the cache starts over when full.
type signInFoldCache struct {
	mu     sync.Mutex
	folded map[string]string
}

func (c *signInFoldCache) get(names []string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	folded := make([]string, len(names))
	for i, name := range names {
		f, ok := c.folded[name]
		if !ok {
			return nil, false
		}
		folded[i] = f
	}
	return folded, true
}

func (c *signInFoldCache) put(names, folded []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.folded == nil || len(c.folded)+len(names) > signInFoldCacheMax {
		c.folded = make(map[string]string, len(names))
	}
	for i, name := range names {
		if len(name) <= signInFoldCacheMaxName {
			c.folded[name] = folded[i]
		}
	}
}

// signInSpend is what one sign-in spent of a name's budget: the name and its
// key, the client address it came from, and whether an attempt spent the
// name's own budget.
type signInSpend struct {
	name      string
	key       string
	source    string
	spentName bool
	// folded reports that the database folded the key (signInNameKeys), so
	// the key may decide whose budget it is.
	folded bool
	// derived marks a budget name taken from what was typed rather than the
	// name that signed in (Jellyfin's "alice#Kids"), which can be another
	// account's.
	derived bool
}

// newSignInSpend starts a sign-in's spend of the budget of name. loginName is
// the name the sign-in looks an account up by, when it differs from name.
func (s *Service) newSignInSpend(ctx context.Context, name, loginName string) *signInSpend {
	names := []string{name}
	if loginName != "" && loginName != name {
		names = append(names, loginName)
	}
	keys, folded := s.signInNameKeys(ctx, names...)
	return &signInSpend{
		name:    name,
		key:     keys[0],
		source:  signInSource(ctx),
		folded:  folded,
		derived: len(keys) > 1 && keys[1] != keys[0],
	}
}

// signInSource is the client address a sign-in counts from and makes known,
// or "" when the request's address does not say where the person is, so
// only the name's own budget applies. An overlay request counts from the
// peer its network access provider vouched for, since the provider's own
// connection carries every overlay user, kept apart from the same address
// on another overlay or the default path (overlays such as Tailscale use the
// carrier-grade NAT range). An address a forwarder sent for a client the
// resolver could not identify is the forwarder's, and a loopback address is
// a proxy, plugin or relay on this host: strangers share both.
func signInSource(ctx context.Context) string {
	if path := netaccess.PathFromContext(ctx); !path.IsDefault() {
		if path.Peer.IsValid() {
			return "overlay:" + strconv.Itoa(path.InstallationID) + ":" + path.Peer.String()
		}
		return ""
	}
	if clientip.ForwardedClientUnknown(ctx) {
		return ""
	}
	ip := clientip.FromContext(ctx)
	if addr, err := netip.ParseAddr(ip); err != nil || addr.IsLoopback() || addr.IsUnspecified() {
		return ""
	}
	return ip
}

// reserveSignIn counts one password attempt against spend's name. When the
// name is locked for the request's address it returns a
// *SignInThrottledError and the caller must not check the password; a
// request whose context is done gets the context's error instead.
func (s *Service) reserveSignIn(ctx context.Context, spend *signInSpend) error {
	if s.signInAttempts == nil {
		return nil
	}
	lane, retryAfter, ok := s.signInAttempts.Reserve(ctx, spend.key, spend.source)
	if !ok {
		if err := ctx.Err(); err != nil {
			return err
		}
		return &SignInThrottledError{RetryAfter: retryAfter}
	}
	if lane == SignInLaneName {
		spend.spentName = true
	}
	return nil
}

// finishSignIn clears the counts once the password matched (the sign-in
// succeeded, or a compatibility sign-in found a temporary password it cannot
// change), and records the address as one the name and user's own names
// sign in from. The address's own count is cleared whichever lane the
// sign-in spent, so a later lock of the name does not find it used up. The
// name's count is cleared only when the sign-in spent it: a success from a
// known address leaves the lock someone else caused in place, which keeps
// their guessing bounded. Every other outcome, outages included, keeps the
// attempts counted. The counts are cleared even when the client has already
// hung up.
func (s *Service) finishSignIn(ctx context.Context, spend *signInSpend, user *models.User, err error) {
	if s.signInAttempts == nil || (err != nil && !errors.Is(err, ErrPasswordChangeRequired)) {
		return
	}
	ctx = context.WithoutCancel(ctx)
	own, ownFolded := s.ownSignInKeys(ctx, user)
	if !ownFolded || !spend.folded || !s.ownsSignInBudget(ctx, spend, own) {
		// Clearing or extending another account's budget would let whoever
		// holds this one guess at that account without end, alternating with
		// sign-ins of their own; so would a key the database did not fold.
		if ownFolded {
			s.signInAttempts.Trust(ctx, own, spend.source)
		}
		return
	}
	if spend.spentName {
		s.signInAttempts.Reset(ctx, spend.key, spend.source, SignInLaneName)
	}
	if spend.source != "" {
		s.signInAttempts.Reset(ctx, spend.key, spend.source, SignInLaneSource)
	}
	if !slices.Contains(own, spend.key) {
		own = append(own, spend.key)
	}
	s.signInAttempts.Trust(ctx, own, spend.source)
}

// ownsSignInBudget reports whether the name whose budget spend spent belongs
// to the account that signed in, whose own budget keys are own, so its
// success may clear and extend that budget. One of the account's own names
// does. So does a name no account answers to, such as its directory
// username: the lookup and the key fold case alike (signInNameKeys). A name
// that finds an account does not: a directory identity of a separate account
// with the same login name, or a Jellyfin "alice#Kids" signing in to the
// account named that, whose budget name is derived from the typed text and
// counts only when it is one of the account's own names. A failed lookup
// counts as another's.
func (s *Service) ownsSignInBudget(ctx context.Context, spend *signInSpend, own []string) bool {
	if slices.Contains(own, spend.key) {
		return true
	}
	if spend.derived || s.users == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, signInLookupTimeout)
	defer cancel()
	_, err := LookupLogin(ctx, s.users, spend.name)
	return IsNotFound(err)
}

// trustSignInSource records the request's address as one user's names sign
// in from, after a sign-in that spent no budget or a session refresh.
func (s *Service) trustSignInSource(ctx context.Context, user *models.User) {
	if s.signInAttempts == nil || user == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	if own, ok := s.ownSignInKeys(ctx, user); ok && len(own) > 0 {
		s.signInAttempts.Trust(ctx, own, signInSource(ctx))
	}
}

// ownSignInKeys are the budget keys of user's username and email, folded by
// the database (signInNameKeys), without repeats, and whether it answered.
// A name signs in to the account LookupLogin finds for it, by username
// first, then by email, and a name that finds another account is left out:
// an account kept from before the single login identity space can have
// another account's username as its email, and that name is the other
// account's to clear or extend.
func (s *Service) ownSignInKeys(ctx context.Context, user *models.User) ([]string, bool) {
	if user == nil || s.users == nil {
		return nil, false
	}
	names := make([]string, 0, 2)
	for _, name := range []string{user.Username, user.Email} {
		if name = NormalizeUsername(name); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, true
	}
	ctx, cancel := context.WithTimeout(ctx, signInLookupTimeout)
	defer cancel()
	var folded []string
	err := s.users.pool.QueryRow(ctx, `
		SELECT coalesce(array_agg(lower(t.name) ORDER BY t.i) FILTER (WHERE coalesce(
			(SELECT u.id FROM users u WHERE u.username = t.name::citext),
			(SELECT u.id FROM users u WHERE u.email = t.name::citext)) = $2), '{}')
		FROM unnest($1::text[]) WITH ORDINALITY AS t(name, i)`,
		names, user.ID).Scan(&folded)
	if err != nil {
		slog.WarnContext(ctx, "sign-in budget names of an account could not be read", "component", "auth", "error", err)
		return nil, false
	}
	keys := make([]string, len(folded))
	for i, name := range folded {
		keys[i] = signInKey(name)
	}
	return slices.Compact(keys), true
}
