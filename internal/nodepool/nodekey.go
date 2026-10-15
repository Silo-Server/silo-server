package nodepool

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/secret"
)

// A node key is a transcode node's own credential: the input key for the
// requests the core signs to it and for the requests it signs to the core.
// Its format is snk1_<node id>_<base64url of 32 random bytes>. The node id is
// part of the key, so a node needs nothing else to name its row.
const (
	nodeKeyPrefix      = "snk1_"
	nodeKeyRandomBytes = 32
)

var (
	// ErrInvalidNodeKey reports a string that is not a well-formed node key.
	ErrInvalidNodeKey = errors.New("invalid node key")
	// ErrNodeKeyNotIssuable reports a key request for a node that cannot take
	// one. Only transcode nodes have keys: a proxy holds the shared secret.
	ErrNodeKeyNotIssuable = errors.New("only transcode nodes take a node key")
)

var nodeKeyEncoding = base64.RawURLEncoding.Strict()

// GenerateNodeKey returns a fresh key for node nodeID.
func GenerateNodeKey(nodeID int) (string, error) {
	if nodeID <= 0 {
		return "", fmt.Errorf("%w: node id must be positive", ErrInvalidNodeKey)
	}
	random := make([]byte, nodeKeyRandomBytes)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate node key: %w", err)
	}
	return nodeKeyPrefix + strconv.Itoa(nodeID) + "_" + nodeKeyEncoding.EncodeToString(random), nil
}

// ParseNodeKey validates a node key and returns the node id it names. Only the
// canonical form passes: no surrounding whitespace, a positive decimal id
// without leading zeros, and exactly 32 bytes of unpadded base64url.
func ParseNodeKey(key string) (int, error) {
	rest, ok := strings.CutPrefix(key, nodeKeyPrefix)
	if !ok {
		return 0, fmt.Errorf("%w: missing %q prefix", ErrInvalidNodeKey, nodeKeyPrefix)
	}
	// The id is digits only, so the first underscore ends it even though the
	// base64url alphabet contains underscores.
	idPart, encoded, ok := strings.Cut(rest, "_")
	if !ok {
		return 0, fmt.Errorf("%w: missing node id", ErrInvalidNodeKey)
	}
	nodeID, err := strconv.Atoi(idPart)
	if err != nil || nodeID <= 0 || strconv.Itoa(nodeID) != idPart {
		return 0, fmt.Errorf("%w: malformed node id", ErrInvalidNodeKey)
	}
	random, err := nodeKeyEncoding.DecodeString(encoded)
	// The decoder skips newlines, so re-encoding is what pins the exact text.
	if err != nil || len(random) != nodeKeyRandomBytes || nodeKeyEncoding.EncodeToString(random) != encoded {
		return 0, fmt.Errorf("%w: malformed key material", ErrInvalidNodeKey)
	}
	return nodeID, nil
}

// nodeKeyAAD binds a key ciphertext to its row, so a ciphertext copied onto
// another node's row fails to decrypt there.
func nodeKeyAAD(nodeID int) string {
	return "stream_nodes.node_key/" + strconv.Itoa(nodeID)
}

// EncryptNodeKey seals node nodeID's key for its stream_nodes row.
func EncryptNodeKey(cipher *secret.Cipher, nodeID int, key string) (string, error) {
	if cipher == nil {
		return "", errors.New("encrypt node key: no data cipher")
	}
	if id, err := ParseNodeKey(key); err != nil || id != nodeID {
		return "", fmt.Errorf("encrypt node key: %w: not a key for node %d", ErrInvalidNodeKey, nodeID)
	}
	return cipher.Encrypt(key, nodeKeyAAD(nodeID))
}

// DecryptNodeKey opens the key stored on node nodeID's row. A plaintext value,
// a ciphertext from another row, and a key naming another node all fail.
func DecryptNodeKey(cipher *secret.Cipher, nodeID int, ciphertext string) (string, error) {
	if cipher == nil {
		return "", errors.New("decrypt node key: no data cipher")
	}
	key, err := cipher.Decrypt(ciphertext, nodeKeyAAD(nodeID))
	if err != nil {
		return "", fmt.Errorf("decrypt node key for node %d: %w", nodeID, err)
	}
	if id, err := ParseNodeKey(key); err != nil || id != nodeID {
		return "", fmt.Errorf("decrypt node key for node %d: %w", nodeID, ErrInvalidNodeKey)
	}
	return key, nil
}

// IssueNodeKey generates a new key for transcode node id, stores it encrypted,
// and returns it. This is the only time the key is returned: it is never read
// back out of the row except by the credential registry. Issuing again
// rotates the key, and the previous one stops working once the registries
// refresh. A proxy gets ErrNodeKeyNotIssuable and a missing row
// ErrNodeNotFound.
func (r *Repository) IssueNodeKey(ctx context.Context, cipher *secret.Cipher, id int) (string, error) {
	key, err := GenerateNodeKey(id)
	if err != nil {
		return "", err
	}
	ciphertext, err := EncryptNodeKey(cipher, id, key)
	if err != nil {
		return "", err
	}
	tag, err := r.pool.Exec(ctx,
		`UPDATE stream_nodes SET node_key_ciphertext = $2 WHERE id = $1 AND type = $3`,
		id, ciphertext, NodeTypeTranscode)
	if err != nil {
		return "", fmt.Errorf("issue node key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		if _, err := r.GetByID(ctx, id); err != nil {
			return "", err
		}
		return "", ErrNodeKeyNotIssuable
	}
	return key, nil
}

// nodeCredentialRow is what the credential registry reads of one node.
type nodeCredentialRow struct {
	ID         int
	Name       string
	Type       string
	URL        string
	Ciphertext *string
}

// listNodeCredentials reads every node, enabled or not, with its key
// ciphertext.
func (r *Repository) listNodeCredentials(ctx context.Context) ([]nodeCredentialRow, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, name, type, url, node_key_ciphertext FROM stream_nodes`)
	if err != nil {
		return nil, fmt.Errorf("list node credentials: %w", err)
	}
	defer rows.Close()
	var out []nodeCredentialRow
	for rows.Next() {
		var row nodeCredentialRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Type, &row.URL, &row.Ciphertext); err != nil {
			return nil, fmt.Errorf("list node credentials: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list node credentials: %w", err)
	}
	return out, nil
}
