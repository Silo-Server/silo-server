package nodepool

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/secret"
)

const (
	// keyRegistryRefreshInterval recovers from a missed node_pool_changed
	// event: a deleted node or a rotated key is gone within this long anyway.
	keyRegistryRefreshInterval = 60 * time.Second
	// keyRegistryMissInterval bounds how often a lookup for an unknown node
	// (or a keyless one) may reload the registry, so a stream of requests for
	// a node that does not exist cannot turn into a stream of queries.
	keyRegistryMissInterval = time.Second
	// keyRegistryRefreshTimeout bounds one reload.
	keyRegistryRefreshTimeout = 10 * time.Second
)

// KeyRegistry holds the credential of every stream node, enabled or not, by
// node id and by URL: the decrypted key of each transcode node that has one,
// a "no key issued" marker for each that does not, and the type of each
// proxy. The placement pools are no substitute: they hold only enabled
// nodes, and a disabled node keeps serving the streams it already has.
//
// It reloads on Invalidate (wired to node_pool_changed), every 60 seconds,
// and on a lookup miss at most once a second, so a deleted node or a rotated
// key disappears within one refresh. Concurrent reloads share one database
// read. A KeyRegistry is safe for concurrent use.
type KeyRegistry struct {
	load   func(context.Context) ([]nodeCredentialRow, error)
	cipher *secret.Cipher
	now    func() time.Time
	wake   chan struct{}

	// reloadMu guards inflight and lastDone. inflight is the reload running
	// now, if any, which every caller that may share it waits on. lastDone is
	// when the last reload finished, successful or not; the miss cooldown
	// runs from it.
	reloadMu sync.Mutex
	inflight *registryReload
	lastDone time.Time

	mu    sync.RWMutex
	byID  map[int]*nodeCredential
	byURL map[string]*nodeCredential
}

// registryReload is one reload in progress. err is set before done closes.
type registryReload struct {
	done chan struct{}
	err  error
}

// nodeCredential is one node as the registry holds it.
type nodeCredential struct {
	id         int
	name       string
	nodeType   string
	url        string
	ciphertext string
	// key is the decrypted node key; empty when none is issued or the stored
	// one cannot be decrypted.
	key string
}

// NewKeyRegistry returns an empty registry that reads repo and decrypts keys
// with cipher. Call Refresh before relying on it and Start to keep it current.
func NewKeyRegistry(repo *Repository, cipher *secret.Cipher) *KeyRegistry {
	return newKeyRegistry(repo.listNodeCredentials, cipher)
}

func newKeyRegistry(load func(context.Context) ([]nodeCredentialRow, error), cipher *secret.Cipher) *KeyRegistry {
	return &KeyRegistry{
		load:   load,
		cipher: cipher,
		now:    time.Now,
		wake:   make(chan struct{}, 1),
		byID:   map[int]*nodeCredential{},
		byURL:  map[string]*nodeCredential{},
	}
}

// Start reloads the registry every 60 seconds and on Invalidate until ctx
// ends. It does not load first; call Refresh for that.
func (r *KeyRegistry) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(keyRegistryRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-r.wake:
			}
			if err := r.Refresh(ctx); err != nil && ctx.Err() == nil {
				slog.WarnContext(ctx, "node credential reload failed; keeping the credentials already loaded",
					"component", "nodepool", "error", err)
			}
		}
	}()
}

// Invalidate asks the Start loop to reload soon, without waiting for it. Wire
// it to node_pool_changed.
func (r *KeyRegistry) Invalidate() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Refresh reloads every node's credential and returns once the registry
// reflects the rows as they are now. A reload already running may have read
// them before the change the caller wants to see, so Refresh waits it out and
// then starts, or joins, the next one. On failure the registry keeps what it
// had. ctx bounds only the wait: the reload itself runs under its own timeout,
// since other callers may be sharing it.
func (r *KeyRegistry) Refresh(ctx context.Context) error {
	r.reloadMu.Lock()
	if running := r.inflight; running != nil {
		r.reloadMu.Unlock()
		select {
		case <-running.done:
		case <-ctx.Done():
			return ctx.Err()
		}
		r.reloadMu.Lock()
	}
	// Whatever runs now started after this call did, so its rows are fresh
	// enough to share.
	reload := r.inflight
	if reload == nil {
		reload = r.startReloadLocked(ctx)
	}
	r.reloadMu.Unlock()
	select {
	case <-reload.done:
		return reload.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// refreshAfterMiss reloads after a lookup found nothing usable. A reload that
// is already running is joined rather than repeated, and within
// keyRegistryMissInterval of the last one finishing nothing is reloaded or
// waited for. Lookups that reach this are not authenticated, so this bounds
// the database reads they can cause to one at a time, a second apart.
func (r *KeyRegistry) refreshAfterMiss() {
	r.reloadMu.Lock()
	reload := r.inflight
	if reload == nil {
		if !r.lastDone.IsZero() && r.now().Sub(r.lastDone) < keyRegistryMissInterval {
			r.reloadMu.Unlock()
			return
		}
		reload = r.startReloadLocked(context.Background())
	}
	r.reloadMu.Unlock()
	<-reload.done
	if reload.err != nil {
		slog.Warn("node credential reload after a lookup miss failed", "component", "nodepool", "error", reload.err)
	}
}

// startReloadLocked starts a reload in the background and records it as the
// one in flight. The caller holds reloadMu. The reload keeps ctx's values but
// not its cancellation: other callers may join it.
func (r *KeyRegistry) startReloadLocked(ctx context.Context) *registryReload {
	reload := &registryReload{done: make(chan struct{})}
	r.inflight = reload
	go func() {
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), keyRegistryRefreshTimeout)
		reload.err = r.reload(loadCtx)
		cancel()
		r.reloadMu.Lock()
		r.inflight = nil
		r.lastDone = r.now()
		r.reloadMu.Unlock()
		close(reload.done)
	}()
	return reload
}

