package backup

import (
	"errors"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/secret"
)

// StoreConfig is what opens a deployment's blob stores without its database:
// the artwork backend and local root, and the S3 buckets with their
// credentials. A full restore records it in the journal before anything
// changes, so --recover reopens the same stores while both databases stay
// closed.
type StoreConfig struct {
	Backend   string        `json:"backend"`
	LocalPath string        `json:"local_path,omitempty"`
	Public    *BucketConfig `json:"public,omitempty"`
	Private   *BucketConfig `json:"private,omitempty"`
}

// BucketConfig is one S3 bucket. SecretKey is in memory only; the journal
// holds SealedSecretKey, encrypted with SECRET_KEY.
type BucketConfig struct {
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region,omitempty"`
	Bucket          string `json:"bucket"`
	KeyPrefix       string `json:"key_prefix,omitempty"`
	AccessKey       string `json:"access_key,omitempty"`
	PathStyle       bool   `json:"path_style,omitempty"`
	SecretKey       string `json:"-"`
	SealedSecretKey string `json:"sealed_secret_key,omitempty"`
}

// journalSecretAAD binds a sealed credential to one journal and one bucket,
// so it cannot be moved into another journal or role.
func journalSecretAAD(stamp, role string) string {
	return "backup_restore_journal:" + stamp + ":" + role
}

// sealed returns a copy safe to write to the journal.
func (c *StoreConfig) sealed(cipher *secret.Cipher, stamp string) (*StoreConfig, error) {
	if c == nil {
		return nil, nil
	}
	out := *c
	var err error
	if out.Public, err = sealBucket(c.Public, cipher, stamp, "public"); err != nil {
		return nil, err
	}
	if out.Private, err = sealBucket(c.Private, cipher, stamp, "private"); err != nil {
		return nil, err
	}
	return &out, nil
}

// opened decrypts a journal's store configuration.
func (c *StoreConfig) opened(cipher *secret.Cipher, stamp string) (StoreConfig, error) {
	if c == nil {
		return StoreConfig{}, errors.New("the restore journal records no blob storage")
	}
	out := *c
	var err error
	if out.Public, err = openBucket(c.Public, cipher, stamp, "public"); err != nil {
		return StoreConfig{}, err
	}
	if out.Private, err = openBucket(c.Private, cipher, stamp, "private"); err != nil {
		return StoreConfig{}, err
	}
	return out, nil
}

func sealBucket(b *BucketConfig, cipher *secret.Cipher, stamp, role string) (*BucketConfig, error) {
	if b == nil {
		return nil, nil
	}
	out := *b
	out.SecretKey = ""
	if b.SecretKey != "" {
		if cipher == nil {
			return nil, errors.New("sealing the storage credentials needs SECRET_KEY")
		}
		sealed, err := cipher.Encrypt(b.SecretKey, journalSecretAAD(stamp, role))
		if err != nil {
			return nil, fmt.Errorf("sealing the %s bucket credentials: %w", role, err)
		}
		out.SealedSecretKey = sealed
	}
	return &out, nil
}

func openBucket(b *BucketConfig, cipher *secret.Cipher, stamp, role string) (*BucketConfig, error) {
	if b == nil {
		return nil, nil
	}
	out := *b
	out.SealedSecretKey = ""
	if b.SealedSecretKey != "" {
		if cipher == nil {
			return nil, errors.New("reading the storage credentials needs SECRET_KEY")
		}
		plain, err := cipher.Decrypt(b.SealedSecretKey, journalSecretAAD(stamp, role))
		if err != nil {
			// The cause is not wrapped: Decrypt's errors can quote parts of
			// the sealed value, and this one is printed.
			return nil, fmt.Errorf("reading the %s bucket credentials: they do not open with this SECRET_KEY; use the SECRET_KEY the restore ran with", role)
		}
		out.SecretKey = plain
	}
	return &out, nil
}
