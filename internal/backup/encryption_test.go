package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

func testArchiveKey(t *testing.T, seed string) *ArchiveKey {
	t.Helper()
	key, err := NewArchiveKey([]byte(seed + strings.Repeat("k", 40)))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func encryptBytes(t *testing.T, key *ArchiveKey, plain []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := key.Encrypt(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestArchiveEncryptionRoundTrip: what a backup stores decrypts to what it
// was given, the stored digest is of the ciphertext a manifest records, and
// another key, a damaged payload, or a truncated file never yields
// plaintext that passes.
func TestArchiveEncryptionRoundTrip(t *testing.T) {
	key := testArchiveKey(t, "server-a")
	plain := make([]byte, 200_000) // several age chunks
	if _, err := rand.Read(plain); err != nil {
		t.Fatal(err)
	}
	stored, err := io.ReadAll(key.encryptingReader(bytes.NewReader(plain)))
	if err != nil {
		t.Fatal(err)
	}
	if !isAgeHeader(stored) || bytes.Contains(stored, plain[:64]) {
		t.Fatal("stored bytes are not an age file of the plaintext")
	}

	r, err := openStored(io.NopCloser(bytes.NewReader(stored)), key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("decrypted %d bytes, %v; want the %d plaintext bytes", len(got), err, len(plain))
	}
	entry, err := r.storedEntry("db.dump.age")
	sum := sha256.Sum256(stored)
	if err != nil || entry.SHA256 != hex.EncodeToString(sum[:]) || entry.Size != int64(len(stored)) {
		t.Fatalf("stored entry = %+v, %v; want the ciphertext's size and SHA-256", entry, err)
	}

	if _, err := openStored(io.NopCloser(bytes.NewReader(stored)), testArchiveKey(t, "server-b")); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("another key = %v, want ErrWrongKey", err)
	}
	damaged := append([]byte(nil), stored...)
	damaged[len(damaged)/2] ^= 1
	truncated := stored[:len(stored)-100]
	for name, data := range map[string][]byte{"damaged": damaged, "truncated": truncated} {
		r, err := openStored(io.NopCloser(bytes.NewReader(data)), key)
		if err == nil {
			_, err = io.ReadAll(r)
		}
		if err == nil {
			t.Fatalf("%s ciphertext decrypted without an error", name)
		}
	}
}

// TestArchiveKeyRefusesCostlyHeaders: an uploaded file names its own scrypt
// work factor, so a header asking for more than the bound is refused before
// any work.
func TestArchiveKeyRefusesCostlyHeaders(t *testing.T) {
	key := testArchiveKey(t, "server-a")
	stored := encryptBytes(t, key, []byte("restore set"))
	costly := bytes.Replace(stored, []byte(" 10\n"), []byte(" 22\n"), 1)
	if bytes.Equal(costly, stored) {
		t.Fatal("the test did not find the work factor in the header")
	}
	if _, err := key.Decrypt(bytes.NewReader(costly)); err == nil || errors.Is(err, ErrWrongKey) {
		t.Fatalf("a work factor of 22 = %v, want a refusal that is not a key mismatch", err)
	}
}

// mapObjects is a backup folder in memory that records what was opened.
type mapObjects struct {
	objects map[string][]byte
	opened  []string
}

func (m *mapObjects) Open(_ context.Context, name string) (io.ReadCloser, error) {
	m.opened = append(m.opened, name)
	data, ok := m.objects[name]
	if !ok {
		return nil, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// TestFetchSourceChecksSpaceAndScope: fetching an encrypted backup refuses
// before downloading anything when the working folder lacks room for it,
// fetches no blobs.tar for a database-only restore, and stops an object
// that writes more than its stored size.
func TestFetchSourceChecksSpaceAndScope(t *testing.T) {
	key := testArchiveKey(t, "server-a")
	m := validTestManifest()
	m.Encrypted, m.KeyCheck, m.Objects = true, key.Check, nil
	objects := &mapObjects{objects: map[string][]byte{}}
	for name, plain := range map[string][]byte{DumpName: []byte("dump bytes"), BlobsName: []byte("blob bytes")} {
		stored := encryptBytes(t, key, plain)
		objects.objects[name+EncryptedSuffix] = stored
		s := newSum()
		_, _ = s.Write(stored)
		m.Objects = append(m.Objects, s.entry(name+EncryptedSuffix))
	}
	ctx := t.Context()
	plenty := func(string) (uint64, error) { return 1 << 40, nil }

	_, err := FetchSource(ctx, objects, &m, FetchOptions{Key: key, WorkDir: t.TempDir(), FreeBytes: func(string) (uint64, error) { return fetchReserve, nil }})
	if !errors.Is(err, ErrNotEnoughSpace) || len(objects.opened) != 0 {
		t.Fatalf("fetch without room = %v, opened %v; want a refusal before any download", err, objects.opened)
	}
	src, err := FetchSource(ctx, objects, &m, FetchOptions{Key: key, WorkDir: t.TempDir(), DatabaseOnly: true, FreeBytes: plenty})
	if err != nil || len(objects.opened) != 1 || objects.opened[0] != DumpName+EncryptedSuffix {
		t.Fatalf("database-only fetch = %v, opened %v", err, objects.opened)
	}
	if err := src.Validate(ctx, ValidateOptions{KeyCheck: key.Check}); err != nil {
		t.Fatalf("validating a database-only fetch: %v", err)
	}

	// The manifest says the dump is smaller than what arrives.
	for i := range m.Objects {
		if m.Objects[i].Name == DumpName+EncryptedSuffix {
			bigger := encryptBytes(t, key, bytes.Repeat([]byte("x"), int(m.Objects[i].Size)+10))
			objects.objects[m.Objects[i].Name] = bigger
		}
	}
	if _, err := FetchSource(ctx, objects, &m, FetchOptions{Key: key, WorkDir: t.TempDir(), DatabaseOnly: true, FreeBytes: plenty}); err == nil ||
		!strings.Contains(err.Error(), "larger than its manifest") {
		t.Fatalf("fetch of an oversized object = %v", err)
	}
}
