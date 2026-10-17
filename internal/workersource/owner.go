package workersource

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

const ownerHeader = "X-Silo-Source-Owner"
const ownerRenewDomain = "silo/source-owner-renew/v1"
const ownerRenewReplyDomain = "silo/source-owner-renew-reply/v1"
const ownerRenewAction = "renew"

func validateOwnerOptions(o Options) error {
	if len(o.OwnerID) > 256 || strings.IndexFunc(o.OwnerID, func(r rune) bool { return r <= 32 }) >= 0 {
		return ErrDenied
	}
	if o.OwnerURL == "" {
		return nil
	}
	origin, err := workeridentity.ValidateOrigin(o.OwnerURL, true)
	if err != nil || origin != o.OwnerURL || origin == o.Origin || len(o.OwnerKey) != ed25519.PrivateKeySize {
		return ErrDenied
	}
	return nil
}

func (a *Authority) initializeOwner() error {
	transport := a.options.OwnerTransport
	if transport == nil {
		base, ok := http.DefaultTransport.(*http.Transport)
		if !ok || base == nil {
			return ErrUnavailable
		}
		t := base.Clone()
		t.Proxy = nil
		t.DisableCompression = true
		t.ResponseHeaderTimeout = 10 * time.Second
		t.MaxIdleConnsPerHost = a.options.Limits.MaxReaders
		transport = t
	}
	a.ownerClient = &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if len(a.options.OwnerKey) == ed25519.PrivateKeySize {
		key := ed25519.PublicKey(a.options.OwnerKey[ed25519.SeedSize:])
		a.ownerVerifier = &workeridentity.Verifier{Origin: a.options.Origin, Domain: ownerRenewDomain, AllowHTTP: strings.HasPrefix(a.options.Origin, "http://"), ResolveKey: func(int) (ed25519.PublicKey, error) { return key, nil }}
	}
	return nil
}

// Close releases idle relay and renewal connections. HTTP contexts own active
// requests; the shared Store remains an external dependency.
func (a *Authority) Close() { a.ownerClient.CloseIdleConnections() }

type sourceRecordKey struct{}

func (a *Authority) requestRecord(r *http.Request, grant string) (Record, error) {
	if record, ok := r.Context().Value(sourceRecordKey{}).(Record); ok && record.Source.Grant == grant {
		return record, nil
	}
	return a.options.Store.Get(r.Context(), grant)
}

func (a *Authority) ownerRoute(r *http.Request) (node int, grant string, renew bool, ok bool) {
	if !strings.HasPrefix(r.URL.Path, a.routePrefix) || r.URL.RawQuery != "" || r.URL.RawPath != "" {
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, a.routePrefix), "/")
	if len(parts) != 3 && len(parts) != 4 || parts[1] != "sources" {
		return
	}
	node, err := strconv.Atoi(parts[0])
	if err != nil || node < 1 || strconv.Itoa(node) != parts[0] {
		return 0, "", false, false
	}
	parsed, err := uuid.Parse(parts[2])
	if err != nil || parsed.String() != parts[2] {
		return 0, "", false, false
	}
	if len(parts) == 4 {
		if parts[3] != ownerRenewAction || r.Method != http.MethodPost {
			return 0, "", false, false
		}
		renew = true
	} else if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return 0, "", false, false
	}
	return node, parts[2], renew, true
}

// WrapOwner routes source reads and renewals before any worker proof consumes
// its shared nonce. Only the admitting process can interpret its local pin.
func (a *Authority) WrapOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		node, grant, renew, ok := a.ownerRoute(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Encoding", "identity")
		record, err := a.requestRecord(r, grant)
		if err != nil {
			sourceError(w, r, HTTPStatus(err))
			return
		}
		if record.Source.NodeID != node || record.Source.Origin != a.options.Origin || r.Header.Get("X-Silo-Source-Grant") != grant {
			sourceError(w, r, 403)
			return
		}
		if !a.options.Now().Before(record.ExpiresAt) {
			sourceError(w, r, 410)
			return
		}
		if expected := r.Header.Get(ownerHeader); expected != "" && expected != a.options.OwnerID {
			sourceError(w, r, 503)
			return
		}
		if record.OwnerID == "" {
			sourceError(w, r, 503)
			return
		}
		if record.OwnerID != a.options.OwnerID {
			if r.Header.Get(ownerHeader) != "" {
				sourceError(w, r, 503)
				return
			}
			a.forwardOwner(w, r, record)
			return
		}
		if renew && r.Header.Get(workeridentity.HeaderDomain) == ownerRenewDomain {
			a.serveOwnerRenew(w, r, node, record)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sourceRecordKey{}, record)))
	})
}

func (a *Authority) forwardOwner(w http.ResponseWriter, r *http.Request, record Record) {
	// A relay spends the same finite stream budget as a local source reader.
	// Renewal control remains available while those streaming slots are busy.
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		select {
		case a.readers <- struct{}{}:
			defer func() { <-a.readers }()
		default:
			sourceError(w, r, 503)
			return
		}
	}
	target, err := url.Parse(record.OwnerURL)
	canonical, validationErr := workeridentity.ValidateOrigin(record.OwnerURL, true)
	if err != nil || validationErr != nil || canonical != record.OwnerURL {
		sourceError(w, r, 503)
		return
	}
	proxy := httputil.ReverseProxy{Transport: a.ownerClient.Transport, Director: func(out *http.Request) {
		out.URL.Scheme = target.Scheme
		out.URL.Host = target.Host
		out.Header.Set(ownerHeader, record.OwnerID)
	}, ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) { sourceError(w, r, 503) }}
	proxy.ServeHTTP(w, r)
}

