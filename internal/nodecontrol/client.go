package nodecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/logstream"
	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/opslog"
)

// requestTimeout bounds one call to the core.
const requestTimeout = 10 * time.Second

// maxConfigBytes bounds the configuration document a node reads.
const maxConfigBytes = 1 << 20

// Client is a transcode node's side of the control plane: every call it makes
// to the core, signed with the node's own key.
type Client struct {
	coreURL string
	nodeID  int
	key     string
	http    *http.Client
	// sources reads source bytes (ReadSource): no overall timeout, since a
	// read streams for as long as FFmpeg consumes it.
	sources *http.Client
	// onUnauthorized runs when the core answers a call other than the
	// configuration read with 401. See SetOnUnauthorized.
	onUnauthorized func()
}

// NewClient returns the client of the node that holds nodeKey, calling the
// core at coreURL. The node's id comes from its key, so a malformed key or an
// unusable URL is an error.
func NewClient(coreURL, nodeKey string) (*Client, error) {
	nodeKey = strings.TrimSpace(nodeKey)
	nodeID, err := nodepool.ParseNodeKey(nodeKey)
	if err != nil {
		return nil, err
	}
	coreURL = strings.TrimRight(strings.TrimSpace(coreURL), "/")
	parsed, err := url.Parse(coreURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("core URL must be an absolute http or https URL")
	}
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{coreURL: coreURL, nodeID: nodeID, key: nodeKey, http: &http.Client{
		Timeout: requestTimeout,
		// A redirect would send the signed request somewhere it was not
		// signed for.
		CheckRedirect: noRedirect,
	}, sources: &http.Client{Transport: sourceTransport(), CheckRedirect: noRedirect}}, nil
}

// sourceTransport is the transport of source byte reads: straight to the
// core, never through a proxy, asking for no compression, with an answer due
// within requestTimeout and enough pooled connections for the reads a busy
// node keeps open (every FFmpeg input, and up to 128 reader fetches).
func sourceTransport() http.RoundTripper {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	t := base.Clone()
	t.Proxy = nil
	t.DisableCompression = true
	t.ResponseHeaderTimeout = requestTimeout
	t.MaxIdleConnsPerHost = 64
	return t
}

// NodeID is the node's id, as its key names it.
func (c *Client) NodeID() int { return c.nodeID }

// SetOnUnauthorized sets fn to run when the core answers a call other than
// FetchConfig with 401, so the node rereads its configuration and learns its
// key was rejected without waiting for the next poll. fn runs on every such
// answer, so the caller paces the reads it asks for. The configuration read
// is excluded: it would ask for itself again. fn must not block. Call it
// during construction.
func (c *Client) SetOnUnauthorized(fn func()) {
	c.onUnauthorized = fn
}

// StatusError is an answer from the core other than the one a call expects.
type StatusError struct {
	Path   string
	Status int
	// Reason is a refused resolve's Resolve* reason, or "".
	Reason string
}

func (e *StatusError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("core answered %s with status %d (%s)", e.Path, e.Status, e.Reason)
	}
	return fmt.Sprintf("core answered %s with status %d", e.Path, e.Status)
}

// Status is the HTTP status of a StatusError in err, or 0.
func Status(err error) int {
	if statusErr, ok := errors.AsType[*StatusError](err); ok {
		return statusErr.Status
	}
	return 0
}

// Reason is the Resolve* reason of a StatusError in err, or "".
func Reason(err error) string {
	if statusErr, ok := errors.AsType[*StatusError](err); ok {
		return statusErr.Reason
	}
	return ""
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, header http.Header) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.coreURL+path, reader)
	if err != nil {
		return nil, err
	}
	for name, values := range header {
		req.Header[name] = values
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := nodeauth.SignToCore(req, c.coreURL, c.nodeID, c.key); err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err == nil && resp.StatusCode == http.StatusUnauthorized && path != PathConfig && c.onUnauthorized != nil {
		c.onUnauthorized()
	}
	return resp, err
}

// post sends body to path and requires a 2xx answer.
func (c *Client) post(ctx context.Context, path string, body []byte) error {
	resp, err := c.do(ctx, http.MethodPost, path, body, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &StatusError{Path: path, Status: resp.StatusCode}
	}
	return nil
}

// FetchConfig reads the node's configuration. When etag names the current
// revision the core answers 304 and FetchConfig returns notModified.
func (c *Client) FetchConfig(ctx context.Context, etag string) (doc *NodeConfig, notModified bool, err error) {
	header := http.Header{}
	if etag != "" {
		header.Set(headerIfNoneMatch, etag)
	}
	resp, err := c.do(ctx, http.MethodGet, PathConfig, nil, header)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, true, nil
	case http.StatusOK:
	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, false, &StatusError{Path: PathConfig, Status: resp.StatusCode}
	}
	var decoded NodeConfig
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxConfigBytes)).Decode(&decoded); err != nil {
		return nil, false, fmt.Errorf("decode node configuration: %w", err)
	}
	if decoded.Revision == "" {
		return nil, false, errors.New("node configuration has no revision")
	}
	if decoded.Node.ID != c.nodeID {
		return nil, false, fmt.Errorf("core sent the configuration of node %d to node %d", decoded.Node.ID, c.nodeID)
	}
	return &decoded, false, nil
}

