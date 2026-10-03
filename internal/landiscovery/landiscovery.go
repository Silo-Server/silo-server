// Package landiscovery advertises the API server on the local network with
// DNS-SD over multicast DNS (Bonjour), so a client with no saved address can
// list the Silo servers on its LAN.
//
// The advertisement is a hint, not a credential: it carries the deployment's
// public server identity (see internal/serveridentity) so clients can group
// what they find with servers they already know, and a client confirms a
// found address with GET /api/v2/system/identity before using it. Overlay
// networks do not carry multicast, so a provider such as Tailscale is found
// through its own DNS name, not through this package.
package landiscovery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/brutella/dnssd"
	dnssdlog "github.com/brutella/dnssd/log"
)

// ServiceType is the DNS-SD service type clients browse for.
const ServiceType = "_silo._tcp"

// TXT record keys. TXTVersion changes only when an existing key changes
// meaning; new keys are added without bumping it.
const (
	TXTKeyVersion  = "v"
	TXTKeyServerID = "id"
	TXTVersion     = "1"
)

// maxInstanceNameBytes is the DNS label limit an instance name must fit in.
const maxInstanceNameBytes = 63

// defaultInstanceName matches branding's default server name.
const defaultInstanceName = "Silo"

// Config describes the one service an API process advertises.
type Config struct {
	// Name is the instance name browsers show, normally the branding server
	// name. A name another host already uses on the link is renamed by the
	// responder ("Silo (2)"); clients tell servers apart by ServerID.
	Name string
	// ServerID is the deployment's native server identity.
	ServerID string
	// Port is the TCP port the API listener accepts plain HTTP on.
	Port int
}

// ErrLoopbackOnly reports that the API listener is bound to a loopback
// address, so nothing on the LAN could connect to an advertised port.
var ErrLoopbackOnly = errors.New("lan discovery: API listener is bound to loopback")

// PortFromAddr returns the port to advertise for the bound API listener
// address, or ErrLoopbackOnly when the listener only accepts local
// connections.
func PortFromAddr(addr net.Addr) (int, error) {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || tcp.Port == 0 {
		return 0, fmt.Errorf("lan discovery: unsupported listener address %v", addr)
	}
	if tcp.IP != nil && tcp.IP.IsLoopback() {
		return 0, ErrLoopbackOnly
	}
	return tcp.Port, nil
}

var quietLibraryLog sync.Once

// Advertise announces cfg on every multicast-capable interface and answers
// queries for it until ctx is canceled, then sends goodbye packets and
// returns ctx.Err(). It returns earlier only when the responder cannot start,
// for example when no interface supports multicast.
func Advertise(ctx context.Context, cfg Config) error {
	quietLibraryLog.Do(func() {
		// The library logs to stdout by default; Silo logs through slog.
		dnssdlog.Info.SetOutput(io.Discard)
		dnssdlog.Debug.SetOutput(io.Discard)
	})
	srvCfg, err := serviceConfig(cfg)
	if err != nil {
		return err
	}
	service, err := dnssd.NewService(srvCfg)
	if err != nil {
		return fmt.Errorf("lan discovery: %w", err)
	}
	responder, err := dnssd.NewResponder()
	if err != nil {
		return fmt.Errorf("lan discovery: %w", err)
	}
	if _, err := responder.Add(service); err != nil {
		return fmt.Errorf("lan discovery: %w", err)
	}
	slog.InfoContext(ctx, "advertising Silo on the local network",
		"service", ServiceType, "name", srvCfg.Name, "host", srvCfg.Host+".local", "port", srvCfg.Port)
	return responder.Respond(ctx)
}

// serviceConfig builds the DNS-SD registration for cfg.
//
// The host name is derived from the server ID rather than the machine's
// hostname, so the responder never contends with the operating system's own
// mDNS daemon for "<hostname>.local" records.
func serviceConfig(cfg Config) (dnssd.Config, error) {
	id := strings.TrimSpace(cfg.ServerID)
	if id == "" {
		return dnssd.Config{}, errors.New("lan discovery: server ID is required")
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return dnssd.Config{}, fmt.Errorf("lan discovery: invalid port %d", cfg.Port)
	}
	return dnssd.Config{
		Name: instanceName(cfg.Name),
		Type: ServiceType,
		Host: hostLabel(id),
		Port: cfg.Port,
		Text: map[string]string{
			TXTKeyVersion:  TXTVersion,
			TXTKeyServerID: id,
		},
	}, nil
}

// instanceName trims name to a DNS label, falling back to defaultInstanceName.
func instanceName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return defaultInstanceName
	}
	if len(name) <= maxInstanceNameBytes {
		return name
	}
	// Cut on a rune boundary so a multi-byte name stays valid UTF-8.
	cut := maxInstanceNameBytes
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}
	return strings.TrimSpace(name[:cut])
}

// hostLabel returns "silo-" plus the first eight alphanumerics of the server
// ID: stable for the deployment and distinct between deployments on one LAN.
func hostLabel(serverID string) string {
	var b strings.Builder
	b.WriteString("silo-")
	n := 0
	for _, r := range strings.ToLower(serverID) {
		if n == 8 {
			break
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			n++
		}
	}
	return b.String()
}
