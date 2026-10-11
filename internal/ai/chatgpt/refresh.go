package chatgpt

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// Keep retry classification private; provider response bodies may contain secrets.
type transientTokenError struct{ retryAfter time.Duration }

func (*transientTokenError) Error() string { return ErrProvider.Error() }
func (*transientTokenError) Unwrap() error { return ErrProvider }

func tokenRetryAfter(value string) time.Duration {
	if delay, err := time.ParseDuration(value + "s"); err == nil && delay > 0 {
		return delay
	}
	if deadline, err := http.ParseTime(value); err == nil {
		return max(0, time.Until(deadline))
	}
	return 0
}

// Refresh stays serialized and bounded by the independent rotation deadline.
// Authorization-code exchanges are consumed once and never use this retry path.
func (s *Service) refreshExchange(ctx context.Context, form url.Values) (tokenResponse, error) {
	for attempt := range 3 {
		if err := ctx.Err(); err != nil {
			return tokenResponse{}, errors.Join(ErrProvider, err)
		}
		tokens, err := s.exchange(ctx, form)
		temporary, retry := errors.AsType[*transientTokenError](err)
		if !retry || attempt == 2 {
			return tokens, err
		}
		delay := max(time.Second<<attempt, temporary.retryAfter)
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= delay {
			return tokens, err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return tokenResponse{}, errors.Join(ErrProvider, ctx.Err())
		case <-timer.C:
		}
	}
	return tokenResponse{}, ErrProvider
}
