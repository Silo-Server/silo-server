// Package workeridentity authenticates the standalone worker protocol.
package workeridentity

import (
	"bytes"
	"container/heap"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	MaxBodyBytes         = 1 << 20
	DomainCoreCommand    = "silo/worker-core-command/v1"
	DomainNodeProof      = "silo/worker-node-proof/v1"
	DomainSourceRequest  = "silo/worker-source-request/v1"
	DomainSourceGrant    = "silo/worker-source/v1"
	DomainExecutionGrant = "silo/worker-execution/v1"
	DomainSnapshotGrant  = "silo/worker-snapshot/v1"
	DomainConfigGrant    = "silo/worker-config/v1"
	DomainReportGrant    = "silo/worker-report/v1"
	HeaderNodeID         = "X-Silo-Worker-Node-ID"
	HeaderDomain         = "X-Silo-Worker-Domain"
	HeaderAudience       = "X-Silo-Worker-Audience"
	HeaderTimestamp      = "X-Silo-Worker-Timestamp"
	HeaderNonce          = "X-Silo-Worker-Nonce"
	HeaderSignature      = "X-Silo-Worker-Signature"
	HeaderWorkID         = "X-Silo-Worker-Work-ID"
	HeaderGeneration     = "X-Silo-Worker-Generation"
)

type Verifier struct {
	Origin     string
	Domain     string
	AllowHTTP  bool
	ResolveKey func(int) (ed25519.PublicKey, error)
	Window     time.Duration
	MaxNonces  int
	Now        func() time.Time
	mu         sync.Mutex
	nonces     map[string]time.Time
	expiries   nonceHeap
}

type Grant struct {
	Version    int             `json:"version"`
	Domain     string          `json:"domain"`
	Audience   string          `json:"audience"`
	NodeID     int             `json:"node_id"`
	WorkID     string          `json:"work_id"`
	Generation uint64          `json:"generation"`
	ExpiresAt  int64           `json:"expires_at"`
	Body       json.RawMessage `json:"body"`
}

type Binding struct {
	Version        int                `json:"version"`
	NodeID         int                `json:"node_id"`
	CoreURL        string             `json:"core_url"`
	CorePublicKey  ed25519.PublicKey  `json:"core_public_key"`
	NodePrivateKey ed25519.PrivateKey `json:"node_private_key"`
	NodeURL        string             `json:"node_url"`
	AllowHTTP      bool               `json:"allow_http"`
}

const requestPrefix = "silo.worker.request.v1\x00"
const grantPrefix = "silo.worker.grant.v1\x00"

// Sign authenticates an exact request for a configured recipient origin.
func Sign(req *http.Request, origin, domain string, key ed25519.PrivateKey, nodeID int, allowHTTP bool) error {
	if !validPrivateKey(key) || nodeID <= 0 || !validDomain(domain) {
		return errors.New("invalid signing identity")
	}
	origin, err := ValidateOrigin(origin, allowHTTP)
	if err != nil {
		return err
	}
	if err = requestOrigin(req, origin, allowHTTP); err != nil {
		return err
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	encoding, err := oneHeader(req.Header, "Accept-Encoding", false)
	if err != nil {
		return err
	}
	if encoding == "" {
		deleteHeader(req.Header, "Accept-Encoding")
		req.Header.Set("Accept-Encoding", "identity")
	}
	for _, h := range []string{HeaderNodeID, HeaderDomain, HeaderAudience, HeaderTimestamp, HeaderNonce, HeaderSignature} {
		deleteHeader(req.Header, h)
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	req.Header.Set(HeaderNodeID, strconv.Itoa(nodeID))
	req.Header.Set(HeaderDomain, domain)
	req.Header.Set(HeaderAudience, origin)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(time.Now().UnixMilli(), 10))
	req.Header.Set(HeaderNonce, hex.EncodeToString(nonce[:]))
	message, err := requestMessage(req)
	if err != nil {
		return err
	}
	req.Header.Set(HeaderSignature, base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, message)))
	return nil
}

