package audiobooks

import (
	"testing"
)

func TestClassifyProviderErrorSortsByHowItShouldRetry(t *testing.T) {
	cases := []struct {
		msg  string
		want EnrichmentErrorClass
	}{
		{"HTTP 429 too many requests", EnrichmentErrorRateLimited},
		{"daily quota exceeded", EnrichmentErrorRateLimited},
		{"rate limit reached", EnrichmentErrorRateLimited},
		{"HTTP 403: Forbidden", EnrichmentErrorPermanent},
		{"unauthorized: bad token", EnrichmentErrorPermanent},
		{"connection reset by peer", EnrichmentErrorTransient},
		{"context deadline exceeded", EnrichmentErrorTransient},
	}
	for _, tc := range cases {
		if got := classifyProviderError(errString(tc.msg)); got != tc.want {
			t.Errorf("classifyProviderError(%q) = %q, want %q", tc.msg, got, tc.want)
		}
	}
	if got := classifyProviderError(nil); got != EnrichmentErrorTransient {
		t.Errorf("classifyProviderError(nil) = %q, want transient", got)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
