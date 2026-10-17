// Package workerproxy delegates existing trusted proxy output requests to
// standalone workers without sharing customer secrets with the executor.
package workerproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodehttp"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

type grantKey struct {
	node  string
	token [32]byte
	epoch string
}
type entry struct {
	encoded string
	scope   workeridentity.Grant
}
type Transport struct {
	Origin, Secret string
	SecretValue    func() string
	// Grant uses local core authority when the caller is the core itself.
	// Proxies leave it nil and obtain the same grant through signed HTTP.
	Grant func(context.Context, string, string) (workercontrolcore.GrantReply, error)
	// ResolveOutput is core-only: tokenless customer relays recover the
	// original viewer from admitted recipes or the current download request.
	ResolveOutput func(*http.Request, workercontrolcore.Node) (string, error)
	Nodes         workercontrolcore.NodeRegistry
	client        *http.Client
	mu            sync.Mutex
	cache         map[grantKey]entry
	flights       singleflight.Group
	transports    workeridentity.TransportPool
}

type tokenContextKey struct{}

func customerOutputRequest(request *http.Request) bool {
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/"), "/")
	read := request.Method == http.MethodGet || request.Method == http.MethodHead
	if len(parts) >= 2 && (parts[0] == "transcode" || parts[0] == "remux") {
		return read || request.Method == http.MethodPost && len(parts) == 5 && parts[2] == "segment" && parts[4] == "downloaded"
	}
	return read && len(parts) >= 3 && parts[0] == "downloads" && parts[1] == "artifacts"
}

func artifactStatRequest(request *http.Request) bool {
	marked := nodehttp.IsArtifactStat(request.Context())
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/"), "/")
	return marked && request.Method == http.MethodHead && len(parts) == 3 && parts[0] == "downloads" && parts[1] == "artifacts" && parts[2] != "" && parts[2] != "." && parts[2] != ".."
}

// WithToken preserves the verified output credential for post-response
// acknowledgements, which have their own short request lifetime.
func WithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tokenContextKey{}, token)
}