// Verify checks the configured audience and consumes a nonce only after a valid
// proof. A full cache rejects new proofs instead of evicting live replay entries.
func (v *Verifier) Verify(req *http.Request, nodeID int) error {
	return v.verify(req, nodeID, nil)
}

// VerifyKey shares this verifier's replay ledger across dynamically delegated
// work keys. The caller first verifies the core-signed delegation and scope.
func (v *Verifier) VerifyKey(req *http.Request, nodeID int, key ed25519.PublicKey) error {
	return v.verify(req, nodeID, key)
}

func (v *Verifier) verify(req *http.Request, nodeID int, delegated ed25519.PublicKey) error {
	origin, err := ValidateOrigin(v.Origin, v.AllowHTTP)
	if err != nil {
		return err
	}
	if !validDomain(v.Domain) || nodeID <= 0 || v.ResolveKey == nil && delegated == nil {
		return errors.New("invalid verifier configuration")
	}
	if err = requestOrigin(req, origin, v.AllowHTTP); err != nil {
		return err
	}
	id, err := RequestNodeID(req)
	if err != nil || id != nodeID {
		return errors.New("request node mismatch")
	}
	domain, err := oneHeader(req.Header, HeaderDomain, true)
	if err != nil || domain != v.Domain {
		return errors.New("request domain mismatch")
	}
	audience, err := oneHeader(req.Header, HeaderAudience, true)
	if err != nil || audience != origin {
		return errors.New("request audience mismatch")
	}
	timestamp, err := oneHeader(req.Header, HeaderTimestamp, true)
	if err != nil {
		return err
	}
	millis, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || strconv.FormatInt(millis, 10) != timestamp {
		return errors.New("invalid proof timestamp")
	}
	nonce, err := oneHeader(req.Header, HeaderNonce, true)
	if err != nil {
		return err
	}
	nonceBytes, err := hex.DecodeString(nonce)
	if err != nil || len(nonceBytes) != 16 || hex.EncodeToString(nonceBytes) != nonce {
		return errors.New("invalid proof nonce")
	}
	window := v.Window
	if window == 0 {
		window = 30 * time.Second
	}
	if window <= 0 || window > 30*time.Second {
		return errors.New("invalid replay window")
	}
	capacity := v.MaxNonces
	if capacity == 0 {
		capacity = 65536
	}
	if capacity < 1 || capacity > 65536 {
		return errors.New("invalid replay cache capacity")
	}
	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	sent := time.UnixMilli(millis)
	if !now.Before(sent.Add(window)) || sent.After(now.Add(window)) {
		return errors.New("request proof expired")
	}
	if err := v.verifySignature(req, nodeID, delegated); err != nil {
		return err
	}
	return v.consumeNonce(nodeID, nonce, now, sent.Add(window), capacity)
}

func (v *Verifier) verifySignature(req *http.Request, nodeID int, delegated ed25519.PublicKey) error {
	encoded, err := oneHeader(req.Header, HeaderSignature, true)
	if err != nil {
		return err
	}
	sig, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("invalid request signature")
	}
	key := delegated
	if key == nil {
		key, err = v.ResolveKey(nodeID)
		if err != nil {
			return errors.New("unknown request identity")
		}
	}
	if !validPublicKey(key) {
		return errors.New("invalid request public key")
	}
	message, err := requestMessage(req)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, message, sig) {
		return errors.New("invalid request proof")
	}
	return nil
}

func (v *Verifier) consumeNonce(nodeID int, nonce string, now, expires time.Time, capacity int) error {
	cacheKey := strconv.Itoa(nodeID) + ":" + nonce
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.nonces == nil {
		v.nonces = make(map[string]time.Time)
	}
	for len(v.expiries) > 0 && !now.Before(v.expiries[0].deadline) {
		entry, ok := heap.Pop(&v.expiries).(nonceEntry)
		if !ok {
			return errors.New("invalid replay cache entry")
		}
		delete(v.nonces, entry.key)
	}
	if _, exists := v.nonces[cacheKey]; exists {
		return errors.New("request proof replayed")
	}
	if len(v.nonces) >= capacity {
		return errors.New("request replay cache full")
	}
	v.nonces[cacheKey] = expires
	heap.Push(&v.expiries, nonceEntry{cacheKey, expires})
	return nil
}

