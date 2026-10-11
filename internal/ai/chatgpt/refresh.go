package chatgpt

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// Keep retry classification private; provider response bodies may contain secrets.
type transientProviderError struct{ retryAfter time.Duration }

func (*transientProviderError) Error() string { return ErrProvider.Error() }
func (*transientProviderError) Unwrap() error { return ErrProvider }

func tokenRetryAfter(value string) time.Duration {
	if delay, err := time.ParseDuration(value + "s"); err == nil && delay > 0 {
		return delay
	}
	if deadline, err := http.ParseTime(value); err == nil {
		return max(0, time.Until(deadline))
	}
	return 0
}

func waitProviderRetry(ctx context.Context, attempt int, retryAfter time.Duration) bool {
	delay := max(time.Second<<attempt, retryAfter)
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= delay {
		return false
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Refresh stays serialized and bounded by the independent rotation deadline.
// Authorization-code exchanges are consumed once and never use this retry path.
func (s *Service) refreshExchange(ctx context.Context, form url.Values) (tokenResponse, error) {
	for attempt := range 3 {
		if err := ctx.Err(); err != nil {
			return tokenResponse{}, errors.Join(ErrProvider, err)
		}
		tokens, err := s.exchange(ctx, form)
		temporary, retry := errors.AsType[*transientProviderError](err)
		if !retry || attempt == 2 {
			return tokens, err
		}
		if !waitProviderRetry(ctx, attempt, temporary.retryAfter) {
			return tokens, errors.Join(err, ctx.Err())
		}
	}
	return tokenResponse{}, ErrProvider
}
