package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// SignInAttemptLimiter bounds password attempts per sign-in name
// (*ratelimit.AttemptLimiter under ratelimit.SignInNamePolicy; this package
// cannot import ratelimit). Reserve counts an attempt before the password is
// checked and refuses one while the name is locked; Reset clears the count
// after a correct password.
type SignInAttemptLimiter interface {
	Reserve(ctx context.Context, key string) (retryAfter time.Duration, ok bool)
	Reset(ctx context.Context, key string)
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

// reserveSignIn counts one password attempt against name's budget. When the
// name is locked it returns a *SignInThrottledError and the caller must not
// check the password; a request whose context is done gets the context's
// error instead.
func (s *Service) reserveSignIn(ctx context.Context, name string) error {
	if s.signInAttempts == nil {
		return nil
	}
	if retryAfter, ok := s.signInAttempts.Reserve(ctx, signInAttemptKey(name)); !ok {
		if err := ctx.Err(); err != nil {
			return err
		}
		return &SignInThrottledError{RetryAfter: retryAfter}
	}
	return nil
}

// finishSignIn clears name's budget once the password matched: the sign-in
// succeeded, or a compatibility sign-in found a temporary password it cannot
// change. Every other outcome, outages included, keeps the attempt counted.
// The count is cleared even when the client has already hung up.
func (s *Service) finishSignIn(ctx context.Context, name string, err error) {
	if s.signInAttempts == nil || (err != nil && !errors.Is(err, ErrPasswordChangeRequired)) {
		return
	}
	s.signInAttempts.Reset(context.WithoutCancel(ctx), signInAttemptKey(name))
}