type nonceEntry struct {
	key      string
	deadline time.Time
}
type nonceHeap []nonceEntry

func (h *nonceHeap) Len() int           { return len(*h) }
func (h *nonceHeap) Less(i, j int) bool { return (*h)[i].deadline.Before((*h)[j].deadline) }
func (h *nonceHeap) Swap(i, j int)      { (*h)[i], (*h)[j] = (*h)[j], (*h)[i] }
func (h *nonceHeap) Push(value any) {
	entry, ok := value.(nonceEntry)
	if !ok {
		panic("invalid replay cache entry")
	}
	*h = append(*h, entry)
}
func (h *nonceHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	old[len(old)-1] = nonceEntry{}
	*h = old[:len(old)-1]
	return last
}

func RequestNodeID(req *http.Request) (int, error) {
	if req == nil {
		return 0, errors.New("missing request")
	}
	value, err := oneHeader(req.Header, HeaderNodeID, true)
	if err != nil {
		return 0, err
	}
	id, err := strconv.Atoi(value)
	if err != nil || id <= 0 || strconv.Itoa(id) != value {
		return 0, errors.New("invalid request node")
	}
	return id, nil
}

func requestMessage(req *http.Request) ([]byte, error) {
	body, err := readBody(req)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(body)
	fields := []string{req.Method, req.URL.RequestURI(), hex.EncodeToString(digest[:])}
	for _, h := range []string{HeaderNodeID, HeaderDomain, HeaderAudience, HeaderTimestamp, HeaderNonce, HeaderWorkID, HeaderGeneration, "Range", "If-Match", "Accept-Encoding", "X-Silo-Stream-Token", "X-Silo-Transcode-Segment-Generation"} {
		value, e := oneHeader(req.Header, h, false)
		if e != nil {
			return nil, e
		}
		fields = append(fields, value)
	}
	encoded, _ := json.Marshal(fields)
	return append([]byte(requestPrefix), encoded...), nil
}

type restoredBody struct {
	io.Reader
	original io.Closer
}

func (b *restoredBody) Close() error { return b.original.Close() }
func readBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	original := req.Body
	body, err := io.ReadAll(io.LimitReader(original, MaxBodyBytes+1))
	req.Body = &restoredBody{Reader: io.MultiReader(bytes.NewReader(body), original), original: original}
	if err != nil {
		return nil, errors.New("cannot read request body")
	}
	if len(body) > MaxBodyBytes {
		return nil, errors.New("request body exceeds limit")
	}
	return body, nil
}

func oneHeader(headers http.Header, name string, required bool) (string, error) {
	var values []string
	for key, vs := range headers {
		if strings.EqualFold(key, name) {
			values = append(values, vs...)
		}
	}
	if len(values) > 1 || required && (len(values) != 1 || values[0] == "") {
		return "", fmt.Errorf("invalid %s header", name)
	}
	if len(values) == 0 {
		return "", nil
	}
	if strings.ContainsAny(values[0], "\r\n") {
		return "", fmt.Errorf("invalid %s header", name)
	}
	return values[0], nil
}
func deleteHeader(headers http.Header, name string) {
	for key := range headers {
		if strings.EqualFold(key, name) {
			delete(headers, key)
		}
	}
}
func validDomain(domain string) bool {
	return len(domain) > 0 && len(domain) <= 128 && !strings.ContainsAny(domain, "\x00\r\n ")
}

