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
//
// The package carries its own small responder (responder.go) rather than a
// general mDNS library: it answers only on-link multicast queries, follows
// interfaces as they come and go without withdrawing the service, and owns
// its sockets and goroutines so stopping it leaves nothing behind.
package landiscovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"
	"unicode/utf8"
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

// maxInstanceNameBytes keeps an instance name within the 63-byte DNS label
// limit after a conflict suffix such as " (2)" is appended.
const maxInstanceNameBytes = 63 - len(" (9999)")

// checkInterval is how often Advertise looks for new interfaces, changed
// addresses and a changed server name, and how often it retries after a
// failure.
const checkInterval = 30 * time.Second

// Options describes what an API process advertises. ServerID and Name are
// read live, so a failed read is retried and a rename is picked up.
type Options struct {
	// Port is the TCP port the API listener accepts plain HTTP on.
	Port int
	// IPv4 and IPv6 are the address families the API listener accepts; only
	// those families' addresses are advertised.
	IPv4, IPv6 bool
	// ServerID returns the deployment's native server identity.
	ServerID func(context.Context) (string, error)
	// Name returns the instance name browsers show, normally the branding
	// server name.
	Name func(context.Context) (string, error)
}

// ErrLoopbackOnly reports that the API listener is bound to a loopback
// address, so nothing on the LAN could connect to an advertised port.
var ErrLoopbackOnly = errors.New("lan discovery: API listener is bound to loopback")

// ErrSingleAddress reports that the API listener is bound to one address.
// mDNS answers per link, and a listener on one address is reachable on only
// one of them; advertising it would offer the server where it is not
// serving.
var ErrSingleAddress = errors.New("lan discovery: API listener is bound to a single address")

// Listener returns the port and address families to advertise for the bound
// API listener. Only a listener on every address is advertised: "0.0.0.0"
// accepts IPv4 only, "::" (Go's ":port") both families.
func Listener(addr net.Addr) (port int, ipv4, ipv6 bool, err error) {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || tcp.Port == 0 {
		return 0, false, false, fmt.Errorf("lan discovery: unsupported listener address %v", addr)
	}
	switch {
	case tcp.IP == nil || tcp.IP.Equal(net.IPv6unspecified):
		return tcp.Port, true, true, nil
	case tcp.IP.Equal(net.IPv4zero):
		return tcp.Port, true, false, nil
	case tcp.IP.IsLoopback():
		return 0, false, false, ErrLoopbackOnly
	default:
		return 0, false, false, ErrSingleAddress
	}
}

// Advertise announces the service until ctx is canceled, then withdraws it
// and returns ctx.Err(). Failures (identity unavailable, port 5353 taken) are
// logged once and retried every check interval.
func Advertise(ctx context.Context, opts Options) error {
	if opts.Port <= 0 || opts.Port > 65535 || opts.ServerID == nil || opts.Name == nil {
		return errors.New("lan discovery: incomplete options")
	}
	lastProblem := ""
	for {
		err := advertise(ctx, opts)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err.Error() != lastProblem {
			lastProblem = err.Error()
			slog.WarnContext(ctx, "LAN discovery unavailable; retrying", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(checkInterval):
		}
	}
}

// advertise runs one responder until ctx ends or it cannot continue.
func advertise(ctx context.Context, opts Options) error {
	id, err := opts.ServerID(ctx)
	if err != nil {
		return fmt.Errorf("server identity: %w", err)
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("server identity is empty")
	}
	name, err := currentName(ctx, opts)
	if err != nil {
		return err
	}
	r, err := openResponder()
	if err != nil {
		return err
	}
	defer r.close()

	if len(r.joinInterfaces()) == 0 {
		slog.InfoContext(ctx, "LAN discovery waiting for a multicast-capable network interface")
	}
	svc := service{instance: name, host: newHostLabel(id), serverID: id, port: opts.Port, ipv4: opts.IPv4, ipv6: opts.IPv6}
	if svc, err = r.claim(ctx, svc); err != nil {
		return err
	}
	r.activate(svc)
	r.announce(svc, r.servedInterfaces())
	slog.InfoContext(ctx, "advertising Silo on the local network",
		"service", ServiceType, "name", svc.instance, "host", svc.hostName(), "port", svc.port)

	// RFC 6762 §8.3: announce at least twice, a second apart.
	repeat := time.After(time.Second)
	// reclaim withdraws the current names and claims next afresh. Callers
	// pass the unsuffixed name, so a later conflict probes "Name", "Name (2)"
	// … again rather than stacking suffixes past the label limit.
	reclaim := func(next service) error {
		r.goodbye(r.deactivate())
		// A conflict reported for the names being withdrawn is settled by
		// this claim.
		select {
		case <-r.conflicts():
		default:
		}
		claimed, err := r.claim(ctx, next)
		if err != nil {
			return err
		}
		svc = claimed
		r.activate(svc)
		r.announce(svc, r.servedInterfaces())
		repeat = time.After(time.Second)
		return nil
	}
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.goodbye(r.deactivate())
			return ctx.Err()
		case <-repeat:
			r.announce(svc, r.servedInterfaces())
		case <-r.conflicts():
			// Another responder answers for one of our names: probe again,
			// renaming if it keeps them (RFC 6762 §9).
			next := svc
			next.instance = name
			if err := reclaim(next); err != nil {
				return err
			}
			slog.InfoContext(ctx, "LAN discovery resolved a name conflict", "name", svc.instance, "host", svc.hostName())
		case <-ticker.C:
			renamed, err := currentName(ctx, opts)
			if err == nil && renamed != name {
				next := svc
				next.instance = renamed
				if err := reclaim(next); err != nil {
					return err
				}
				name = renamed
				slog.InfoContext(ctx, "LAN discovery renamed the advertisement", "name", svc.instance)
				continue
			}
			// A newly joined link may already have our names: probe there
			// before announcing (RFC 6762 §8.1). Interfaces already served
			// keep their service untouched.
			if fresh := r.joinInterfaces(); len(fresh) > 0 {
				result, err := r.probe(ctx, svc)
				if err != nil {
					return err
				}
				if result.instanceTaken || result.hostTaken || result.lostTiebreak {
					next := svc
					next.instance = name
					if err := reclaim(next); err != nil {
						return err
					}
					continue
				}
				r.announce(svc, fresh)
			}
			// Changed addresses are announced so caches drop the old ones
			// (RFC 6762 §8.4).
			r.announceAddressChanges(svc, r.changedAddresses())
		}
	}
}

func currentName(ctx context.Context, opts Options) (string, error) {
	raw, err := opts.Name(ctx)
	if err != nil {
		return "", fmt.Errorf("server name: %w", err)
	}
	name := instanceName(raw)
	if name == "" {
		return "", errors.New("server name is empty")
	}
	return name, nil
}

// instanceName trims name to fit a DNS label with room for a conflict
// suffix, cutting on a rune boundary.
func instanceName(name string) string {
	name = strings.TrimSpace(name)
	if len(name) > maxInstanceNameBytes {
		cut := maxInstanceNameBytes
		for cut > 0 && !utf8.RuneStart(name[cut]) {
			cut--
		}
		name = strings.TrimSpace(name[:cut])
	}
	return name
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
