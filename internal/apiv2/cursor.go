package apiv2

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

// CursorScope binds a cursor to its operation, security scope, filter, sort
// and tiebreaker. A cursor presented against a different scope is invalid.
type CursorScope struct {
	OperationID string
	// Security is the caller's scope identity: account and profile, and for
	// every collection the viewer access policy filters, viewerScopeDigest
	// as well. A cursor never outlives a change in what the caller may see:
	// when the policy changes mid-pagination, rows that became visible and
	// sort before the key would otherwise be skipped silently, so the client
	// restarts from the first page instead.
	Security string
	Filter   string
	Sort     string
	// Tiebreaker names the unique column that orders equal keys.
	Tiebreaker string
}

func (s CursorScope) key() string {
	return strings.Join([]string{s.OperationID, s.Security, s.Filter, s.Sort, s.Tiebreaker}, "\x00")
}

// viewerScopeDigest is a stable digest of the visibility-affecting fields of
// the request's effective access policy (policy revision, whether library
// access is restricted at all, allowed and disabled library IDs,
// content-rating ceiling). It goes into CursorScope.Security for
// access-filtered collections. "none" stands for a request that resolved no
// viewer scope.
//
// A nil id list and an empty one are different policies (an unrestricted
// profile versus one restricted to nothing), so they serialize differently:
// not every store bumps PolicyRevision when only that distinction changes,
// and a cursor minted under the narrower policy must not survive the wider
// one.
func viewerScopeDigest(ctx context.Context) string {
	scope, ok := scopeFrom(ctx)
	if !ok {
		return labelNone
	}
	ids := func(in []int) string {
		if in == nil {
			return "-"
		}
		sorted := slices.Clone(in)
		slices.Sort(sorted)
		parts := make([]string, len(sorted))
		for i, id := range sorted {
			parts[i] = strconv.Itoa(id)
		}
		return strings.Join(parts, ",")
	}
	restricted := "0"
	if scope.LibrariesRestricted {
		restricted = "1"
	}
	canonical := strings.Join([]string{
		strconv.FormatInt(scope.PolicyRevision, 10),
		restricted,
		ids(scope.AllowedLibraryIDs),
		ids(scope.DisabledLibraryIDs),
		scope.MaxContentRating,
	}, "\x00")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:8])
}

// Cursors mints and verifies opaque, tamper-resistant cursors. The position
// payload is caller-defined (typically the last row's sort key plus
// tiebreaker) and must not contain credentials or sensitive metadata: it is
// authenticated, not encrypted.
type Cursors struct {
	secret []byte
}

// NewCursors keys a Cursors; an empty secret gets a per-process random key.
func NewCursors(secret []byte) *Cursors {
	if len(secret) == 0 {
		secret = make([]byte, 32)
		_, _ = rand.Read(secret)
	}
	return &Cursors{secret: secret}
}

type cursorBody struct {
	V   int             `json:"v"`
	Pos json.RawMessage `json:"p"`
}

// Encode mints a cursor for a position within a scope.
func (c *Cursors) Encode(scope CursorScope, position any) (string, error) {
	pos, err := json.Marshal(position)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(cursorBody{V: 1, Pos: pos})
	if err != nil {
		return "", err
	}
	mac := c.sign(scope, body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac), nil
}

// Decode verifies a cursor against the scope and unmarshals its position.
// Any failure is the invalid_cursor problem (400).
func (c *Cursors) Decode(scope CursorScope, cursor string, position any) *Problem {
	invalid := NewProblem(TypeInvalidCursor, "The cursor is malformed, tampered with, or belongs to a different query.")
	body64, mac64, ok := strings.Cut(cursor, ".")
	if !ok {
		return invalid
	}
	body, err := base64.RawURLEncoding.DecodeString(body64)
	if err != nil {
		return invalid
	}
	mac, err := base64.RawURLEncoding.DecodeString(mac64)
	if err != nil {
		return invalid
	}
	if !hmac.Equal(mac, c.sign(scope, body)) {
		return invalid
	}
	var cb cursorBody
	if err := json.Unmarshal(body, &cb); err != nil || cb.V != 1 {
		return invalid
	}
	if err := json.Unmarshal(cb.Pos, position); err != nil {
		return invalid
	}
	return nil
}

func (c *Cursors) sign(scope CursorScope, body []byte) []byte {
	h := hmac.New(sha256.New, c.secret)
	h.Write([]byte(scope.key()))
	h.Write([]byte{0})
	h.Write(body)
	return h.Sum(nil)
}
