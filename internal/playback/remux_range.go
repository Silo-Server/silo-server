package playback

import (
	"net/http"
	"strconv"
	"strings"
)

// RemuxRangeQueryParam is the query parameter a progressive remux URL carries
// when its plan negotiated progressive_range_refusal_v1. Its only value is
// RemuxRangeRefuse.
const (
	RemuxRangeQueryParam = "remux_range"
	RemuxRangeRefuse     = "refuse"
)

// RefusesRemuxRange reports whether a progressive remux request must be
// refused rather than streamed: its URL opted in with RemuxRangeQueryParam and
// it asks for bytes the response cannot start at. A remux always streams from
// the URL's seek origin, so the only range it can honor is one that starts at
// byte zero (`bytes=0-` from a browser, `bytes=0-1` from a probe). A request
// without a Range header, or with one that does not parse, streams as before.
//
// Without the opt-in every request streams from the origin and the client
// discards what it already has, which native players rely on to recover a
// dropped connection.
func RefusesRemuxRange(r *http.Request) bool {
	if r.Method != http.MethodGet || r.URL.Query().Get(RemuxRangeQueryParam) != RemuxRangeRefuse {
		return false
	}
	spec, ok := strings.CutPrefix(strings.TrimSpace(r.Header.Get("Range")), "bytes=")
	if !ok {
		return false
	}
	first, _, _ := strings.Cut(spec, ",")
	start, end, ok := strings.Cut(strings.TrimSpace(first), "-")
	if !ok {
		return false
	}
	// A suffix range (`bytes=-500`) names the end of a stream whose length
	// is not known yet.
	if start == "" {
		return strings.TrimSpace(end) != ""
	}
	offset, err := strconv.ParseInt(start, 10, 64)
	return err == nil && offset > 0
}

// RemuxRangeRefusedMessage is the body detail of a refused remux range.
const RemuxRangeRefusedMessage = "A progressive remux cannot resume past its start; reopen the stream at the current position."
