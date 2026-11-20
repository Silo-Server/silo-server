package playback

import (
	"bytes"
	"io"
	"net/http"
)

// subtitleCueGate holds a windowed text response until its first cue is
// complete, so an extract that fails after writing only the format header
// cannot commit a 200 whose body is just "WEBVTT\n". FFmpeg's WebVTT and ASS
// muxers write their header before they reach the window's first packet; a
// source read that then fails leaves a response a browser accepts as a
// successful, empty subtitle document. The handler can answer retryably only
// while no status has been committed, so the header waits here until a cue
// proves the extract is producing content.
//
// A window that ends without a cue is not a failure by itself: a 120 s slice of
// a track can legitimately hold no dialog. RunExtract reports that as success,
// and the gate then releases the complete empty document. The gate never
// truncates or invents output; it only decides when the bytes already produced
// may reach the response.
//
// The hold is bounded. A document longer than subtitleCueGateMaxBytes without a
// cue is not a header-only response anymore, so it is released and streamed;
// buffering it forever would trade one bad response for an unbounded one.
const subtitleCueGateMaxBytes = 256 << 10

type subtitleCueGate struct {
	w      io.Writer
	format string
	buf    bytes.Buffer
	// released is true once the held bytes have reached w. Before that, w has
	// seen nothing and no status is committed.
	released bool
}

func newSubtitleCueGate(w io.Writer, format string) *subtitleCueGate {
	return &subtitleCueGate{w: w, format: format}
}

// Write buffers output until a cue is present, then releases the buffer and
// streams the rest. It always reports the full write as accepted while
// buffering, because the extractor did hand the bytes over.
func (g *subtitleCueGate) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if g.released {
		return g.w.Write(p)
	}
	_, _ = g.buf.Write(p)
	if g.buf.Len() >= subtitleCueGateMaxBytes || hasCue(g.format, g.buf.Bytes()) {
		return len(p), g.release()
	}
	return len(p), nil
}

func (g *subtitleCueGate) release() error {
	if g.released {
		return nil
	}
	g.released = true
	_, err := g.w.Write(g.buf.Bytes())
	g.buf.Reset()
	return err
}

// commit writes whatever is held as a complete, cue-less document. It is the
// success path: the extraction ended cleanly inside a window with no dialog.
func (g *subtitleCueGate) commit() error {
	return g.release()
}

// finish resolves the gate against the extraction result. A clean extract
// commits whatever the muxer produced (a cue-less window is still a complete
// document); a failed one drops the held bytes so the caller can answer an
// error status. It tolerates a nil gate so the caller keeps one path.
func (g *subtitleCueGate) finish(err error) error {
	if g == nil {
		return err
	}
	if err == nil {
		return g.commit()
	}
	g.abort()
	return err
}

// abort drops the held bytes so a failed extraction can be answered with an
// error status instead of a header-only 200. It must only be called before
// anything reached w.
func (g *subtitleCueGate) abort() {
	g.buf.Reset()
}

// heldBytes is how much output is buffered before the response. A retry after
// a failed attempt must not append to it, or the response would hold two
// attempts' headers.
func (g *subtitleCueGate) heldBytes() int {
	return g.buf.Len()
}

// Flush passes a flush on only once bytes have reached the response.
func (g *subtitleCueGate) Flush() {
	if !g.released {
		return
	}
	if f, ok := g.w.(http.Flusher); ok {
		f.Flush()
	}
}
