package playback

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// isoBox builds one ISO-BMFF box with the given type and body.
func isoBox(typ string, body []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(8+len(body)))
	out = append(out, typ...)
	return append(out, body...)
}

// wholeInitSegment is a complete ftyp+moov init segment with enough body to cut
// a prefix that is still larger than an empty file.
func wholeInitSegment() []byte {
	return append(isoBox("ftyp", make([]byte, 496)), isoBox("moov", make([]byte, 496))...)
}

// A media segment whose temp file the HLS muxer still holds is bytes in
// progress, not a segment: a request that races the restart must not be
// answered 200 with a fragment of it.
func TestOpenSegmentRefusesASegmentWhoseTempFileIsLive(t *testing.T) {
	dir := t.TempDir()
	// 512 bytes cannot be a whole run of 188-byte TS packets, which is what a
	// mid-write prefix looks like.
	if err := os.WriteFile(filepath.Join(dir, "seg_00468.ts"), bytes.Repeat([]byte{0x47}, 512), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "seg_00468.ts.tmp"), bytes.Repeat([]byte{0x47}, 128), 0o600); err != nil {
		t.Fatal(err)
	}
	session := NewTranscodeSessionForTest(dir)

	if lease, err := session.OpenSegment("seg_00468.ts"); !errors.Is(err, ErrSegmentIncomplete) {
		if lease != nil {
			_ = lease.Close()
		}
		t.Fatalf("open during the writer's temp file: err = %v, want ErrSegmentIncomplete", err)
	}

	// Once the muxer renames its temp file into place the segment is whole and
	// is served as before.
	if err := os.Remove(filepath.Join(dir, "seg_00468.ts.tmp")); err != nil {
		t.Fatal(err)
	}
	lease, err := session.OpenSegment("seg_00468.ts")
	if err != nil {
		t.Fatalf("open after the rename: %v", err)
	}
	defer func() { _ = lease.Close() }()
	if got := lease.Info.Size(); got != 512 {
		t.Fatalf("leased size = %d, want 512", got)
	}
}

// FFmpeg truncates and rewrites init.mp4 in place, so a request that races a
// restart can read a prefix of it. A prefix is not servable, because the client
// appends it as the initialization for every following fragment.
func TestOpenSegmentRefusesATruncatedInitSegment(t *testing.T) {
	whole := wholeInitSegment()
	dir := t.TempDir()
	path := filepath.Join(dir, hlsInitSegmentName)
	if err := os.WriteFile(path, whole[:512], 0o600); err != nil {
		t.Fatal(err)
	}
	session := NewTranscodeSessionForTest(dir)
	if lease, err := session.OpenSegment(hlsInitSegmentName); !errors.Is(err, ErrSegmentIncomplete) {
		if lease != nil {
			_ = lease.Close()
		}
		t.Fatalf("open of a truncated init: err = %v, want ErrSegmentIncomplete", err)
	}

	if err := os.WriteFile(path, whole, 0o600); err != nil {
		t.Fatal(err)
	}
	lease, err := session.OpenSegment(hlsInitSegmentName)
	if err != nil {
		t.Fatalf("open of a whole init: %v", err)
	}
	defer func() { _ = lease.Close() }()
	if got := lease.Info.Size(); got != int64(len(whole)) {
		t.Fatalf("leased size = %d, want %d", got, len(whole))
	}
}

// A segment that changes under the lease (a writer that truncates or replaces
// the name) must not be answered with the short file. The lease's own size is
// the authority, so the response is refused before a byte is written and the
// caller can answer the retry shape instead.
func TestServeSegmentLeaseRefusesAFileTruncatedUnderTheLease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seg_00468.ts")
	if err := os.WriteFile(path, bytes.Repeat([]byte{0x47}, 188*200), 0o600); err != nil {
		t.Fatal(err)
	}
	session := NewTranscodeSessionForTest(dir)
	lease, err := session.OpenSegment("seg_00468.ts")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = lease.Close() }()

	// The writer restarts and truncates the name between the lease and the
	// response: the bytes on disk are no longer the segment that was leased.
	if err := os.Truncate(path, 512); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	complete, serveErr := ServeSegmentLease(rec, httptest.NewRequest(http.MethodGet, "/segment/seg_00468.ts", nil), lease)
	if !errors.Is(serveErr, ErrSegmentIncomplete) {
		t.Fatalf("serve err = %v, want ErrSegmentIncomplete", serveErr)
	}
	if complete {
		t.Fatal("a refused lease reported a complete response")
	}
	if rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "" {
		t.Fatalf("refused lease wrote a body (%d bytes, Content-Length %q)", rec.Body.Len(), rec.Header().Get("Content-Length"))
	}
}

// A whole segment is served exactly once, with the lease's length.
func TestServeSegmentLeaseServesTheWholeSegment(t *testing.T) {
	dir := t.TempDir()
	body := bytes.Repeat([]byte{0x47}, 188*4)
	if err := os.WriteFile(filepath.Join(dir, "seg_00468.ts"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	session := NewTranscodeSessionForTest(dir)
	lease, err := session.OpenSegment("seg_00468.ts")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = lease.Close() }()

	rec := httptest.NewRecorder()
	complete, serveErr := ServeSegmentLease(rec, httptest.NewRequest(http.MethodGet, "/segment/seg_00468.ts", nil), lease)
	if serveErr != nil {
		t.Fatalf("serve: %v", serveErr)
	}
	if !complete {
		t.Fatal("the whole segment was not reported as complete")
	}
	if got := rec.Body.Len(); got != len(body) {
		t.Fatalf("body = %d bytes, want %d", got, len(body))
	}
	if got, want := rec.Header().Get("Content-Length"), "752"; got != want {
		t.Fatalf("Content-Length = %q, want %q", got, want)
	}
}