// reload reads every node's credential and replaces the registry's maps. On
// failure the registry keeps what it had. Only one runs at a time.
func (r *KeyRegistry) reload(ctx context.Context) error {
	rows, err := r.load(ctx)
	if err != nil {
		return err
	}
	r.mu.RLock()
	previous := r.byID
	r.mu.RUnlock()

	byID := make(map[int]*nodeCredential, len(rows))
	byURL := make(map[string]*nodeCredential, len(rows))
	for _, row := range rows {
		cred := &nodeCredential{id: row.ID, name: row.Name, nodeType: row.Type, url: row.URL}
		if row.Type == NodeTypeTranscode && row.Ciphertext != nil && *row.Ciphertext != "" {
			cred.ciphertext = *row.Ciphertext
			if old := previous[row.ID]; old != nil && old.ciphertext == cred.ciphertext {
				// Unchanged since the last load: no need to decrypt (or warn)
				// again.
				cred.key = old.key
			} else if key, err := DecryptNodeKey(r.cipher, row.ID, cred.ciphertext); err == nil {
				cred.key = key
			} else {
				slog.WarnContext(ctx, "a transcode node's stored key cannot be decrypted, so nothing can sign for it; issue it a new key",
					"component", "nodepool", "node_id", row.ID, "error", err)
			}
		}
		byID[row.ID] = cred
		url := CanonicalNodeURL(row.URL)
		// URLs are unique per row but not after canonicalization ("http://n"
		// and "http://N/"). A transcode node wins such a tie: signing for it
		// with the shared secret is the one outcome that must never happen.
		if existing := byURL[url]; existing == nil || existing.nodeType != NodeTypeTranscode {
			byURL[url] = cred
		}
	}
	r.mu.Lock()
	r.byID, r.byURL = byID, byURL
	r.mu.Unlock()
	return nil
}

func (r *KeyRegistry) lookupID(nodeID int) *nodeCredential {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byID[nodeID]
}

func (r *KeyRegistry) lookupURL(nodeURL string) *nodeCredential {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byURL[CanonicalNodeURL(nodeURL)]
}

// keyed reports whether cred is a transcode node with a usable key.
func (c *nodeCredential) keyed() bool {
	return c != nil && c.nodeType == NodeTypeTranscode && c.key != ""
}

// isProxy reports whether cred is a proxy.
func (c *nodeCredential) isProxy() bool {
	return c != nil && c.nodeType == NodeTypeProxy
}

// transcodeByURL returns the credential of the transcode node at nodeURL. A
// proxy at that URL, a keyless node, and no node at all are misses, which
// reload once: the URL may have been given to a transcode node since, or the
// node issued a key, and a cached proxy entry must never stand in for it.
func (r *KeyRegistry) transcodeByURL(nodeURL string) *nodeCredential {
	if cred := r.lookupURL(nodeURL); cred.keyed() {
		return cred
	}
	r.refreshAfterMiss()
	return r.lookupURL(nodeURL)
}

// proxyByURL reports whether a proxy is registered at nodeURL, reloading once
// on a miss. A URL a transcode node shares is never a proxy's.
func (r *KeyRegistry) proxyByURL(nodeURL string) bool {
	if r.lookupURL(nodeURL).isProxy() {
		return true
	}
	r.refreshAfterMiss()
	return r.lookupURL(nodeURL).isProxy()
}