func New(origin, secret string, nodes workercontrolcore.NodeRegistry) (*Transport, error) {
	allowHTTP := strings.HasPrefix(strings.ToLower(origin), "http://")
	canonical, err := workeridentity.ValidateOrigin(strings.TrimRight(origin, "/"), allowHTTP)
	if err != nil || secret == "" || nodes == nil {
		return nil, errors.New("proxy worker authority unavailable")
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("proxy default HTTP transport unavailable")
	}
	base := defaultTransport.Clone()
	base.Proxy = nil
	base.DisableCompression = true
	base.ResponseHeaderTimeout = 3 * time.Second
	base.MaxIdleConnsPerHost = 32
	return &Transport{Origin: canonical, Secret: secret, Nodes: nodes, client: &http.Client{Transport: base, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, cache: make(map[grantKey]entry)}, nil
}

func (t *Transport) Wrap(base http.RoundTripper) http.RoundTripper { return &hop{owner: t, base: base} }

// WrapOutputs delegates customer output traffic and uses commands for
// the core's administrative and execution requests. The two transports are
// alternatives: their proof domains and prepared receipt bridges must not stack.
func (t *Transport) WrapOutputs(base, commands http.RoundTripper) http.RoundTripper {
	return &outputSelection{outputs: t.Wrap(base), commands: commands}
}

type outputSelection struct {
	outputs, commands http.RoundTripper
}

func (s *outputSelection) RoundTrip(request *http.Request) (*http.Response, error) {
	token := request.Header.Get("X-Silo-Stream-Token")
	if token == "" {
		token, _ = request.Context().Value(tokenContextKey{}).(string)
	}
	if token != "" || customerOutputRequest(request) && !artifactStatRequest(request) {
		return s.outputs.RoundTrip(request)
	}
	return s.commands.RoundTrip(request)
}

type hop struct {
	owner *Transport
	base  http.RoundTripper
}

func (h *hop) RoundTrip(request *http.Request) (*http.Response, error) {
	if h.base == nil {
		return (&hop{owner: h.owner, base: http.DefaultTransport}).RoundTrip(request)
	}
	t := h.owner
	url := request.URL.Scheme + "://" + request.URL.Host
	node, found, err := t.Nodes.ByURL(request.Context(), url)
	if err != nil {
		return nil, errors.New("worker identity unavailable")
	}
	if !found {
		return h.base.RoundTrip(request)
	}
	if !node.Enabled {
		return nil, errors.New("worker disabled")
	}
	canonical, err := workeridentity.ValidateOrigin(node.URL, node.AllowHTTP)
	if err != nil || !workeridentity.SameOrigin(canonical, url, node.AllowHTTP) {
		return nil, errors.New("worker transport policy changed")
	}
	url = canonical
	base, err := t.transports.Get(h.base)
	if err != nil {
		return nil, err
	}
	token := request.Header.Get("X-Silo-Stream-Token")
	if token == "" {
		token, _ = request.Context().Value(tokenContextKey{}).(string)
	}
	if token == "" && t.ResolveOutput != nil {
		token, err = t.ResolveOutput(request, node)
		if err != nil {
			return nil, err
		}
	}
	if token == "" {
		return nil, errors.New("standalone output requires authorized stream token")
	}
	secret := t.Secret
	if t.SecretValue != nil {
		secret = t.SecretValue()
	}
	if secret == "" {
		return nil, errors.New("proxy output identity unavailable")
	}
	key := grantKey{url, sha256.Sum256([]byte(token)), workeridentity.OutputEpoch(secret)}
	for attempt := 0; attempt < 2; attempt++ {
		grant, err := t.grant(request.Context(), key, token, secret)
		if err != nil {
			return nil, err
		}
		private, err := workeridentity.OutputPrivateKey(secret, grant.scope)
		if err != nil {
			return nil, err
		}
		out := request.Clone(request.Context())
		out.Header = request.Header.Clone()
		out.Header.Del("Authorization")
		out.Header.Set("X-Silo-Stream-Token", grant.encoded)
		if err = workeridentity.Sign(out, url, workeridentity.DomainProxyOutputRequest, private, node.ID, node.AllowHTTP); err != nil {
			return nil, err
		}
		response, err := base.RoundTrip(out)
		if err == nil {
			if err := verifyOutputReceipt(response, grant); err != nil {
				_ = response.Body.Close()
				return nil, err
			}
		}
		if err != nil || attempt == 1 || response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusConflict {
			return response, err
		}
		_ = response.Body.Close()
		t.mu.Lock()
		delete(t.cache, key)
		t.mu.Unlock()
	}
	return nil, errors.New("worker output denied")
}

func (t *Transport) grant(ctx context.Context, key grantKey, token, secret string) (entry, error) {
	t.mu.Lock()
	cached, ok := t.cache[key]
	t.mu.Unlock()
	if ok && time.Now().Add(10*time.Second).Before(time.Unix(cached.scope.ExpiresAt, 0)) {
		return cached, nil
	}
	result := t.flights.DoChan(key.node+":"+key.epoch+":"+string(key.token[:]), func() (any, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		reply, err := t.requestGrant(ctx, token, key.node, secret)
		if err != nil {
			return nil, err
		}
		scope, err := workeridentity.ReadOutputScope(reply.Grant)
		if err != nil || scope.Audience != key.node || !time.Now().Before(time.Unix(scope.ExpiresAt, 0)) {
			return nil, errors.New("invalid core output scope")
		}
		grant := entry{reply.Grant, scope}
		t.mu.Lock()
		if len(t.cache) >= 4096 {
			clear(t.cache)
		}
		t.cache[key] = grant
		t.mu.Unlock()
		return grant, nil
	})
	select {
	case <-ctx.Done():
		return entry{}, ctx.Err()
	case value := <-result:
		if value.Err != nil {
			return entry{}, value.Err
		}
		grant, ok := value.Val.(entry)
		if !ok {
			return entry{}, errors.New("invalid output grant result")
		}
		return grant, nil
	}
}

func verifyOutputReceipt(response *http.Response, grant entry) error {
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil
	}
	var authority workeridentity.OutputAuthority
	if err := json.Unmarshal(grant.scope.Body, &authority); err != nil {
		return errors.New("invalid output authority")
	}
	if authority.ArtifactFingerprint == "" {
		return nil
	}
	const fingerprintHeader = "X-Silo-Download-Execution-Fingerprint"
	if response.Header.Get(fingerprintHeader) != authority.ArtifactFingerprint || authority.ClientFingerprint == "" {
		return errors.New("artifact receipt mismatch")
	}
	response.Header.Set(fingerprintHeader, authority.ClientFingerprint)
	return nil
}

func (t *Transport) requestGrant(ctx context.Context, token, node, secret string) (workercontrolcore.GrantReply, error) {
	if t.Grant != nil {
		return t.Grant(ctx, token, node)
	}
	var reply workercontrolcore.GrantReply
	body, _ := json.Marshal(map[string]string{"token": token, "node_url": node})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, t.Origin+"/api/internal/workers/output-grant", bytes.NewReader(body))
	if err != nil {
		return workercontrolcore.GrantReply{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if err = nodeauth.Sign(request, t.Origin, secret); err != nil {
		return workercontrolcore.GrantReply{}, err
	}
	response, err := t.client.Do(request)
	if err != nil {
		return workercontrolcore.GrantReply{}, errors.New("core output authority unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return workercontrolcore.GrantReply{}, errors.New("core output authority denied")
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 128<<10)).Decode(&reply); err != nil {
		return workercontrolcore.GrantReply{}, err
	}
	return reply, nil
}
