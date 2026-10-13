// Package workerregistration keeps enrollment state and the protected core
// signing key at the core. Workers never import its storage dependencies.
package workerregistration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

const keyAAD = "worker_http_keys:core-signing-key:v1"

type Repository struct {
	pool   *pgxpool.Pool
	cipher *secret.Cipher
}

func New(pool *pgxpool.Pool, cipher *secret.Cipher) *Repository {
	return &Repository{pool: pool, cipher: cipher}
}

func (r *Repository) SigningKey(ctx context.Context) (ed25519.PrivateKey, error) {
	if r.pool == nil || r.cipher == nil {
		return nil, errors.New("worker registration requires core storage")
	}
	_, candidate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	encrypted, err := r.cipher.Encrypt(base64.StdEncoding.EncodeToString(candidate), keyAAD)
	if err != nil {
		return nil, err
	}
	_, err = r.pool.Exec(ctx, "INSERT INTO worker_http_keys(singleton,private_key) VALUES(TRUE,$1) ON CONFLICT(singleton) DO NOTHING", encrypted)
	if err != nil {
		return nil, err
	}
	var stored string
	if err := r.pool.QueryRow(ctx, "SELECT private_key FROM worker_http_keys WHERE singleton=TRUE").Scan(&stored); err != nil {
		return nil, err
	}
	plain, err := r.cipher.Decrypt(stored, keyAAD)
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(plain)
	if err != nil || len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid stored core signing key")
	}
	return ed25519.PrivateKey(key), nil
}

func (r *Repository) ByID(ctx context.Context, id int) (workercontrolcore.Node, bool, error) {
	var node workercontrolcore.Node
	var key []byte
	err := r.pool.QueryRow(ctx, "SELECT n.id,n.url,n.enabled,w.public_key,w.allow_http FROM stream_nodes n JOIN worker_http_nodes w ON w.node_id=n.id WHERE n.id=$1 AND n.type='transcode'", id).Scan(&node.ID, &node.URL, &node.Enabled, &key, &node.AllowHTTP)
	if errors.Is(err, pgx.ErrNoRows) {
		return node, false, nil
	}
	if err != nil {
		return node, false, err
	}
	node.PublicKey = ed25519.PublicKey(key)
	return node, true, nil
}
func (r *Repository) ByURL(ctx context.Context, rawURL string) (workercontrolcore.Node, bool, error) {
	var id int
	// Keep an enrolled node classified even if its editable URL acquires an
	// unsupported path/query. The caller then rejects its origin rather than
	// falling back to legacy requests containing library paths.
	err := r.pool.QueryRow(ctx, "SELECT n.id FROM stream_nodes n JOIN worker_http_nodes w ON w.node_id=n.id WHERE substring(n.url from '^(https?://[^/?#]+)')=$1 AND n.type='transcode'", strings.TrimRight(rawURL, "/")).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return workercontrolcore.Node{}, false, nil
	}
	if err != nil {
		return workercontrolcore.Node{}, false, err
	}
	return r.ByID(ctx, id)
}

type Enrollment struct {
	Version       int               `json:"version"`
	NodeID        int               `json:"node_id"`
	CoreURL       string            `json:"core_url"`
	CorePublicKey ed25519.PublicKey `json:"core_public_key"`
	NodeURL       string            `json:"node_url"`
	AllowHTTP     bool              `json:"allow_http"`
	Ticket        string            `json:"ticket"`
	ExpiresAt     int64             `json:"expires_at"`
}

