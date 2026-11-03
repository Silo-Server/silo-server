package s3client

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/smithy-go"
	"github.com/aws/smithy-go/middleware"
)

// retryMaxAttempts keeps the SDK default so a dead endpoint still fails fast.
// Callers that must outlast a long throttle, such as storage transitions, wait
// on ErrThrottled themselves.
const retryMaxAttempts = retry.DefaultMaxAttempts

// throttleCodes are rate-limit error codes the SDK does not recognize on its
// own. A plain 503 ServiceUnavailable is left out: it signals an outage, and
// the SDK already retries it briefly.
var throttleCodes = map[string]struct{}{
	// Cloudflare R2 and MinIO, with HTTP 429.
	"TooManyRequests": {},
	// MinIO, with HTTP 503.
	"SlowDownRead":  {},
	"SlowDownWrite": {},
	"ServerBusy":    {},
	// Wasabi, with HTTP 403.
	"RequestRateLimitExceeded": {},
}

// isThrottle reports a provider rate limit: HTTP 429, which also covers bodiless
// HEAD responses, or a known throttle code.
func isThrottle(err error) bool {
	var status interface{ HTTPStatusCode() int }
	if errors.As(err, &status) && status.HTTPStatusCode() == http.StatusTooManyRequests {
		return true
	}
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	code := apiErr.ErrorCode()
	if _, ok := throttleCodes[code]; ok {
		return true
	}
	_, ok := retry.DefaultThrottleErrorCodes[code]
	return ok
}

// ErrThrottled wraps a provider rate limit that outlasted every SDK retry, so
// long-running callers can pause and try again instead of failing.
var ErrThrottled = errors.New("s3: storage provider is rate limiting requests")

// markThrottled runs outside the retry middleware and tags a throttle that
// survived every attempt. The SDK error stays in the chain for callers that
// inspect it.
func markThrottled(stack *middleware.Stack) error {
	return stack.Initialize.Add(middleware.InitializeMiddlewareFunc("SiloMarkThrottled", func(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
		out, metadata, err := next.HandleInitialize(ctx, in)
		if err != nil && isThrottle(err) {
			err = fmt.Errorf("%w: %w", ErrThrottled, err)
		}
		return out, metadata, err
	}), middleware.Before)
}

var retryThrottles = retry.IsErrorRetryableFunc(func(err error) aws.Ternary {
	if isThrottle(err) {
		return aws.TrueTernary
	}
	return aws.UnknownTernary
})

// throttleExemptRetryer retries throttles without spending the retry quota.
// A long throttle would otherwise drain it, after which later throttles fail
// at once without any backoff. Outages and server errors still spend it, so a
// failing backend stops being retried.
type throttleExemptRetryer struct{ aws.RetryerV2 }

func (r throttleExemptRetryer) GetRetryToken(ctx context.Context, opErr error) (func(error) error, error) {
	if isThrottle(opErr) {
		return func(error) error { return nil }, nil
	}
	return r.RetryerV2.GetRetryToken(ctx, opErr)
}

// newRetryer backs off on provider rate limits. The SDK's adaptive mode is
// not used: its first throttle can shrink the attempt bucket below one token
// and block every later request on the client.
func newRetryer() aws.Retryer {
	return throttleExemptRetryer{retry.NewStandard(func(o *retry.StandardOptions) {
		o.MaxAttempts = retryMaxAttempts
		o.Retryables = append([]retry.IsErrorRetryable{retryThrottles}, o.Retryables...)
	})}
}
