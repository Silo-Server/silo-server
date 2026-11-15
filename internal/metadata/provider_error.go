package metadata

import (
	"regexp"
	"strings"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	httpRateLimitCodeRE = regexp.MustCompile(`\b429\b`)
	httpUnauthorizedRE  = regexp.MustCompile(`\b401\b`)
	httpForbiddenRE     = regexp.MustCompile(`\b403\b`)
)

// ProviderErrorClass is the provider-agnostic retry disposition shared by
// enrichment domains.
type ProviderErrorClass string

const (
	ProviderErrorTransient   ProviderErrorClass = "transient"
	ProviderErrorRateLimited ProviderErrorClass = "rate_limited"
	ProviderErrorPermanent   ProviderErrorClass = "permanent"
)

// ProviderErrorPolicy adjusts how ClassifyProviderError reads untyped errors.
// The zero value is the default policy.
type ProviderErrorPolicy struct {
	// ForbiddenIsRateLimit classifies an untyped HTTP 403 or "forbidden" in
	// the error text as rate limited instead of permanent. Some upstream APIs
	// (Apple's iTunes Search API among them) answer throttling with 403, and a
	// provider that forwards that status as plain text gives the host no way
	// to tell a throttle from a ban. A 401 is about credentials and stays
	// permanent, as do typed gRPC PermissionDenied and Unauthenticated.
	ForbiddenIsRateLimit bool
}

// ClassifyProviderError prefers typed gRPC status codes and falls back to the
// text emitted by HTTP/native providers. retryAfter is populated when a gRPC
// ResourceExhausted response carries RetryInfo.
func ClassifyProviderError(err error) (class ProviderErrorClass, retryAfter time.Duration) {
	return ProviderErrorPolicy{}.Classify(err)
}

// Classify is ClassifyProviderError under policy p.
func (p ProviderErrorPolicy) Classify(err error) (class ProviderErrorClass, retryAfter time.Duration) {
	if err == nil {
		return ProviderErrorTransient, 0
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		return p.combine(joined.Unwrap())
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok && wrapped.Unwrap() != nil {
		class, retryAfter := p.Classify(wrapped.Unwrap())
		if class != ProviderErrorTransient {
			return class, retryAfter
		}
		// Native HTTP providers often add their status only in a wrapping
		// message. Let that text strengthen an otherwise-transient leaf.
		if wrapperClass := p.classifyText(err.Error()); wrapperClass != ProviderErrorTransient {
			return wrapperClass, 0
		}
		return class, retryAfter
	}

	if grpcStatus, ok := status.FromError(err); ok {
		switch grpcStatus.Code() {
		case codes.ResourceExhausted:
			for _, detail := range grpcStatus.Details() {
				if retry, ok := detail.(*errdetails.RetryInfo); ok && retry.GetRetryDelay() != nil {
					return ProviderErrorRateLimited, retry.GetRetryDelay().AsDuration()
				}
			}
			return ProviderErrorRateLimited, 0
		case codes.InvalidArgument,
			codes.NotFound,
			codes.PermissionDenied,
			codes.Unauthenticated,
			codes.FailedPrecondition,
			codes.Unimplemented:
			return ProviderErrorPermanent, 0
		case codes.OK:
			return ProviderErrorTransient, 0
		case codes.Unknown:
			// A joined or native error may not preserve a typed status. Fall
			// through to the complete error text below.
		default:
			return ProviderErrorTransient, 0
		}
	}

	return p.classifyText(err.Error()), 0
}

func (p ProviderErrorPolicy) combine(errs []error) (ProviderErrorClass, time.Duration) {
	if len(errs) == 0 {
		return ProviderErrorTransient, 0
	}
	allPermanent := true
	hasRateLimit := false
	var longestRetryAfter time.Duration
	for _, err := range errs {
		class, retryAfter := p.Classify(err)
		switch class {
		case ProviderErrorRateLimited:
			hasRateLimit = true
			if retryAfter > longestRetryAfter {
				longestRetryAfter = retryAfter
			}
		case ProviderErrorTransient:
			allPermanent = false
		}
	}
	if hasRateLimit {
		return ProviderErrorRateLimited, longestRetryAfter
	}
	if allPermanent {
		return ProviderErrorPermanent, 0
	}
	return ProviderErrorTransient, 0
}

func (p ProviderErrorPolicy) classifyText(message string) ProviderErrorClass {
	msg := strings.ToLower(message)
	switch {
	case strings.Contains(msg, "resourceexhausted"),
		strings.Contains(msg, "resource exhausted"),
		httpRateLimitCodeRE.MatchString(msg),
		strings.Contains(msg, "rate limit"),
		strings.Contains(msg, "ratelimit"),
		strings.Contains(msg, "too many requests"),
		strings.Contains(msg, "quota"):
		return ProviderErrorRateLimited
	case strings.Contains(msg, "invalidargument"),
		strings.Contains(msg, "invalid argument"),
		strings.Contains(msg, "notfound"),
		strings.Contains(msg, "not found"),
		strings.Contains(msg, "permissiondenied"),
		strings.Contains(msg, "permission denied"),
		strings.Contains(msg, "unauthenticated"),
		strings.Contains(msg, "unauthorized"),
		httpUnauthorizedRE.MatchString(msg),
		strings.Contains(msg, "failedprecondition"),
		strings.Contains(msg, "failed precondition"),
		strings.Contains(msg, "unimplemented"),
		strings.Contains(msg, "not implemented"):
		return ProviderErrorPermanent
	case httpForbiddenRE.MatchString(msg),
		strings.Contains(msg, "forbidden"):
		if p.ForbiddenIsRateLimit {
			return ProviderErrorRateLimited
		}
		return ProviderErrorPermanent
	default:
		return ProviderErrorTransient
	}
}
