package directconn

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/darthShadow/shunpike/coordination/protocol"
)

// ErrSettingsNotRead is the error CapabilityHost wraps until this replica has
// read the direct-connection settings once. The API answers it as a dependency
// that is not ready, not as an internal failure.
var ErrSettingsNotRead = errors.New("direct-connection settings are not read yet")

// HostSource reports the API host's current direct-connection state.
type HostSource interface {
	CapabilityHost(ctx context.Context) (CapabilityHost, error)
}

// CapabilityHost is what the capability document needs from the API host.
type CapabilityHost struct {
	// Enabled is the administrator's direct_connections.enabled setting.
	Enabled     bool
	STUNServers []string
	// Available is true while the engine is up and the replica gate admits new
	// direct connections. A lease holder that the gate closes keeps its engine
	// running and reports false.
	Available bool
	// EngineRunning is true while this replica's engine is up, whether or not
	// the replica gate admits new direct connections.
	EngineRunning bool
	NodeKey       protocol.NodeKey
	// Draining is true once the server has begun to shut down.
	Draining bool
}

// CapabilityHostID is the host label clients and the store use for the API host.
const CapabilityHostID = coordinationHostLabel

// Capability is the data of the direct-connections capability document.
type Capability struct {
	Enabled          bool
	ProtocolRevision string
	TLSPin           string
	STUNServers      []string
	ACKDecimation    bool
	ReceiveWindowMiB int
	Host             CapabilityHost
}

// Capability reads the toggle and the API host, and adds the process-wide
// values that never change while the server runs.
func (c *Coordination) Capability(ctx context.Context) (Capability, error) {
	if c.cfg.Host == nil {
		return Capability{}, nil
	}
	host, err := c.cfg.Host.CapabilityHost(ctx)
	if err != nil {
		return Capability{}, err
	}
	host.Draining = c.draining.Load()
	return Capability{
		Enabled: host.Enabled, ProtocolRevision: protocol.ProtocolRevision, TLSPin: c.cfg.TLSPin, STUNServers: host.STUNServers,
		ACKDecimation: c.cfg.ACKDecimation, ReceiveWindowMiB: c.cfg.ReceiveWindowMiB, Host: host,
	}, nil
}

// CapabilityHost reports the toggle, the STUN servers and the API host's state
// for the capability document. It reads no settings: the toggle and the servers
// are the values the service last read, taken with the engine's state in one
// hold of the lifecycle lock. Before the first read it returns an error wrapping
// ErrSettingsNotRead. A disable the service has read shows at once, even while
// the engine is still stopping; EngineRunning reports the engine apart.
func (s *Service) CapabilityHost(context.Context) (CapabilityHost, error) {
	l := s.lifecycle
	l.mu.Lock()
	values, ready, engineRunning := l.values, l.ready, l.feature.State == stateOn
	l.mu.Unlock()
	if !ready {
		return CapabilityHost{}, fmt.Errorf("capability host: %w", ErrSettingsNotRead)
	}
	key, err := apiNodeKey(s.cfg.SecretKey)
	if err != nil {
		return CapabilityHost{}, err
	}
	servers := []string{}
	for _, server := range values.servers() {
		if server = strings.TrimSpace(server); server != "" {
			servers = append(servers, server)
		}
	}
	return CapabilityHost{
		Enabled: values.enabled, STUNServers: servers, Available: engineRunning && s.ReplicaGate().Available,
		EngineRunning: engineRunning, NodeKey: key.Public(),
	}, nil
}