func EncodeGrant(g Grant, key ed25519.PrivateKey) (string, error) {
	if !validPrivateKey(key) {
		return "", errors.New("invalid grant signing key")
	}
	if err := validateGrant(g); err != nil {
		return "", err
	}
	data, err := json.Marshal(g)
	if err != nil {
		return "", err
	}
	if len(data) > MaxBodyBytes {
		return "", errors.New("grant exceeds limit")
	}
	sig := ed25519.Sign(key, append([]byte(grantPrefix), data...))
	return base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func DecodeGrant(token, domain, audience string, key ed25519.PublicKey, now time.Time) (Grant, error) {
	var g Grant
	if !validPublicKey(key) || !validDomain(domain) || len(token) > 2*MaxBodyBytes {
		return g, errors.New("invalid grant input")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return g, errors.New("invalid grant encoding")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(data) > MaxBodyBytes {
		return g, errors.New("invalid grant payload")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(key, append([]byte(grantPrefix), data...), sig) {
		return g, errors.New("invalid grant signature")
	}
	if err = strictJSON(bytes.NewReader(data), &g); err != nil {
		return Grant{}, err
	}
	if err = validateGrant(g); err != nil {
		return Grant{}, err
	}
	expected, err := ValidateOrigin(audience, true)
	if err != nil || g.Audience != expected || g.Domain != domain {
		return Grant{}, errors.New("grant audience or domain mismatch")
	}
	if now.Unix() >= g.ExpiresAt {
		return Grant{}, errors.New("grant expired")
	}
	return g, nil
}
func validateGrant(g Grant) error {
	if g.Version != 1 || g.NodeID <= 0 || !validDomain(g.Domain) || g.ExpiresAt <= 0 || len(g.Body) == 0 || !json.Valid(g.Body) {
		return errors.New("invalid grant scope")
	}
	origin, err := ValidateOrigin(g.Audience, true)
	if err != nil || origin != g.Audience {
		return errors.New("invalid grant audience")
	}
	return nil
}

func (b Binding) Validate() error {
	if b.Version != 1 || b.NodeID <= 0 || !validPublicKey(b.CorePublicKey) || !validPrivateKey(b.NodePrivateKey) {
		return errors.New("invalid worker binding identity")
	}
	core, err := ValidateOrigin(b.CoreURL, b.AllowHTTP)
	if err != nil || core != b.CoreURL {
		return errors.New("invalid core origin")
	}
	node, err := ValidateOrigin(b.NodeURL, b.AllowHTTP)
	if err != nil || node != b.NodeURL {
		return errors.New("invalid node origin")
	}
	return nil
}
func DecodeBinding(r io.Reader) (Binding, error) {
	var b Binding
	if err := strictJSON(r, &b); err != nil {
		return b, err
	}
	if err := b.Validate(); err != nil {
		return Binding{}, err
	}
	return b, nil
}
func strictJSON(r io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(r, MaxBodyBytes+1))
	if err != nil {
		return errors.New("cannot read protocol JSON")
	}
	if len(data) > MaxBodyBytes {
		return errors.New("protocol JSON exceeds limit")
	}
	// Reject repeated top-level keys as well as unknown fields.
	keys := json.NewDecoder(bytes.NewReader(data))
	token, err := keys.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("expected JSON object")
	}
	seen := map[string]bool{}
	for keys.More() {
		token, err = keys.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return errors.New("duplicate JSON field")
		}
		seen[key] = true
		var value json.RawMessage
		if err = keys.Decode(&value); err != nil {
			return err
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return err
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("trailing protocol JSON")
	}
	return nil
}
func SaveBinding(path string, b Binding) error {
	if err := b.Validate(); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(b, "", "  ")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600) //nolint:gosec // Operator-selected binding path is created exclusively with private permissions.
	if err != nil {
		return err
	}
	good := false
	defer func() {
		_ = file.Close()
		if !good {
			_ = os.Remove(path)
		}
	}()
	if _, err = file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	good = true
	return nil
}
func LoadBinding(path string) (Binding, error) {
	var b Binding
	info, err := os.Lstat(path)
	if err != nil {
		return b, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return b, errors.New("worker binding requires a private regular file")
	}
	file, err := os.Open(path) //nolint:gosec // Operator-selected binding must match the private regular file checked before and after open.
	if err != nil {
		return b, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return b, err
	}
	if !os.SameFile(info, opened) || opened.Mode().Perm()&0077 != 0 {
		return b, errors.New("worker binding changed while opening")
	}
	return DecodeBinding(file)
}

