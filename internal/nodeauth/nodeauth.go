// Package nodeauth authenticates the internal HTTP calls that API servers and
// proxies make to Silo workers (proxies and transcode nodes).
//
// The shared auth.jwt_secret never crosses the wire: it also signs login,
// profile, and stream tokens, so a copy captured from a worker link could mint
// them. Each request instead carries an HMAC-SHA256 over its method, path,
// query, a timestamp, and the SHA-256 of its body. The MAC key is derived from
// the shared secret with HKDF under a label nothing else uses, so it cannot
// sign or verify any other token.
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

	// keyLabel is the HKDF info string for the MAC key. Changing it, or
	// reusing it for another purpose, breaks the key separation.
	keyLabel = "silo/node-request-auth/v1"
	// canonicalVersion opens the signed string so a later format cannot be
	// confused with this one.
	canonicalVersion = "silo-node-request-v1"
)

var (
	// ErrNoSecret reports that the receiver has no shared secret configured,
	// so it can verify nothing.
	ErrNoSecret = errors.New("node auth: shared secret is not configured")
	// ErrUnauthorized reports a missing, malformed, expired, or wrong
	// credential.
	ErrUnauthorized = errors.New("node auth: unauthorized")
)

var emptyBodyHash = sha256.Sum256(nil)

// Sign authenticates req for the Silo worker registered at nodeURL. Call it
// once the URL and body are final and just before sending: the signature
// expires after maxSkew. A body without GetBody is read into memory and
// replaced.
//
// The signature covers the request path relative to nodeURL, which is the
// path the worker serves: a reverse proxy that puts the worker under a path
// prefix strips that prefix before the worker sees it.
//
// Without a secret, Sign leaves req unsigned and the worker refuses it. Every
// server generates the secret at startup, so this happens only in tests.
func Sign(req *http.Request, nodeURL, secret string) error {
	return sign(req, nodeURL, secret, time.Now())
}

func sign(req *http.Request, nodeURL, secret string, now time.Time) error {
	if secret == "" {
		return nil
	}
	bodyHash, err := outgoingBodyHash(req)
	if err != nil {
		return fmt.Errorf("node auth: hash request body: %w", err)
	}
	timestamp := strconv.FormatInt(now.Unix(), 10)
	body := hex.EncodeToString(bodyHash)
	mac, err := requestMAC(secret, timestamp, req.Method, workerPath(req.URL, nodeURL), req.URL.RawQuery, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", Scheme+" t="+timestamp+",b="+body+",sig="+hex.EncodeToString(mac))
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

func verifySigned(r *http.Request, secret, params string, now time.Time) error {
	fields, ok := parseParams(params)
	if !ok {
		return ErrUnauthorized
	}
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
	want, err := requestMAC(secret, fields.timestamp, r.Method, r.URL.EscapedPath(), r.URL.RawQuery, fields.body)
	if err != nil {
		return ErrUnauthorized
	}
	if !hmac.Equal(got, want) {
		if suppressed, ok := badSignatureLog.allow(); ok {
			slog.WarnContext(r.Context(), "refused a node request with an invalid signature; check that the sender uses this worker's secret and that nothing in front of the worker rewrites the request path or query",
				"component", "nodeauth", "remote_addr", r.RemoteAddr, "suppressed", suppressed)
		}
		return ErrUnauthorized
	}
	// The MAC covers the timestamp, so a valid signature outside the window
	// means the clocks disagree (or an old request was replayed).
	if skew := now.Sub(time.Unix(seconds, 0)); skew > maxSkew || skew < -maxSkew {
		if suppressed, ok := staleLog.allow(); ok {
			slog.WarnContext(r.Context(), "refused a signed node request outside the 30-second window; check that this worker's clock agrees with its API servers and proxies",
				"component", "nodeauth", "remote_addr", r.RemoteAddr, "offset_seconds", int64(skew.Seconds()), "suppressed", suppressed)
		}
		return ErrUnauthorized
	}
	if !bodyMatches(r, bodyHash) {
		if suppressed, ok := badBodyLog.allow(); ok {
			slog.WarnContext(r.Context(), "refused a signed node request whose body does not match its signature; check that nothing in front of the worker changes request bodies",
				"component", "nodeauth", "remote_addr", r.RemoteAddr, "suppressed", suppressed)
		}
		return ErrUnauthorized
	}
	return nil
}

type signedParams struct {
	timestamp, body, signature string
}

// parseParams reads "t=<unix seconds>,b=<hex body SHA-256>,sig=<hex>" in any
// order.
func parseParams(params string) (signedParams, bool) {
	var fields signedParams
	for part := range strings.SplitSeq(params, ",") {
		name, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			return signedParams{}, false
		}
		switch name {
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

// requestMAC signs the canonical form of one request. An empty path is the
// root, which is what a client puts on the wire for it.
func requestMAC(secret, timestamp, method, path, rawQuery, bodyHash string) ([]byte, error) {
	key, err := hkdf.Key(sha256.New, []byte(secret), nil, keyLabel, sha256.Size)
	if err != nil {
		return nil, fmt.Errorf("node auth: derive key: %w", err)
	}
	if path == "" {
		path = "/"
	}
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, strings.Join([]string{
		canonicalVersion, timestamp, method, path, rawQuery, bodyHash,
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
