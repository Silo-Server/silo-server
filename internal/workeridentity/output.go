package workeridentity

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

const DomainProxyOutputGrant = "silo/worker-proxy-output/v1"
const DomainProxyOutputRequest = "silo/worker-proxy-request/v1"

// OutputAuthority delegates only output requests. The executor receives the
// public key; the core and existing trusted proxy retain their shared secret.
type OutputAuthority struct {
	ProxyPublicKey      ed25519.PublicKey `json:"proxy_public_key"`
	Claims              json.RawMessage   `json:"claims"`
	Epoch               string            `json:"epoch"`
	AuthorityID         string            `json:"authority_id,omitempty"`
	JobID               string            `json:"job_id,omitempty"`
	ArtifactFingerprint string            `json:"artifact_fingerprint,omitempty"`
	ClientFingerprint   string            `json:"client_fingerprint,omitempty"`
}

func OutputEpoch(secret string) string {
	value, err := hkdf.Key(sha256.New, []byte(secret), nil, "silo/proxy-output-epoch/v1", 32)
	if err != nil || secret == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(value)
}

func OutputPrivateKey(secret string, g Grant) (ed25519.PrivateKey, error) {
	if secret == "" || g.NodeID <= 0 || g.WorkID == "" || g.Generation == 0 || g.Audience == "" {
		return nil, errors.New("invalid proxy authority")
	}
	info, err := json.Marshal([]any{"silo/proxy-output-signing/v1", g.NodeID, g.WorkID, g.Generation, g.Audience})
	if err != nil {
		return nil, err
	}
	seed, err := hkdf.Key(sha256.New, []byte(secret), nil, string(info), ed25519.SeedSize)
	if err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// ReadOutputScope is for the sending proxy's key selection only. Its caller
// must never use it to authorize a request; executors use DecodeGrant instead.
func ReadOutputScope(encoded string) (Grant, error) {
	var grant Grant
	parts := strings.Split(encoded, ".")
	if len(parts) != 2 || len(encoded) > 128<<10 {
		return grant, errors.New("invalid output authority")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return grant, err
	}
	if err = json.Unmarshal(data, &grant); err != nil {
		return grant, err
	}
	if grant.Version != 1 || grant.Domain != DomainProxyOutputGrant || grant.NodeID <= 0 || grant.WorkID == "" || grant.Generation == 0 {
		return Grant{}, errors.New("invalid output scope")
	}
	return grant, nil
}
