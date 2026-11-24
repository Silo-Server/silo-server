package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

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

// signInAttemptKey is the budget key of a sign-in name. Names match accounts
// trimmed and without regard to case (usernames and emails are citext), so
// they count that way too; a name typed as an email and the same account's
// username are two names. The hash keeps keys short and names out of the
// limiter's logs.
func signInAttemptKey(name string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(NormalizeUsername(name))))
	return "signin_name:" + hex.EncodeToString(sum[:])
}

// signInSpend is what one sign-in spent of a name's budget: the name and its
// key, the client address it came from, and whether an attempt spent the
// name's own budget.
type signInSpend struct {
	name      string
	key       string
	source    string
	spentName bool
	// derived marks a budget name taken from what was typed rather than the
	// name that signed in (Jellyfin's "alice#Kids"), which can be another
	// account's.
	derived bool
}

func newSignInSpend(ctx context.Context, name string) *signInSpend {
	return &signInSpend{name: name, key: signInAttemptKey(name), source: signInSource(ctx)}
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
	if !s.ownsSignInBudget(ctx, spend, user) {
		// Clearing or extending another account's budget would let whoever
		// holds this one guess at that account without end, alternating with
		// sign-ins of their own.
		s.signInAttempts.Trust(ctx, signInTrustKeys("", user), spend.source)
		return
	}
	if spend.spentName {
		s.signInAttempts.Reset(ctx, spend.key, spend.source, SignInLaneName)
	}
	if spend.source != "" {
		s.signInAttempts.Reset(ctx, spend.key, spend.source, SignInLaneSource)
	}
	s.signInAttempts.Trust(ctx, signInTrustKeys(spend.key, user), spend.source)
}

// ownsSignInBudget reports whether the name whose budget spend spent belongs
// to user, the account that signed in, so its success may clear and extend
// that budget. One of user's own names does. So does an ASCII name no
// account answers to, such as user's directory username. A name that finds an
// account does not: a directory identity of a separate account with the same
// login name, or a Jellyfin "alice#Kids" signing in to the account named
// that, whose budget name is derived from the typed text and counts only when
// it is one of user's own names. A failed lookup counts as another's.
func (s *Service) ownsSignInBudget(ctx context.Context, spend *signInSpend, user *models.User) bool {
	if user == nil {
		return false
	}
	if slices.Contains(signInTrustKeys("", user), spend.key) {
		return true
	}
	if spend.derived || s.users == nil {
		return false
	}
	// The budget key folds case in Go and the lookup folds it in the
	// database (citext), which agree on ASCII under any collation but not
	// beyond it: under the C collation "Älice" finds no "älice" account
	// though both have one key. So only an ASCII name can count as no one
	// else's.
	if strings.ContainsFunc(spend.name, func(r rune) bool { return r > unicode.MaxASCII }) {
		return false
	}
	_, err := LookupLogin(ctx, s.users, spend.name)
	return IsNotFound(err)
}

// trustSignInSource records the request's address as one user's names sign
// in from, after a sign-in that spent no budget or a session refresh.
func (s *Service) trustSignInSource(ctx context.Context, user *models.User) {
	if s.signInAttempts == nil || user == nil {
		return
	}
	s.signInAttempts.Trust(context.WithoutCancel(ctx), signInTrustKeys("", user), signInSource(ctx))
}

// signInTrustKeys are the budget keys of typed (when set) and of user's
// username and email, without repeats.
func signInTrustKeys(typed string, user *models.User) []string {
	keys := make([]string, 0, 3)
	if typed != "" {
		keys = append(keys, typed)
	}
	if user == nil {
		return keys
	}
	for _, name := range []string{user.Username, user.Email} {
		if strings.TrimSpace(name) == "" {
			continue
		}
		if key := signInAttemptKey(name); !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}
	return keys
}
