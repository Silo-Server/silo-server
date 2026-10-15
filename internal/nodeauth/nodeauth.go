// Package nodeauth authenticates the internal HTTP calls between Silo API
// servers, proxies, and transcode nodes, in both directions.
//
// No input key ever crosses the wire. Each request instead carries an
// HMAC-SHA256 over its method, path, query, a timestamp, and the SHA-256 of its
// body. The MAC key is derived from the input key with HKDF under a label
// nothing else uses, so it cannot sign or verify any other token.
//
// Calls to a proxy use the shared auth.jwt_secret as the input key; it also
// signs login, profile, and stream tokens, which is why only its HKDF-derived
// key is used here. Calls to a transcode node use that node's own key (see
// Keyring), so a node never holds a secret that could sign for another worker.
// A transcode node signs its calls to the core with the same key under a
// separate label (SignToCore, VerifyFromNode), so a request captured in one
// direction never verifies in the other.
//
// See docs/architecture/worker-http-protocol.md#authentication.
package nodeauth

import (
	"bytes"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Scheme is the Authorization scheme of a signed worker request.
const Scheme = "Silo-Node-HMAC-SHA256"

const (
	// maxSkew is how far a signed request's timestamp may be from the
	// receiver's clock, in either direction.
	maxSkew = 30 * time.Second
	// maxBodyBytes bounds the body a receiver buffers to check its hash.
	// Worker request bodies are small JSON documents.
	maxBodyBytes = 1 << 20

	// keyLabel is the HKDF info string for the MAC key of core-to-worker
	// requests. Changing it, or reusing it for another purpose, breaks the key
	// separation.
	keyLabel = "silo/node-request-auth/v1"
	// canonicalVersion opens the signed string so a later format cannot be
	// confused with this one.
	canonicalVersion = "silo-node-request-v1"

	// nodeToCoreKeyLabel and nodeToCoreVersion are keyLabel and
	// canonicalVersion for requests a transcode node sends to the core. Both
	// differ from the core-to-worker pair, so a signature made in one
	// direction never verifies in the other.
	nodeToCoreKeyLabel = "silo/node-to-core-auth/v1"
	nodeToCoreVersion  = "silo-node-to-core-v1"
)

// direction selects the HKDF label and canonical version of one request
// direction. name labels the receiver's refusal warnings.
type direction struct {
	label, version, name string
}

var (
	coreToNode = direction{label: keyLabel, version: canonicalVersion, name: "core_to_node"}
	nodeToCore = direction{label: nodeToCoreKeyLabel, version: nodeToCoreVersion, name: "node_to_core"}
)

var (
	// ErrNoSecret reports that no key is configured: a receiver can verify
	// nothing, and a sender has no keyring (or an empty key) to sign with.
	ErrNoSecret = errors.New("node auth: no key is configured")
	// ErrUnauthorized reports a missing, malformed, expired, or wrong
	// credential.
	ErrUnauthorized = errors.New("node auth: unauthorized")
	// ErrNoNodeKey reports a transcode node that has no issued key. Nothing
	// can sign for it until an administrator issues one; dispatch treats the
	// node as unavailable. It is never a reason to fall back to the shared
	// secret.
	ErrNoNodeKey = errors.New("node auth: transcode node has no issued key")
	// ErrUnknownNode reports a URL that no registered stream node of the
	// requested kind has. Nothing is signed for it: the receiver might be a
	// transcode node the keyring has not loaded yet, which must never see a
	// shared-secret signature.
	ErrUnknownNode = errors.New("node auth: no stream node of this kind is registered at this URL")
)

// Target is the kind of worker a request is signed for. It decides which key
// may be used: a transcode node is only ever signed for with its own key, and
// the shared secret only ever goes to a URL registered as a proxy.
type Target uint8

const (
	// TargetTranscode is a transcode node, signed for with its own key.
	TargetTranscode Target = iota + 1
	// TargetProxy is a proxy node, signed for with the shared secret.
	TargetProxy
)

// Keyring supplies the request-signing input key for each worker.
type Keyring interface {
	// KeyFor returns the request-signing input key for the worker of kind
	// target registered at nodeURL. For TargetTranscode it is that node's own
	// key, and a node without one yields ErrNoNodeKey; for TargetProxy it is
	// the shared secret. A URL with no registered worker of that kind yields
	// ErrUnknownNode.
	KeyFor(target Target, nodeURL string) (string, error)
}

// SignTranscode authenticates req for the transcode node registered at
// nodeURL with that node's own key. See SignWith.
func SignTranscode(req *http.Request, nodeURL string, keys Keyring) error {
	return SignWith(req, TargetTranscode, nodeURL, keys)
}

// SignProxy authenticates req for the proxy registered at nodeURL with the
// shared secret. See SignWith.
func SignProxy(req *http.Request, nodeURL string, keys Keyring) error {
	return SignWith(req, TargetProxy, nodeURL, keys)
}

// SignWith authenticates req for the worker of kind target registered at
// nodeURL with the key keys returns for it (see sign). A nil keyring or an
// empty key is ErrNoSecret, so a sender that was never given its keys fails
// where it is.
func SignWith(req *http.Request, target Target, nodeURL string, keys Keyring) error {
	if keys == nil {
		return ErrNoSecret
	}
	key, err := keys.KeyFor(target, nodeURL)
	if err != nil {
		return err
	}
	if key == "" {
		return ErrNoSecret
	}
	return sign(req, nodeURL, key, time.Now())
}

var emptyBodyHash = sha256.Sum256(nil)

// sign authenticates req for the Silo worker registered at nodeURL with
// secret. Call it once the URL and body are final and just before sending:
// the signature expires after maxSkew. A body without GetBody is read into
// memory and replaced.
//
// The signature covers the request path relative to nodeURL, which is the
// path the worker serves: a reverse proxy that puts the worker under a path
// prefix strips that prefix before the worker sees it.
func sign(req *http.Request, nodeURL, secret string, now time.Time) error {
	return signRequest(coreToNode, req, nodeURL, secret, "", now)
}

// signRequest sets req's Authorization header for dir. nodeParam, when set, is
// the "n=" field naming the signing node.
func signRequest(dir direction, req *http.Request, baseURL, secret, nodeParam string, now time.Time) error {
	bodyHash, err := outgoingBodyHash(req)
	if err != nil {
		return fmt.Errorf("node auth: hash request body: %w", err)
	}
	timestamp := strconv.FormatInt(now.Unix(), 10)
	body := hex.EncodeToString(bodyHash)
	mac, err := requestMAC(dir, secret, timestamp, req.Method, workerPath(req.URL, baseURL), req.URL.RawQuery, body)
	if err != nil {
		return err
	}
	params := "t=" + timestamp + ",b=" + body + ",sig=" + hex.EncodeToString(mac)
	if nodeParam != "" {
		params = "n=" + nodeParam + "," + params
	}
	req.Header.Set("Authorization", Scheme+" "+params)
	return nil
}

// Verify checks that r came from a holder of the shared secret. It checks the
// MAC before reading any body, so a caller without the secret cannot make the
// worker buffer one. A signed body (at most maxBodyBytes) is then read to
// check its hash and left readable for the handler.
//
// Verify also accepts the legacy "Bearer <secret>" credential so API servers
// and proxies from the previous release keep working while a deployment
// upgrades. Remove that branch in the release after the one that introduced
// signed requests, with the tests marked for removal with it; every sender
// already signs.
func Verify(r *http.Request, secret string) error {
	return verify(r, secret, time.Now())
}

func verify(r *http.Request, secret string, now time.Time) error {
	if secret == "" {
		return ErrNoSecret
	}
	scheme, params, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	switch {
	case strings.EqualFold(scheme, Scheme):
		return verifySigned(r, secret, params, now)
	case strings.EqualFold(scheme, "Bearer"):
		if subtle.ConstantTimeCompare([]byte(params), []byte(secret)) != 1 {
			return ErrUnauthorized
		}
		if suppressed, ok := legacyBearerLog.allow(); ok {
			slog.WarnContext(r.Context(), "accepted a legacy bearer credential from an older Silo API server or proxy; upgrade it, because the next release accepts only signed requests",
				"component", "nodeauth", "remote_addr", r.RemoteAddr, "suppressed", suppressed)
		}
		return nil
	}
	return ErrUnauthorized
}

// VerifySigned is Verify without the legacy bearer: only a signed request
// passes. A transcode node verifies with its own key this way, since no
// sender ever had that key to send as a bearer.
func VerifySigned(r *http.Request, secret string) error {
	return verifySignedAt(r, secret, time.Now())
}

func verifySignedAt(r *http.Request, secret string, now time.Time) error {
	if secret == "" {
		return ErrNoSecret
	}
	scheme, params, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if !strings.EqualFold(scheme, Scheme) {
		return ErrUnauthorized
	}
	return verifySigned(r, secret, params, now)
}

func verifySigned(r *http.Request, secret, params string, now time.Time) error {
	fields, ok := parseParams(params)
	if !ok {
		return ErrUnauthorized
	}
	return verifyFields(coreToNode, r, secret, fields, now)
}

// verifyFields checks a parsed signature for dir: the MAC first, then the
// timestamp, and only then the body, so a caller without the key cannot make
// the receiver buffer one.
func verifyFields(dir direction, r *http.Request, secret string, fields signedParams, now time.Time) error {
	seconds, err := strconv.ParseInt(fields.timestamp, 10, 64)
	if err != nil {
		return ErrUnauthorized
	}
	got, err := hex.DecodeString(fields.signature)
	if err != nil || len(got) != sha256.Size {
		return ErrUnauthorized
	}
	bodyHash, err := hex.DecodeString(fields.body)
	if err != nil || len(bodyHash) != sha256.Size {
		return ErrUnauthorized
	}
	want, err := requestMAC(dir, secret, fields.timestamp, r.Method, r.URL.EscapedPath(), r.URL.RawQuery, fields.body)
	if err != nil {
		return ErrUnauthorized
	}
	if !hmac.Equal(got, want) {
		if suppressed, ok := badSignatureLog.allow(); ok {
			slog.WarnContext(r.Context(), "refused a node request with an invalid signature; check that the sender signs with the receiver's current key and that nothing in front of the receiver rewrites the request path or query",
				"component", "nodeauth", "direction", dir.name, "remote_addr", r.RemoteAddr, "suppressed", suppressed)
		}
		return ErrUnauthorized
	}
	// The MAC covers the timestamp, so a valid signature outside the window
	// means the clocks disagree (or an old request was replayed).
	if skew := now.Sub(time.Unix(seconds, 0)); skew > maxSkew || skew < -maxSkew {
		if suppressed, ok := staleLog.allow(); ok {
			slog.WarnContext(r.Context(), "refused a signed node request outside the 30-second window; check that the clocks of this server and its peer agree",
				"component", "nodeauth", "direction", dir.name, "remote_addr", r.RemoteAddr, "offset_seconds", int64(skew.Seconds()), "suppressed", suppressed)
		}
		return ErrUnauthorized
	}
	if !bodyMatches(r, bodyHash) {
		if suppressed, ok := badBodyLog.allow(); ok {
			slog.WarnContext(r.Context(), "refused a signed node request whose body does not match its signature; check that nothing in front of the receiver changes request bodies",
				"component", "nodeauth", "direction", dir.name, "remote_addr", r.RemoteAddr, "suppressed", suppressed)
		}
		return ErrUnauthorized
	}
	return nil
}

// macMatches reports whether fields carry the signature secret makes for r
// in dir. It reads no body.
func macMatches(dir direction, r *http.Request, secret string, fields signedParams) bool {
	got, err := hex.DecodeString(fields.signature)
	if err != nil || len(got) != sha256.Size {
		return false
	}
	want, err := requestMAC(dir, secret, fields.timestamp, r.Method, r.URL.EscapedPath(), r.URL.RawQuery, fields.body)
	return err == nil && hmac.Equal(got, want)
}

type signedParams struct {
	node, timestamp, body, signature string
}

// parseParams reads "t=<unix seconds>,b=<hex body SHA-256>,sig=<hex>" in any
// order, with an optional "n=<node id>" naming a signing node.
func parseParams(params string) (signedParams, bool) {
	var fields signedParams
	for part := range strings.SplitSeq(params, ",") {
		name, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			return signedParams{}, false
		}
		switch name {
		case "n":
			fields.node = value
		case "t":
			fields.timestamp = value
		case "b":
			fields.body = value
		case "sig":
			fields.signature = value
		}
	}
	return fields, fields.timestamp != "" && fields.body != "" && fields.signature != ""
}

