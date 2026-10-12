// Package nodeenrollment binds an operator-controlled worker to one customer.
package nodeenrollment

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/Silo-Server/silo-server/internal/encodinglimit"
)

const (
	EnrollmentPath     = "/api/v1/node/enroll"
	ReceiptPath        = "/api/v1/node/enrollment"
	DefaultBindingFile = "/var/lib/silo/node-binding/binding.json"
	MaxBodyBytes       = 32768
)

var ErrAlreadyBound = errors.New("node already belongs to a customer")

// Binding contains only customer bootstrap configuration; operator capacity is
// deliberately absent. This value must never be logged or returned to clients.
type Binding struct {
	DatabaseURL string `json:"database_url" minLength:"1" maxLength:"8192" writeOnly:"true"`
	RedisURL    string `json:"redis_url,omitempty" maxLength:"8192" writeOnly:"true"`
	SecretKey   string `json:"secret_key" minLength:"32" maxLength:"4096" writeOnly:"true"`
	NodeURL     string `json:"node_url" minLength:"1" maxLength:"8192"`
	NodeName    string `json:"node_name" minLength:"1" maxLength:"1024"`
}

// Receipt attests ownership without exposing bootstrap secrets.
type Receipt struct {
	BindingID        string                 `json:"binding_id"`
	EncodingCapacity encodinglimit.Capacity `json:"encoding_capacity"`
}

func Identity(secretKey string) string {
	mac := hmac.New(sha256.New, []byte(secretKey))
	_, _ = mac.Write([]byte("silo-node-enrollment-v1"))
	return hex.EncodeToString(mac.Sum(nil))
}
func (b Binding) Validate() error {
	if b.DatabaseURL == "" || len(b.DatabaseURL) > 8192 || len(b.RedisURL) > 8192 || len(b.SecretKey) < 32 || len(b.SecretKey) > 4096 || strings.TrimSpace(b.NodeName) == "" || len(b.NodeName) > 1024 {
		return errors.New("invalid customer bootstrap configuration")
	}
	u, err := url.Parse(b.DatabaseURL)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		return errors.New("invalid customer database URL")
	}
	u, err = url.Parse(b.NodeURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(b.NodeURL) > 8192 {
		return errors.New("invalid node URL")
	}
	return nil
}
func decodeBinding(data []byte) (Binding, error) {
	var b Binding
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return b, errors.New("invalid enrollment payload")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return b, errors.New("invalid enrollment payload")
	}
	return b, b.Validate()
}

// Store publishes a complete immutable binding using an atomic create-if-absent
// link. Even two processes sharing its path cannot replace the first winner.
type Store struct{ Path string }

func (s Store) Load() (*Binding, error) {
	info, err := os.Lstat(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("cannot read node binding")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > MaxBodyBytes {
		return nil, errors.New("node binding must be a private regular file")
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, errors.New("cannot read node binding")
	}
	b, err := decodeBinding(data)
	if err != nil {
		return nil, errors.New("node binding is invalid; provider reset required")
	}
	return &b, nil
}
func (s Store) Save(b Binding) error {
	if err := b.Validate(); err != nil {
		return err
	}
	parent := filepath.Dir(s.Path)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return errors.New("cannot create private binding directory")
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("binding directory must have permissions 0700")
	}
	temp, err := os.CreateTemp(parent, ".binding-")
	if err != nil {
		return errors.New("cannot persist node binding")
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	data, err := json.Marshal(b)
	if err != nil {
		_ = temp.Close()
		return errors.New("cannot encode node binding")
	}
	if _, err = temp.Write(data); err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil || closeErr != nil {
		return errors.New("cannot persist node binding")
	}
	if err = os.Link(temp.Name(), s.Path); errors.Is(err, os.ErrExist) {
		return ErrAlreadyBound
	} else if err != nil {
		return errors.New("cannot publish node binding")
	}
	dir, err := os.Open(parent)
	if err != nil {
		return errors.New("cannot sync binding directory")
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return errors.New("cannot sync binding directory")
	}
	return nil
}
