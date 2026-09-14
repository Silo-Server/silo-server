package netaccess

import "sync"

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
	return b.Registry.Issue(installationID, provider)
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

// Report records a provider status push.
func (b *Broker) Report(status Status) (Status, bool) {
	if b == nil {
		return Status{}, false
	}
	return b.Status.Report(status)
}
