package jellycompat

import "net/http"

type playbackJobRoundTripper struct {
	base    http.RoundTripper
	observe func(*http.Request)
}

func (t playbackJobRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	t.observe(r)
	return t.base.RoundTrip(r)
}