type ownerRenewRequest struct {
	OwnerID string `json:"owner_id"`
}
type ownerRenewReply struct {
	OwnerID string            `json:"owner_id"`
	Nonce   string            `json:"nonce"`
	Source  workerwire.Source `json:"source"`
}

func (a *Authority) renewOwner(ctx context.Context, record Record) (workerwire.Source, error) {
	if record.OwnerID == "" || record.OwnerURL == "" || a.ownerVerifier == nil {
		return workerwire.Source{}, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, sourceCheckTimeout)
	defer cancel()
	canonical, err := workeridentity.ValidateOrigin(record.OwnerURL, true)
	if err != nil || canonical != record.OwnerURL {
		return workerwire.Source{}, ErrUnavailable
	}
	body, _ := json.Marshal(ownerRenewRequest{record.OwnerID})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, record.Source.URL+"/"+ownerRenewAction, bytes.NewReader(body))
	if err != nil {
		return workerwire.Source{}, ErrUnavailable
	}
	request.Header.Set("X-Silo-Source-Grant", record.Source.Grant)
	request.Header.Set("If-Match", record.Source.ETag)
	request.Header.Set(ownerHeader, record.OwnerID)
	request.Header.Set(workeridentity.HeaderWorkID, record.Source.WorkID)
	request.Header.Set(workeridentity.HeaderGeneration, strconv.FormatUint(record.Source.Generation, 10))
	if err = workeridentity.Sign(request, a.options.Origin, ownerRenewDomain, a.options.OwnerKey, record.Source.NodeID, strings.HasPrefix(a.options.Origin, "http://")); err != nil {
		return workerwire.Source{}, ErrUnavailable
	}
	nonce := request.Header.Get(workeridentity.HeaderNonce)
	request.Host = request.URL.Host
	target, _ := url.Parse(record.OwnerURL)
	request.URL.Scheme = target.Scheme
	request.URL.Host = target.Host
	response, err := a.ownerClient.Do(request)
	if err != nil {
		return workerwire.Source{}, ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	// Only a verified response establishes a renewal result. An unsigned
	// intermediary error cannot turn a transport failure into authority loss.
	if response.StatusCode != http.StatusOK {
		return workerwire.Source{}, ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return workerwire.Source{}, ErrUnavailable
	}
	var token string
	if json.Unmarshal(data, &token) != nil {
		return workerwire.Source{}, ErrUnavailable
	}
	grant, err := workeridentity.DecodeGrant(token, ownerRenewReplyDomain, a.options.Origin, ed25519.PublicKey(a.options.OwnerKey[ed25519.SeedSize:]), time.Now())
	if err != nil || grant.NodeID != record.Source.NodeID || grant.WorkID != record.Source.WorkID || grant.Generation != record.Source.Generation || grant.ExpiresAt > time.Now().Add(10*time.Second).Unix() {
		return workerwire.Source{}, ErrUnavailable
	}
	var reply ownerRenewReply
	if json.Unmarshal(grant.Body, &reply) != nil || reply.OwnerID != record.OwnerID || reply.Nonce != nonce || reply.Source != record.Source {
		return workerwire.Source{}, ErrUnavailable
	}
	return record.Source, nil
}

func (a *Authority) serveOwnerRenew(w http.ResponseWriter, r *http.Request, node int, record Record) {
	if a.ownerVerifier == nil || a.ownerVerifier.Verify(r, node) != nil {
		sourceError(w, r, 401)
		return
	}
	var input ownerRenewRequest
	data, err := io.ReadAll(io.LimitReader(r.Body, 1024))
	if err != nil || json.Unmarshal(data, &input) != nil || input.OwnerID != a.options.OwnerID || r.Header.Get(workeridentity.HeaderWorkID) != record.Source.WorkID || r.Header.Get(workeridentity.HeaderGeneration) != strconv.FormatUint(record.Source.Generation, 10) || r.Header.Get("If-Match") != record.Source.ETag {
		sourceError(w, r, 403)
		return
	}
	source, err := a.Renew(r.Context(), node, record.Source.WorkID, record.Source.Generation, record.Source.Grant)
	if err != nil {
		sourceError(w, r, HTTPStatus(err))
		return
	}
	body, _ := json.Marshal(ownerRenewReply{a.options.OwnerID, r.Header.Get(workeridentity.HeaderNonce), source})
	token, err := workeridentity.EncodeGrant(workeridentity.Grant{Version: 1, Domain: ownerRenewReplyDomain, Audience: a.options.Origin, NodeID: node, WorkID: source.WorkID, Generation: source.Generation, ExpiresAt: time.Now().Add(5 * time.Second).Unix(), Body: body}, a.options.OwnerKey)
	if err != nil {
		sourceError(w, r, 503)
		return
	}
	encoded, _ := json.Marshal(token) // Encoding a signed string cannot fail.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(append(encoded, '\n'))
}
