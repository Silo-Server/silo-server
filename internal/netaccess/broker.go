package netaccess

import (
	"crypto/subtle"
	"sync"
)

// Broker ties the token registry and the status cache to resident plugin
// process lifetimes: a start issues a fresh ingress token, a stop revokes it
// and forgets the status so no stale overlay origin survives the process.
// The resident supervisor in internal/plugins drives it.
type Broker struct {
	Registry *Registry
	Status   *StatusCache

	// mu serializes Issue and Revoke. Registry.Revoke and Status.Forget are
	// two operations; without this a replacement's Issue (and the status its
	// process then pushes) could land between them and be forgotten by the
	// old process's revoke. A process cannot report before it was issued a
	// token, so ordering Issue after Revoke completes is enough.
	mu sync.Mutex
}

// NewBroker returns a broker over a fresh registry and status cache.
func NewBroker() *Broker {
	return &Broker{Registry: NewRegistry(), Status: NewStatusCache()}
}

// Issue mints the installation's ingress token for a starting process.
func (b *Broker) Issue(installationID int, provider string) (string, error) {
	if b == nil {
		return "", nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	token, err := b.Registry.Issue(installationID, provider)
	if err == nil {
		b.Status.Forget(installationID)
	}
	return token, err
}

// Revoke forgets the installation's token and last reported status, provided
// token is still the current one; a stale token (the process was already
// replaced) changes nothing.
func (b *Broker) Revoke(installationID int, token string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Registry.Revoke(installationID, token) {
		b.Status.Forget(installationID)
	}
}

// IngressToken returns the installation's current token.
func (b *Broker) IngressToken(installationID int) (string, bool) {
	if b == nil {
		return "", false
	}
	return b.Registry.IngressToken(installationID)
}

// Report records a provider status push from this process's own reads and
// commands (see plugins.Service.applyNetworkAccess), which run against the
// current instance by construction.
func (b *Broker) Report(status Status) (Status, bool) {
	if b == nil {
		return Status{}, false
	}
	return b.Status.Report(status)
}

// ReportFor records a status push from the plugin process holding token.
// The registry check and the cache write happen under the same lock Revoke
// takes, so a push that was in flight while its process was revoked lands
// after the revoke and is refused rather than resurrecting the old origin.
func (b *Broker) ReportFor(installationID int, token string, status Status) (previous Status, changed bool, accepted bool) {
	if b == nil {
		return Status{}, false, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	current, ok := b.Registry.IngressToken(installationID)
	if !ok || subtle.ConstantTimeCompare([]byte(current), []byte(token)) != 1 {
		return Status{}, false, false
	}
	previous, changed = b.Status.Report(status)
	return previous, changed, true
}
