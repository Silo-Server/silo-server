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
// byte zero (`bytes=0-` from a browser, `bytes=0-1` from a probe); a range past
// byte zero or a suffix range, anywhere in the set, is refused. A request
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
	// Any range in the set that the response cannot start with refuses the
	// request; browsers send one.
	for _, part := range strings.Split(spec, ",") {
		if remuxRangeNeedsResume(part) {
			return true
		}
	}
	return false
}

// remuxRangeNeedsResume reports whether one range spec asks for bytes past the
// start of the stream: a start past byte zero, or a suffix range (`bytes=-500`),
// which names the end of a stream whose length is not known yet.
func remuxRangeNeedsResume(part string) bool {
	start, end, ok := strings.Cut(strings.TrimSpace(part), "-")
	if !ok {
		return false
	}
	if start == "" {
		return strings.TrimSpace(end) != ""
	}
	offset, err := strconv.ParseInt(start, 10, 64)
	return err == nil && offset > 0
}

// RemuxRangeRefusedMessage is the body detail of a refused remux range.
const RemuxRangeRefusedMessage = "A progressive remux cannot resume past its start; reopen the stream at the current position."