// nodeKey is NodeKey's answer for cred.
func nodeKey(cred *nodeCredential) (string, error) {
	switch {
	case cred == nil:
		return "", ErrNodeNotFound
	case cred.nodeType != NodeTypeTranscode:
		return "", ErrNodeKeyNotIssuable
	case cred.key == "":
		return "", nodeauth.ErrNoNodeKey
	}
	return cred.key, nil
}

// NodeKey returns the key of transcode node nodeID, whether or not the node
// is enabled, reloading once on a miss. A deleted or unknown node is
// ErrNodeNotFound, a proxy ErrNodeKeyNotIssuable, and a transcode node
// without a usable key nodeauth.ErrNoNodeKey.
func (r *KeyRegistry) NodeKey(nodeID int) (string, error) {
	if cred := r.lookupID(nodeID); cred.keyed() {
		return cred.key, nil
	}
	r.refreshAfterMiss()
	return nodeKey(r.lookupID(nodeID))
}

// ReloadNodeKey is NodeKey after a reload, under the same bound as a miss: a
// request from node nodeID failed to verify with the key loaded for it, and
// the node may hold one issued since. It implements nodeauth.NodeKeys.
func (r *KeyRegistry) ReloadNodeKey(nodeID int) (string, error) {
	r.refreshAfterMiss()
	return nodeKey(r.lookupID(nodeID))
}

// Identity returns node nodeID's id, name, type and URL as last loaded, and
// false when the registry does not hold it. It never reloads: a caller that
// needs a node the registry missed reads the row itself
// (Repository.GetIdentity). A rename or a new URL reaches it with the reload
// node_pool_changed asks for, else within a minute.
func (r *KeyRegistry) Identity(nodeID int) (Identity, bool) {
	cred := r.lookupID(nodeID)
	if cred == nil {
		return Identity{}, false
	}
	return Identity{ID: cred.id, Name: cred.name, Type: cred.nodeType, URL: cred.url}, true
}

// HasKey reports whether transcode node nodeID has a usable key in the
// registry as last loaded. It never reloads, so placement can ask it under
// its own lock: a node without a key cannot be signed for, and so takes no
// new work.
func (r *KeyRegistry) HasKey(nodeID int) bool {
	return r.lookupID(nodeID).keyed()
}

// SignTarget is the kind of worker a request to n is signed for. Anything
// not recorded as a proxy is signed for as a transcode node, which only ever
// gets its own key.
func (n *Node) SignTarget() nodeauth.Target {
	if n != nil && n.Type == NodeTypeProxy {
		return nodeauth.TargetProxy
	}
	return nodeauth.TargetTranscode
}

// Keyring returns the signing keyring for calls to stream nodes: a transcode
// node's own key, and for a proxy the shared secret sharedSecret returns (read
// per call, so a live configuration reload applies).
func (r *KeyRegistry) Keyring(sharedSecret func() string) nodeauth.Keyring {
	return registryKeyring{registry: r, sharedSecret: sharedSecret}
}

type registryKeyring struct {
	registry     *KeyRegistry
	sharedSecret func() string
}

// KeyFor implements nodeauth.Keyring. A transcode target gets only the key of
// the transcode node registered at nodeURL; a proxy target gets the shared
// secret only when nodeURL is registered as a proxy. Anything else is refused
// rather than signed with the shared secret: the URL may be a transcode node
// this registry has not loaded yet.
func (k registryKeyring) KeyFor(target nodeauth.Target, nodeURL string) (string, error) {
	switch target {
	case nodeauth.TargetTranscode:
		cred := k.registry.transcodeByURL(nodeURL)
		switch {
		case cred == nil || cred.nodeType != NodeTypeTranscode:
			return "", nodeauth.ErrUnknownNode
		case cred.key == "":
			return "", nodeauth.ErrNoNodeKey
		}
		return cred.key, nil
	case nodeauth.TargetProxy:
		if !k.registry.proxyByURL(nodeURL) {
			return "", nodeauth.ErrUnknownNode
		}
		if k.sharedSecret == nil {
			return "", nodeauth.ErrNoSecret
		}
		return k.sharedSecret(), nil
	}
	return "", errors.New("node auth: unsupported signing target")
}

// CanonicalNodeURL is the one form node URLs are compared in, for the
// registry's keys and its lookups alike, so that two spellings of one
// address find one node: surrounding whitespace dropped, scheme and host
// lowercased, a default port dropped, the path compared decoded and without
// a trailing slash. A URL that does not parse as absolute keeps the pools'
// trailing-slash normalization. It is only a lookup key: the path a request
// is signed over is never derived from it.
func CanonicalNodeURL(nodeURL string) string {
	trimmed := strings.TrimSpace(nodeURL)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return normalizeNodeURL(trimmed)
	}
	scheme := strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := parsed.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host + strings.TrimRight(parsed.Path, "/")
}