func (r *Repository) Issue(ctx context.Context, nodeID int, coreURL string, allowHTTP bool) (Enrollment, error) {
	var nodeURL string
	if err := r.pool.QueryRow(ctx, "SELECT url FROM stream_nodes WHERE id=$1 AND type='transcode' AND enabled", nodeID).Scan(&nodeURL); err != nil {
		return Enrollment{}, err
	}
	coreURL, err := workeridentity.ValidateOrigin(coreURL, allowHTTP)
	if err != nil {
		return Enrollment{}, err
	}
	nodeURL, err = workeridentity.ValidateOrigin(nodeURL, allowHTTP)
	if err != nil {
		return Enrollment{}, err
	}
	key, err := r.SigningKey(ctx)
	if err != nil {
		return Enrollment{}, err
	}
	var ticket [32]byte
	if _, err := rand.Read(ticket[:]); err != nil {
		return Enrollment{}, err
	}
	encoded := hex.EncodeToString(ticket[:])
	enrollment := Enrollment{Version: 1, NodeID: nodeID, CoreURL: coreURL, CorePublicKey: ed25519.PublicKey(key[ed25519.SeedSize:]), NodeURL: nodeURL, AllowHTTP: allowHTTP, Ticket: encoded, ExpiresAt: time.Now().Add(5 * time.Minute).Unix()}
	digest := enrollmentDigest(enrollment)
	_, err = r.pool.Exec(ctx, "INSERT INTO worker_http_enrollments(ticket_hash,node_id,expires_at) VALUES($1,$2,$3)", digest[:], nodeID, time.Unix(enrollment.ExpiresAt, 0))
	if err != nil {
		return Enrollment{}, err
	}
	return enrollment, nil
}

// Redeem consumes the one-use ticket and installs proof of node-key ownership
// atomically. Retrying a lost response requires a fresh operator ticket.
func (r *Repository) Redeem(ctx context.Context, enrollment Enrollment, key ed25519.PublicKey) error {
	if workeridentity.ValidatePublicKey(key) != nil || enrollment.Validate() != nil {
		return errors.New("invalid worker enrollment")
	}
	digest := enrollmentDigest(enrollment)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var nodeID int
	err = tx.QueryRow(ctx, "UPDATE worker_http_enrollments SET consumed_at=now() WHERE ticket_hash=$1 AND consumed_at IS NULL AND expires_at>now() RETURNING node_id", digest[:]).Scan(&nodeID)
	if err != nil || nodeID != enrollment.NodeID {
		return errors.New("worker enrollment expired or already consumed")
	}
	var currentURL string
	if err := tx.QueryRow(ctx, "SELECT url FROM stream_nodes WHERE id=$1 AND enabled AND type='transcode' FOR UPDATE", nodeID).Scan(&currentURL); err != nil {
		return errors.New("worker enrollment node changed")
	}
	currentURL, err = workeridentity.ValidateOrigin(currentURL, enrollment.AllowHTTP)
	if err != nil || currentURL != enrollment.NodeURL {
		return errors.New("worker enrollment node changed")
	}
	_, err = tx.Exec(ctx, "INSERT INTO worker_http_nodes(node_id,public_key,allow_http) VALUES($1,$2,$3) ON CONFLICT(node_id) DO UPDATE SET public_key=excluded.public_key,allow_http=excluded.allow_http", nodeID, []byte(key), enrollment.AllowHTTP)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (e Enrollment) Validate() error {
	core, err := workeridentity.ValidateOrigin(e.CoreURL, e.AllowHTTP)
	if err != nil || core != e.CoreURL {
		return errors.New("invalid enrollment core origin")
	}
	node, err := workeridentity.ValidateOrigin(e.NodeURL, e.AllowHTTP)
	if err != nil || node != e.NodeURL {
		return errors.New("invalid enrollment worker origin")
	}
	ticket, err := hex.DecodeString(e.Ticket)
	if e.Version != 1 || e.NodeID <= 0 || e.ExpiresAt <= 0 || workeridentity.ValidatePublicKey(e.CorePublicKey) != nil || err != nil || len(ticket) != 32 || hex.EncodeToString(ticket) != e.Ticket {
		return errors.New("invalid enrollment identity")
	}
	return nil
}

// Hash the complete operator-issued context. Changing an origin, public key or
// HTTP policy cannot redirect a ticket to a different binding.
func enrollmentDigest(e Enrollment) [32]byte {
	data, _ := json.Marshal(e)
	return sha256.Sum256(append([]byte("silo.worker.enrollment.ticket.v1\x00"), data...))
}
