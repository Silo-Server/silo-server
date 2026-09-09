package requestbody

import (
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

type failingReadCloser struct {
	err    error
	closed bool
}

func (r *failingReadCloser) Read([]byte) (int, error) { return 0, r.err }
func (r *failingReadCloser) Close() error {
	r.closed = true
	return nil
}

func TestReadAcceptsExactLimitAndClosesBody(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader("1234"))
	body, err := Read(httptest.NewRecorder(), req, 4)
	if err != nil || string(body) != "1234" {
		t.Fatalf("Read() = %q, %v", body, err)
	}
}

func TestReadRejectsOverLimitWithoutContentLength(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader("12345"))
	req.ContentLength = -1
	_, err := Read(httptest.NewRecorder(), req, 4)
	if !IsTooLarge(err) || !errors.Is(err, ErrUnreadable) {
		t.Fatalf("Read() error = %v, want unreadable MaxBytesError", err)
	}
}

func TestReadRejectsTransportErrorAndClosesBody(t *testing.T) {
	wantErr := errors.New("read failed")
	reader := &failingReadCloser{err: wantErr}
	req := httptest.NewRequest("POST", "/", nil)
	req.Body = reader
	_, err := Read(httptest.NewRecorder(), req, 4)
	if !errors.Is(err, ErrUnreadable) || !errors.Is(err, wantErr) || IsTooLarge(err) {
		t.Fatalf("Read() error = %v", err)
	}
	if !reader.closed {
		t.Fatal("request body was not closed")
	}
}

func TestReadRejectsCloseError(t *testing.T) {
	wantErr := errors.New("close failed")
	req := httptest.NewRequest("POST", "/", nil)
	req.Body = struct {
		io.Reader
		io.Closer
	}{Reader: strings.NewReader("ok"), Closer: closeFunc(func() error { return wantErr })}
	_, err := Read(httptest.NewRecorder(), req, 4)
	if !errors.Is(err, ErrUnreadable) || !errors.Is(err, wantErr) {
		t.Fatalf("Read() error = %v", err)
	}
}

type closeFunc func() error

func (f closeFunc) Close() error { return f() }