// workerPath is target's escaped path without the path prefix of nodeURL, if
// it has one.
func workerPath(target *url.URL, nodeURL string) string {
	path := target.EscapedPath()
	base, err := url.Parse(nodeURL)
	if err != nil {
		return path
	}
	if prefix := strings.TrimRight(base.EscapedPath(), "/"); prefix != "" && strings.HasPrefix(path, prefix+"/") {
		return path[len(prefix):]
	}
	return path
}

// requestMAC signs the canonical form of one request in direction dir. An
// empty path is the root, which is what a client puts on the wire for it.
func requestMAC(dir direction, secret, timestamp, method, path, rawQuery, bodyHash string) ([]byte, error) {
	key, err := hkdf.Key(sha256.New, []byte(secret), nil, dir.label, sha256.Size)
	if err != nil {
		return nil, fmt.Errorf("node auth: derive key: %w", err)
	}
	if path == "" {
		path = "/"
	}
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, strings.Join([]string{
		dir.version, timestamp, method, path, rawQuery, bodyHash,
	}, "\n"))
	return mac.Sum(nil), nil
}

func outgoingBodyHash(req *http.Request) ([]byte, error) {
	if req.Body == nil || req.Body == http.NoBody {
		return emptyBodyHash[:], nil
	}
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		defer func() { _ = body.Close() }()
		hash := sha256.New()
		if _, err := io.Copy(hash, body); err != nil {
			return nil, err
		}
		return hash.Sum(nil), nil
	}
	data, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(data))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	req.ContentLength = int64(len(data))
	sum := sha256.Sum256(data)
	return sum[:], nil
}

