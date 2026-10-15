package nodeauth

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrNoNodeIdentity reports a transcode node that tried to sign a request to
// the core without its id or key.
var ErrNoNodeIdentity = errors.New("node auth: node id and key are not configured")

// SignToCore authenticates req, sent by transcode node nodeID to the core at
// coreURL, with that node's key. The header names the node ("n=<id>") so the
// core can find the key; the MAC uses the node-to-core label, so the request
// never verifies as a core-to-node one. The signed path is relative to
// coreURL, by the same rule SignWith applies to a worker URL. Call it just before
// sending: the signature expires after maxSkew.
func SignToCore(req *http.Request, coreURL string, nodeID int, nodeKey string) error {
	return signToCore(req, coreURL, nodeID, nodeKey, time.Now())
}

func signToCore(req *http.Request, coreURL string, nodeID int, nodeKey string, now time.Time) error {
	if nodeID <= 0 || nodeKey == "" {
		return ErrNoNodeIdentity
	}
	return signRequest(nodeToCore, req, coreURL, nodeKey, strconv.Itoa(nodeID), now)
}

// NodeKeyError is VerifyFromNode refusing a request because the node it names
// has no key to check it with: Err is NodeKeys' answer (an unknown, deleted,
// keyless or non-transcode node). It matches ErrUnauthorized, so a caller
// that only answers 401 need not tell it apart; one that logs can name the
// node and the reason.
type NodeKeyError struct {
	NodeID int
	Err    error
}

func (e *NodeKeyError) Error() string {
	return fmt.Sprintf("node auth: no key for node %d: %v", e.NodeID, e.Err)
}

func (e *NodeKeyError) Unwrap() []error { return []error{ErrUnauthorized, e.Err} }

// NodeKeys supplies the keys VerifyFromNode checks node requests with.
type NodeKeys interface {
	// NodeKey returns transcode node nodeID's key. Any error (an unknown,
	// deleted, or keyless node) refuses the request.
	NodeKey(nodeID int) (string, error)
	// ReloadNodeKey reloads the keys and returns node nodeID's key then. It is
	// asked only after a request from a known node failed to verify with the
	// key NodeKey returned, since the node may hold a key issued after this
	// server last loaded them. It must bound how often it reloads: the request
	// that prompts it is not authenticated.
	ReloadNodeKey(nodeID int) (string, error)
}

// VerifyFromNode checks that r was signed by a transcode node with its own key
// and returns that node's id. The key comes from keys, by the id the header
// names; a signature that does not match it is checked once more against the
// key after a reload, so a node with a freshly rotated key is not refused
// until the next periodic refresh. A node with no key to check against is a
// *NodeKeyError. Like Verify it checks the MAC before
// reading any body, enforces the same 30-second window and 1 MiB body bound,
// and leaves a verified body readable for the handler.
func VerifyFromNode(r *http.Request, keys NodeKeys) (int, error) {
	return verifyFromNode(r, keys, time.Now())
}

func verifyFromNode(r *http.Request, keys NodeKeys, now time.Time) (int, error) {
	scheme, params, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if !strings.EqualFold(scheme, Scheme) {
		return 0, ErrUnauthorized
	}
	fields, ok := parseParams(params)
	if !ok {
		return 0, ErrUnauthorized
	}
	nodeID, err := strconv.Atoi(fields.node)
	// Only the canonical decimal form names a node, so "07" and "+7" cannot
	// pose as node 7 in logs or anywhere else that compares the raw field.
	if err != nil || nodeID <= 0 || strconv.Itoa(nodeID) != fields.node {
		return 0, ErrUnauthorized
	}
	key, err := keys.NodeKey(nodeID)
	if err == nil && key == "" {
		err = ErrNoNodeKey
	}
	if err != nil {
		return 0, &NodeKeyError{NodeID: nodeID, Err: err}
	}
	if !macMatches(nodeToCore, r, key, fields) {
		if fresh, err := keys.ReloadNodeKey(nodeID); err == nil && fresh != "" {
			key = fresh
		}
	}
	if err := verifyFields(nodeToCore, r, key, fields, now); err != nil {
		return 0, err
	}
	return nodeID, nil
}
