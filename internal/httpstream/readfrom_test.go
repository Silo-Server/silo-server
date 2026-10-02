package httpstream

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

type readerFromResponseWriter struct {
	bytes.Buffer
	called int
	header http.Header
}

func (w *readerFromResponseWriter) Header() http.Header { return w.header }
func (w *readerFromResponseWriter) WriteHeader(int)     {}
func (w *readerFromResponseWriter) ReadFrom(r io.Reader) (int64, error) {
	w.called++
	return io.Copy(&w.Buffer, r)
}

func TestCopyChunkedUsesReaderFromPerSlice(t *testing.T) {
	w := &readerFromResponseWriter{header: make(http.Header)}
	rf, ok := ReaderFromOf(w)
	if !ok {
		t.Fatal("ReaderFromOf did not report direct implementation")
	}
	var recorded int64
	n, err := CopyChunked(rf, bytes.NewReader(make([]byte, 10)), 4, func(n int64, _ error) { recorded += n })
	if err != nil || n != 10 || recorded != 10 || w.called != 3 {
		t.Fatalf("CopyChunked = n=%d err=%v recorded=%d calls=%d", n, err, recorded, w.called)
	}
}

type recordingReaderFrom struct {
	readers []io.Reader
	errAt   int
	err     error
}

func (f *recordingReaderFrom) ReadFrom(r io.Reader) (int64, error) {
	f.readers = append(f.readers, r)
	n, err := io.Copy(io.Discard, r)
	if f.errAt == len(f.readers) {
		return n, f.err
	}
	return n, err
}

func TestCopyChunkedLimitedReaderPreservesSendfileShape(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "copy-chunked-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(make([]byte, 10)); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}

	fake := &recordingReaderFrom{}
	n, err := CopyChunked(fake, io.LimitReader(file, 10), 4, nil)
	if err != nil || n != 10 || len(fake.readers) != 3 {
		t.Fatalf("CopyChunked = n=%d err=%v calls=%d", n, err, len(fake.readers))
	}
	for i, reader := range fake.readers {
		lr, ok := reader.(*io.LimitedReader)
		if !ok || lr.R != file {
			t.Fatalf("reader %d = %T %+v; want one limiter over file", i, reader, reader)
		}
	}
}

func TestCopyChunkedAccounting(t *testing.T) {
	tests := []struct {
		name        string
		size        int
		limit       int64
		chunk       int64
		wantCalls   int
		wantRecords int
	}{
		{name: "limited shorter than chunk", size: 3, limit: 3, chunk: 4, wantCalls: 1, wantRecords: 1},
		{name: "limited exact multiple", size: 8, limit: 8, chunk: 4, wantCalls: 2, wantRecords: 2},
		{name: "unlimited shorter than chunk", size: 3, limit: -1, chunk: 4, wantCalls: 1, wantRecords: 1},
		{name: "unlimited exact multiple", size: 8, limit: -1, chunk: 4, wantCalls: 3, wantRecords: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			src := io.Reader(bytes.NewReader(make([]byte, test.size)))
			if test.limit >= 0 {
				src = io.LimitReader(src, test.limit)
			}
			fake := &recordingReaderFrom{}
			var records []int64
			n, err := CopyChunked(fake, src, test.chunk, func(n int64, _ error) { records = append(records, n) })
			if err != nil || n != int64(test.size) || len(fake.readers) != test.wantCalls || len(records) != test.wantRecords {
				t.Fatalf("CopyChunked = n=%d err=%v calls=%d records=%v", n, err, len(fake.readers), records)
			}
		})
	}
}

func TestCopyChunkedLimitedReaderErrorUpdatesBudget(t *testing.T) {
	wantErr := errors.New("read failed")
	fake := &recordingReaderFrom{errAt: 1, err: wantErr}
	lr := &io.LimitedReader{R: bytes.NewReader(make([]byte, 10)), N: 10}
	var recordedN int64
	var recordedErr error
	n, err := CopyChunked(fake, lr, 4, func(n int64, err error) {
		recordedN, recordedErr = n, err
	})
	if !errors.Is(err, wantErr) || n != 4 || lr.N != 6 || recordedN != 4 || !errors.Is(recordedErr, wantErr) {
		t.Fatalf("CopyChunked = n=%d err=%v remaining=%d record=(%d,%v)", n, err, lr.N, recordedN, recordedErr)
	}
}

func TestWriterOnlyHidesReaderFrom(t *testing.T) {
	w := &readerFromResponseWriter{header: make(http.Header)}
	if _, ok := WriterOnly(w).(io.ReaderFrom); ok {
		t.Fatal("WriterOnly exposed io.ReaderFrom")
	}
}

func TestCompressExceptBypassPreservesReaderFromAndKeptRouteCompresses(t *testing.T) {
	handlerSawReaderFrom := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, handlerSawReaderFrom = w.(io.ReaderFrom)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(bytes.Repeat([]byte("x"), 2048))
	})
	handler := CompressExcept(gzip.BestSpeed, func(r *http.Request) bool { return r.URL.Path == "/media" })(next)

	mediaWriter := &readerFromResponseWriter{header: make(http.Header)}
	mediaReq := httptest.NewRequest(http.MethodGet, "/media", nil)
	mediaReq.Header.Set("Accept-Encoding", "gzip")
	handler.ServeHTTP(mediaWriter, mediaReq)
	if !handlerSawReaderFrom || mediaWriter.Header().Get("Content-Encoding") != "" || mediaWriter.Len() != 2048 {
		t.Fatalf("bypass: saw ReaderFrom=%v encoding=%q bytes=%d", handlerSawReaderFrom, mediaWriter.Header().Get("Content-Encoding"), mediaWriter.Len())
	}

	recorder := httptest.NewRecorder()
	jsonReq := httptest.NewRequest(http.MethodGet, "/json", nil)
	jsonReq.Header.Set("Accept-Encoding", "gzip")
	handler.ServeHTTP(recorder, jsonReq)
	if recorder.Header().Get("Content-Encoding") != "gzip" || recorder.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatalf("kept route headers: encoding=%q vary=%q", recorder.Header().Get("Content-Encoding"), recorder.Header().Get("Vary"))
	}
}
