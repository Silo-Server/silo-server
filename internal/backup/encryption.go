package backup

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	agefile "filippo.io/age"

	"github.com/Silo-Server/silo-server/internal/secret"
)

// EncryptedSuffix ends the name of every object stored encrypted.
const EncryptedSuffix = ".age"

const (
	// scryptWorkFactor is the age scrypt work factor (log2 N) backups are
	// encrypted with. The passphrase is a 256-bit HKDF output, not a password,
	// so scrypt's stretching adds nothing; 2^10 costs about 1 MiB and 1 ms per
	// object, against age's default 2^18 (256 MiB, a third of a second).
	scryptWorkFactor = 10
	// scryptMaxWorkFactor bounds what decrypting accepts, about 32 MiB: an
	// uploaded file is untrusted, and its header names its own work factor.
	scryptMaxWorkFactor = 15
)

var (
	// ErrWrongKey reports an encrypted object this server's SECRET_KEY
	// cannot open.
	ErrWrongKey = errors.New("this backup is encrypted with a different SECRET_KEY")
	// ErrKeyNotConfirmed pauses S3 backups: the admin has not confirmed that
	// they keep the current SECRET_KEY off this server.
	ErrKeyNotConfirmed = errors.New("S3 backups are paused: confirm on the Backups page that you keep a copy of this server's SECRET_KEY")
)

// ArchiveKey encrypts and decrypts backup objects with the passphrase derived
// from SECRET_KEY (secret.BackupPassphrase), in the age format with a scrypt
// recipient. Check is the key's check value, which manifests record.
type ArchiveKey struct {
	passphrase string
	Check      string
}

// NewArchiveKey derives the archive key of masterKey.
func NewArchiveKey(masterKey []byte) (*ArchiveKey, error) {
	passphrase, err := secret.BackupPassphrase(masterKey)
	if err != nil {
		return nil, err
	}
	check, err := secret.KeyCheckValue(masterKey)
	if err != nil {
		return nil, err
	}
	return &ArchiveKey{passphrase: passphrase, Check: check}, nil
}

// Encrypt returns a writer that encrypts into w. Close flushes the final
// chunk; it does not close w.
func (k *ArchiveKey) Encrypt(w io.Writer) (io.WriteCloser, error) {
	recipient, err := agefile.NewScryptRecipient(k.passphrase)
	if err != nil {
		return nil, err
	}
	recipient.SetWorkFactor(scryptWorkFactor)
	return agefile.Encrypt(w, recipient)
}

// Decrypt returns the plaintext of r. The header is read and checked here;
// the payload is authenticated chunk by chunk as it is read, and a damaged
// or truncated payload fails the read that reaches it. A file encrypted
// with another key returns ErrWrongKey.
func (k *ArchiveKey) Decrypt(r io.Reader) (io.Reader, error) {
	identity, err := agefile.NewScryptIdentity(k.passphrase)
	if err != nil {
		return nil, err
	}
	identity.SetMaxWorkFactor(scryptMaxWorkFactor)
	plain, err := agefile.Decrypt(r, identity)
	if err != nil {
		if _, ok := errors.AsType[*agefile.NoIdentityMatchError](err); ok {
			return nil, ErrWrongKey
		}
		return nil, fmt.Errorf("reading the encrypted file: %w", err)
	}
	return plain, nil
}

// ageHeaders begin an age-encrypted file, binary or armored.
var ageHeaders = [][]byte{[]byte("age-encryption.org/"), []byte("-----BEGIN AGE ENCRYPTED FILE-----")}

// isAgeHeader reports whether head, a file's first bytes, begins an age file.
func isAgeHeader(head []byte) bool {
	for _, header := range ageHeaders {
		if bytes.HasPrefix(head, header) {
			return true
		}
	}
	return false
}

// storedName is the name an object of base is stored under: base, or
// base.age when encrypted.
func storedName(base string, encrypted bool) string {
	if encrypted {
		return base + EncryptedSuffix
	}
	return base
}

// encryptingReader returns the ciphertext of r as a reader: a goroutine
// encrypts into a pipe. A read error of r fails the returned reader with it;
// closing the returned reader stops the goroutine at its next write.
func (k *ArchiveKey) encryptingReader(r io.Reader) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		// Encrypt writes the header at once, so it runs here, where the
		// pipe has a reader.
		enc, err := k.Encrypt(pw)
		if err == nil {
			_, err = io.Copy(enc, r)
		}
		if err == nil {
			err = enc.Close()
		}
		_ = pw.CloseWithError(err)
	}()
	return pr
}

// storedReader decrypts a stored object while hashing its stored bytes, so
// a reader checks the manifest's checksum, which is of the ciphertext, and
// reads plaintext in one pass.
type storedReader struct {
	io.Reader
	raw  io.Reader
	sum  *sum
	body io.Closer
}

// openStored wraps body, a stored object, to read its plaintext: as is when
// key is nil, decrypted otherwise. The stored bytes are hashed either way.
func openStored(body io.ReadCloser, key *ArchiveKey) (*storedReader, error) {
	s := newSum()
	raw := io.TeeReader(body, s)
	r := &storedReader{Reader: raw, raw: raw, sum: s, body: body}
	if key == nil {
		return r, nil
	}
	plain, err := key.Decrypt(raw)
	if err != nil {
		_ = body.Close()
		return nil, err
	}
	r.Reader = plain
	return r, nil
}

// storedEntry drains what is left of the stored bytes into the hash and
// returns the SHA-256 and size of everything stored: after the plaintext
// ended, nothing the stored bytes hold escapes the checksum.
func (r *storedReader) storedEntry(name string) (ObjectEntry, error) {
	if _, err := io.Copy(io.Discard, r.raw); err != nil {
		return ObjectEntry{}, err
	}
	return r.sum.entry(name), nil
}

func (r *storedReader) Close() error { return r.body.Close() }
