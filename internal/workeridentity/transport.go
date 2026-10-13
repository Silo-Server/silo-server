package workeridentity

import (
	"crypto/tls"
	"errors"
	"net/http"
	"sync"
)

// TransportPool isolates enrolled-worker links from environment proxies and
// TLS bypasses. Clones retain caller deadlines and reuse connection pools.
type TransportPool struct {
	mu         sync.Mutex
	transports map[*http.Transport]*http.Transport
}

func (p *TransportPool) Get(base http.RoundTripper) (http.RoundTripper, error) {
	if base == nil {
		base = http.DefaultTransport
	}
	transport, ok := base.(*http.Transport)
	if !ok {
		// In-process adapters may implement RoundTripper without a network
		// transport. Production node clients use http.Transport.
		return base, nil
	}
	if transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify {
		return nil, errors.New("worker transport requires verified TLS")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing := p.transports[transport]; existing != nil {
		return existing, nil
	}
	if len(p.transports) >= 32 {
		return nil, errors.New("worker transport pool limit reached")
	}
	if p.transports == nil {
		p.transports = make(map[*http.Transport]*http.Transport)
	}
	clone := transport.Clone()
	clone.Proxy = nil
	clone.DisableCompression = true
	if clone.TLSClientConfig == nil {
		clone.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else if clone.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		clone.TLSClientConfig.MinVersion = tls.VersionTLS12
	}
	// The authenticated origin must also select the certificate hostname.
	clone.TLSClientConfig.ServerName = ""
	p.transports[transport] = clone
	return clone, nil
}
