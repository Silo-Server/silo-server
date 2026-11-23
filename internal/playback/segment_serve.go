package playback

import (
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/httpstream"
)

// SegmentIncompleteRetryAfterSeconds is the Retry-After a segment route
// advertises when the writer cannot yet vouch for a whole segment. It matches
// the retry delay the other playback routes use for a transient executor
// answer, so hls.js backs off instead of re-requesting the same bytes at once.
const SegmentIncompleteRetryAfterSeconds = 1

// ServeSegmentLease writes the leased segment as one complete representation
// and reports whether the whole representation reached the client.
//
// Two things used to let a partial body end as an ordinary 200. http.ServeContent
// takes the length from the file at serve time, so a name a writer truncated or
// replaced under the lease was announced with the new (short) length and copied
// "completely"; and it ignores a copy error, so a file that shrank mid-copy
// ended as a clean 200 shorter than its own Content-Length. A client cannot tell
// either from a delivered segment: hls.js appends the fragment and waits for
// frames that never come.
//
// The lease's own size is therefore the authority. A name whose size no longer
// matches what was leased is refused as incomplete before any byte is written
// (the caller answers the retry shape), and a 200 whose accepted body is shorter
// than its announced length aborts the connection instead of closing cleanly,
// which the player reads as a failed transfer it retries.
//
// The lease keeps zero-copy: the underlying *os.File still reaches the
// ResponseWriter's ReadFrom, so segments keep using sendfile.
func ServeSegmentLease(w http.ResponseWriter, r *http.Request, lease *SegmentLease) (complete bool, err error) {
	vouched := lease.Info.Size()
	current, seekErr := lease.File.Seek(0, io.SeekEnd)
	if seekErr != nil {
		return false, ErrSegmentIncomplete
	}
	if current != vouched {
		// The writer changed the name after the lease. Its bytes cannot be
		// promised as this segment, so nothing is served from it.
		return false, ErrSegmentIncomplete
	}
	sw := httpstream.NewRollingDeadlineWriter(w)
	http.ServeContent(sw, r, lease.Info.Name(), lease.Info.ModTime(), lease.File)
	if r.Method == http.MethodHead || sw.StatusCode() != http.StatusOK {
		// A HEAD has no body and a 206 is a partial response by design; only a
		// whole-representation 200 can promise the segment.
		return sw.CompletedFullResponse(vouched), nil
	}
	declared, parseErr := strconv.ParseInt(sw.Header().Get("Content-Length"), 10, 64)
	if parseErr == nil && declared > 0 && sw.BytesWritten() != declared {
		outcome := sw.Outcome(r.Context())
		if outcome == httpstream.OutcomeCompleted {
			// Nothing failed on the wire and the body still ended short: the
			// file changed under the lease. This is the case that must never
			// close as success, so it is worth an ERROR line.
			slog.ErrorContext(r.Context(), "playback: segment response ended before its own length",
				"component", "playback",
				"segment", lease.Info.Name(),
				"declared", declared,
				"written", sw.BytesWritten(),
			)
		} else {
			// The player abandoned the transfer (a reloaded level, a seek);
			// the abort below is the ordinary end of an aborted body.
			slog.DebugContext(r.Context(), "playback: segment response ended with the client",
				"component", "playback",
				"segment", lease.Info.Name(),
				"declared", declared,
				"written", sw.BytesWritten(),
				"outcome", outcome,
			)
		}
		panic(http.ErrAbortHandler)
	}
	return sw.CompletedFullResponse(vouched), nil
}
