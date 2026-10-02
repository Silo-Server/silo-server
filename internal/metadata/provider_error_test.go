package metadata

import (
	"errors"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestClassifyProviderErrorUsesTypedGRPCCodes(t *testing.T) {
	rateLimited, err := status.New(codes.ResourceExhausted, "busy").WithDetails(&errdetails.RetryInfo{
		RetryDelay: durationpb.New(17 * time.Minute),
	})
	if err != nil {
		t.Fatalf("WithDetails: %v", err)
	}

	for name, tc := range map[string]struct {
		err       error
		wantClass ProviderErrorClass
		wantRetry time.Duration
	}{
		"rate limited":    {rateLimited.Err(), ProviderErrorRateLimited, 17 * time.Minute},
		"unauthenticated": {status.Error(codes.Unauthenticated, "bad token"), ProviderErrorPermanent, 0},
		"not found":       {status.Error(codes.NotFound, "missing"), ProviderErrorPermanent, 0},
		"unavailable":     {status.Error(codes.Unavailable, "offline"), ProviderErrorTransient, 0},
	} {
		t.Run(name, func(t *testing.T) {
			gotClass, gotRetry := ClassifyProviderError(tc.err)
			if gotClass != tc.wantClass || gotRetry != tc.wantRetry {
				t.Fatalf("ClassifyProviderError() = (%q, %v), want (%q, %v)", gotClass, gotRetry, tc.wantClass, tc.wantRetry)
			}
		})
	}
}

func TestClassifyProviderErrorFallsBackToNativeErrorText(t *testing.T) {
	for message, want := range map[string]ProviderErrorClass{
		"HTTP 429 too many requests":   ProviderErrorRateLimited,
		"HTTP 403 forbidden":           ProviderErrorPermanent,
		"connection reset by peer":     ProviderErrorTransient,
		"provider item OL1429M failed": ProviderErrorTransient,
		"provider item GB403M failed":  ProviderErrorTransient,
	} {
		got, _ := ClassifyProviderError(errors.New(message))
		if got != want {
			t.Errorf("ClassifyProviderError(%q) = %q, want %q", message, got, want)
		}
	}
}

func TestClassifyProviderErrorCombinesJoinedProviderFailures(t *testing.T) {
	rateLimited, err := status.New(codes.ResourceExhausted, "busy").WithDetails(&errdetails.RetryInfo{
		RetryDelay: durationpb.New(23 * time.Minute),
	})
	if err != nil {
		t.Fatalf("WithDetails: %v", err)
	}

	class, retryAfter := ClassifyProviderError(errors.Join(
		status.Error(codes.Unavailable, "first provider offline"),
		rateLimited.Err(),
	))
	if class != ProviderErrorRateLimited || retryAfter != 23*time.Minute {
		t.Fatalf("joined rate limit = (%q, %v), want (%q, %v)", class, retryAfter, ProviderErrorRateLimited, 23*time.Minute)
	}

	class, retryAfter = ClassifyProviderError(errors.Join(
		status.Error(codes.NotFound, "one provider has no route"),
		status.Error(codes.Unavailable, "another provider is offline"),
	))
	if class != ProviderErrorTransient || retryAfter != 0 {
		t.Fatalf("mixed permanent/transient = (%q, %v), want (%q, 0)", class, retryAfter, ProviderErrorTransient)
	}
}