// bodyMatches reads r's body, at most maxBodyBytes, compares its hash with
// the signed one, and leaves the body readable for the handler.
func bodyMatches(r *http.Request, want []byte) bool {
	if r.Body == nil || r.Body == http.NoBody {
		return hmac.Equal(want, emptyBodyHash[:])
	}
	if r.ContentLength > maxBodyBytes {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(data) > maxBodyBytes {
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	sum := sha256.Sum256(data)
	return hmac.Equal(want, sum[:])
}

// logThrottle limits one kind of warning to a line per interval per process:
// segment relays and capability probes would otherwise log on every request.
// Each line counts the warnings suppressed since the previous one, so a
// stranger's junk request cannot hide a run of genuine refusals.
type logThrottle struct {
	last       atomic.Int64
	suppressed atomic.Int64
}

const logInterval = 10 * time.Minute

var legacyBearerLog, badSignatureLog, staleLog, badBodyLog logThrottle

// processStart anchors the throttles to Go's monotonic clock, which wall-clock
// corrections do not move, so a clock fix neither silences nor re-opens them.
var processStart = time.Now()

// allow reports whether a warning may be logged now; if so it records the
// time and returns the number suppressed since the last one.
func (l *logThrottle) allow() (suppressed int64, ok bool) {
	return l.allowAt(time.Since(processStart))
}

// allowAt is allow at elapsed time since processStart. A concurrent caller
// that sampled an earlier time than the last recorded one counts as inside the
// interval.
func (l *logThrottle) allowAt(elapsed time.Duration) (suppressed int64, ok bool) {
	last := l.last.Load()
	if last != 0 && elapsed-time.Duration(last) < logInterval {
		l.suppressed.Add(1)
		return 0, false
	}
	if !l.last.CompareAndSwap(last, max(int64(elapsed), 1)) {
		l.suppressed.Add(1)
		return 0, false
	}
	return l.suppressed.Swap(0), true
}
