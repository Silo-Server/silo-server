package backup

import "io"

// storedReader reads a stored object while hashing its bytes, so a reader
// checks the manifest's checksum in the same pass.
type storedReader struct {
	io.Reader
	sum  *sum
	body io.Closer
}

// openStored wraps body, a stored object, to read it while hashing it.
func openStored(body io.ReadCloser) *storedReader {
	s := newSum()
	return &storedReader{Reader: io.TeeReader(body, s), sum: s, body: body}
}

// storedEntry drains what is left of the stored bytes into the hash and
// returns the SHA-256 and size of everything stored: nothing past what the
// reader used escapes the checksum.
func (r *storedReader) storedEntry(name string) (ObjectEntry, error) {
	if _, err := io.Copy(io.Discard, r.Reader); err != nil {
		return ObjectEntry{}, err
	}
	return r.sum.entry(name), nil
}

func (r *storedReader) Close() error { return r.body.Close() }
