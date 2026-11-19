package matroska

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestReadElementHeaderErrors(t *testing.T) {
	r := bytes.NewReader([]byte{0x1F, 0x43})
	if _, _, _, err := ReadElementHeader(r, 0); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("cut-off header: err = %v, want ErrUnexpectedEOF", err)
	}
	if _, _, _, err := ReadElementHeader(r, 2); !errors.Is(err, io.EOF) {
		t.Fatalf("at the end: err = %v, want EOF", err)
	}
	failing := errors.New("disk gone")
	if _, _, _, err := ReadElementHeader(errReader{failing}, 0); !errors.Is(err, failing) {
		t.Fatalf("failed read: err = %v, want %v", err, failing)
	}
}

type errReader struct{ err error }

func (r errReader) ReadAt([]byte, int64) (int, error) { return 0, r.err }