// ValidateOrigin returns the canonical authority with no credentials or path.
func ValidateOrigin(origin string, allowHTTP bool) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("invalid protocol origin")
	}
	if u.Scheme != "https" && (!allowHTTP || u.Scheme != "http") {
		return "", errors.New("protocol requires HTTPS or explicitly allowed HTTP")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || strings.ContainsAny(host, "%\\\x00 \t\r\n") {
		return "", errors.New("invalid origin host")
	}
	port := u.Port()
	if port != "" {
		number, e := strconv.Atoi(port)
		if e != nil || number < 1 || number > 65535 {
			return "", errors.New("invalid origin port")
		}
		port = strconv.Itoa(number)
	}
	if port == "443" && u.Scheme == "https" || port == "80" && u.Scheme == "http" {
		port = ""
	}
	if strings.Contains(host, ":") {
		if net.ParseIP(host) == nil {
			return "", errors.New("invalid origin address")
		}
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return u.Scheme + "://" + host, nil
}
func requestOrigin(req *http.Request, origin string, allowHTTP bool) error {
	if req == nil || req.URL == nil || req.Method == "" || req.URL.User != nil || req.URL.Fragment != "" || req.URL.Opaque != "" {
		return errors.New("invalid request target")
	}
	expected, _ := url.Parse(origin)
	if req.URL.IsAbs() {
		actual, err := ValidateOrigin(req.URL.Scheme+"://"+req.URL.Host, allowHTTP)
		if err != nil || actual != origin {
			return errors.New("request origin mismatch")
		}
	}
	if req.Host != "" {
		actual, err := ValidateOrigin(expected.Scheme+"://"+req.Host, allowHTTP)
		if err != nil || actual != origin {
			return errors.New("request host mismatch")
		}
	}
	return nil
}

type checkedTransport struct {
	base      *http.Transport
	allowHTTP bool
}

func (t *checkedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil {
		return nil, errors.New("missing request URL")
	}
	if req.URL.User != nil || req.URL.Fragment != "" {
		return nil, errors.New("invalid transport URL")
	}
	if _, err := ValidateOrigin(req.URL.Scheme+"://"+req.URL.Host, t.allowHTTP); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}
func NewHTTPClient(allowHTTP bool, timeout time.Duration) *http.Client {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	transport := base.Clone()
	transport.Proxy = nil
	transport.DisableCompression = true
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return &http.Client{Timeout: timeout, Transport: &checkedTransport{base: transport, allowHTTP: allowHTTP}, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("worker protocol redirects are disabled")
	}}
}
func validPrivateKey(key ed25519.PrivateKey) bool {
	if len(key) != ed25519.PrivateKeySize {
		return false
	}
	if bytes.Equal(key[:ed25519.SeedSize], make([]byte, ed25519.SeedSize)) {
		return false
	}
	derived := ed25519.NewKeyFromSeed(key[:ed25519.SeedSize])
	return bytes.Equal(key, derived) && validPublicKey(ed25519.PublicKey(key[ed25519.SeedSize:]))
}
func validPublicKey(key ed25519.PublicKey) bool {
	if len(key) != ed25519.PublicKeySize {
		return false
	}
	var y [32]byte
	copy(y[:], key)
	y[31] &= 0x7f
	var prime [32]byte
	for i := range prime {
		prime[i] = 0xff
	}
	prime[0] = 0xed
	prime[31] = 0x7f
	for i := 31; i >= 0; i-- {
		if y[i] > prime[i] {
			return false
		}
		if y[i] < prime[i] {
			break
		}
		if i == 0 {
			return false
		}
	}
	// These are the compressed y-coordinates of the small-order points.
	for _, encoded := range []string{"0000000000000000000000000000000000000000000000000000000000000000", "0100000000000000000000000000000000000000000000000000000000000000", "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05", "c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a", "ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f"} {
		point, _ := hex.DecodeString(encoded)
		if bytes.Equal(y[:], point) {
			return false
		}
	}
	return true
}

// ValidatePublicKey rejects malformed, noncanonical and small-order key
// encodings before they are accepted as protocol identities.
func ValidatePublicKey(key ed25519.PublicKey) error {
	if !validPublicKey(key) {
		return errors.New("invalid worker public key")
	}
	return nil
}