// PostReport sends one encoded Report. The caller encodes it, so it can shrink
// a report whose encoding exceeds MaxBodyBytes before sending. A report the
// core already has a newer one for is a StatusError with status 409.
func (c *Client) PostReport(ctx context.Context, report []byte) error {
	return c.post(ctx, PathReport, report)
}

// maxResolutionBytes bounds the resolve answer a node reads.
const maxResolutionBytes = 1 << 20

// Resolve asks the core for the recipe and playback identity of a transport
// this node serves. A refusal is a StatusError whose Reason is one of the
// Resolve* reasons; any other failure, the core's or the network's, is
// retryable by the node's caller.
func (c *Client) Resolve(ctx context.Context, req ResolveRequest) (*Resolution, error) {
	body, _ := json.Marshal(req) // strings only
	resp, err := c.do(ctx, http.MethodPost, PathResolve, body, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		var refusal ResolveRefusal
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&refusal)
		return nil, &StatusError{Path: PathResolve, Status: resp.StatusCode, Reason: refusal.Reason}
	}
	var resolution Resolution
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResolutionBytes)).Decode(&resolution); err != nil {
		return nil, fmt.Errorf("decode resolution: %w", err)
	}
	return &resolution, nil
}

// maxOpenResponseBytes bounds the sources/open answer a node reads: a grant
// of at most 8 KiB, its path, and a few numbers.
const maxOpenResponseBytes = 64 << 10

// OpenSource exchanges a source grant at the core's sources/open route, for a
// job's input opening or for renewal, and returns the core's answer
// unchecked (nodesource.Opener validates it). A refusal is a
// *nodesource.StatusError with Op "open", whose Fatal the opener's retries go
// by. It is the nodesource.OpenFunc of the node's opener.
func (c *Client) OpenSource(ctx context.Context, grant string) (*nodesource.OpenResponse, error) {
	body, _ := json.Marshal(nodesource.OpenRequest{Grant: grant}) // a string only
	resp, err := c.do(ctx, http.MethodPost, PathSourcesOpen, body, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, &nodesource.StatusError{Op: "open", Status: resp.StatusCode}
	}
	var opened nodesource.OpenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxOpenResponseBytes)).Decode(&opened); err != nil {
		return nil, fmt.Errorf("decode source open answer: %w", err)
	}
	return &opened, nil
}

// ReadSource sends one ranged read of a source's bytes to the core's sources
// route, signed as the node, with the grant in its header (which the
// signature covers), If-Match on the revision and Accept-Encoding identity.
// It returns the answer whatever its status; nodesource checks it and closes
// its body. It is the nodesource.FetchFunc of the node's opener.
func (c *Client) ReadSource(ctx context.Context, read nodesource.ReadRequest) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.coreURL+PathSources, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(nodesource.HeaderSourceGrant, read.Grant)
	req.Header.Set(headerRange, fmt.Sprintf("bytes=%d-%d", read.Offset, read.Offset+read.Length-1))
	req.Header.Set(headerIfMatch, read.ETag)
	req.Header.Set("Accept-Encoding", "identity")
	if err := nodeauth.SignToCore(req, c.coreURL, c.nodeID, c.key); err != nil {
		return nil, err
	}
	resp, err := c.sources.Do(req)
	if err == nil && resp.StatusCode == http.StatusUnauthorized && c.onUnauthorized != nil {
		c.onUnauthorized()
	}
	return resp, err
}

// RevokeRecipes asks the core to withdraw every recipe bound to this node, as
// part of a force reload's teardown. A refusal is a StatusError; 503 and no
// answer are retryable.
func (c *Client) RevokeRecipes(ctx context.Context) error {
	return c.post(ctx, PathRevokeRecipes, []byte("{}"))
}

// NotifyHealthChanged tells the core the node started or stopped draining,
// or finished, so every API server rechecks it at once.
func (c *Client) NotifyHealthChanged(ctx context.Context) error {
	return c.post(ctx, PathHealthChanged, []byte("{}"))
}

// ShipLogs stores a batch of the node's operational log entries on the core.
// It is the Insert of the node's log drain: a batch over MaxLogBatchEntries,
// or whose encoding exceeds MaxBodyBytes, is sent in halves, a single entry
// too large by itself is dropped, and a failure that a retry can fix (no
// answer, 429, 5xx) wraps logstream.ErrRetryable so the drain retries it.
func (c *Client) ShipLogs(ctx context.Context, entries []opslog.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	if len(entries) > MaxLogBatchEntries {
		half := len(entries) / 2
		return errors.Join(c.ShipLogs(ctx, entries[:half]), c.ShipLogs(ctx, entries[half:]))
	}
	body, err := json.Marshal(LogBatch{Entries: entries})
	if err != nil {
		return err
	}
	if len(body) > MaxBodyBytes {
		if len(entries) == 1 {
			return errors.New("log entry exceeds the batch bound by itself; dropped")
		}
		half := len(entries) / 2
		return errors.Join(c.ShipLogs(ctx, entries[:half]), c.ShipLogs(ctx, entries[half:]))
	}
	err = c.post(ctx, PathLogs, body)
	if err == nil {
		return nil
	}
	// Status 0 is no answer: the network or the core is down, which passes.
	if status := Status(err); status == 0 || status == http.StatusTooManyRequests || status >= 500 {
		return fmt.Errorf("%w: %w", logstream.ErrRetryable, err)
	}
	return err
}
