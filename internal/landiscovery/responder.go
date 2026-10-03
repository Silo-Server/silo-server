package landiscovery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

const mdnsPort = 5353

var (
	group4 = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: mdnsPort}
	group6 = &net.UDPAddr{IP: net.ParseIP("ff02::fb"), Port: mdnsPort}
)

// Probe timing from RFC 6762 §8.1.
const (
	probeCount    = 3
	probeInterval = 250 * time.Millisecond
	// maxRenames bounds the "Name (2)", "Name (3)" … attempts after conflicts.
	maxRenames = 20
)

// responder is a minimal mDNS responder for one service. It owns its
// sockets and goroutines: close releases both. It answers only multicast
// queries from the receiving interface's own link, joins interfaces as they
// appear without withdrawing the service elsewhere, and computes addresses
// when it answers, so address changes need no polling.
type responder struct {
	v4 *ipv4.PacketConn // nil when the host has no IPv4 mDNS socket
	v6 *ipv6.PacketConn // nil when the host has no IPv6 mDNS socket

	sendMu sync.Mutex // SetMulticastInterface and WriteTo go together

	mu         sync.Mutex
	svc        service
	active     bool                    // answering queries for svc
	served     map[int]net.Interface   // interfaces whose groups are joined
	probes     chan *dns.Msg           // responses seen while probing
	lastAnswer map[answerKey]time.Time // per-link rate limit (RFC 6762 §6)

	wg sync.WaitGroup
}

type answerKey struct {
	ifIndex int
	name    string
}

// openResponder binds the mDNS sockets and starts reading. It fails only
// when neither IPv4 nor IPv6 can be bound.
func openResponder() (*responder, error) {
	r := &responder{served: map[int]net.Interface{}, lastAnswer: map[answerKey]time.Time{}}
	lc := net.ListenConfig{Control: shareMDNSPort}
	pc4, err4 := lc.ListenPacket(context.Background(), "udp4", fmt.Sprintf("0.0.0.0:%d", mdnsPort))
	if err4 == nil {
		r.v4 = ipv4.NewPacketConn(pc4)
		// Dst lets a unicast packet to this port be told apart from a
		// multicast one; only the latter is answered.
		err4 = errors.Join(
			r.v4.SetControlMessage(ipv4.FlagInterface|ipv4.FlagDst, true),
			r.v4.SetMulticastTTL(255),
			r.v4.SetMulticastLoopback(true))
		if err4 != nil {
			_ = pc4.Close()
			r.v4 = nil
		}
	}
	pc6, err6 := lc.ListenPacket(context.Background(), "udp6", fmt.Sprintf("[::]:%d", mdnsPort))
	if err6 == nil {
		r.v6 = ipv6.NewPacketConn(pc6)
		err6 = errors.Join(
			r.v6.SetControlMessage(ipv6.FlagInterface|ipv6.FlagDst, true),
			r.v6.SetMulticastHopLimit(255),
			r.v6.SetMulticastLoopback(true))
		if err6 != nil {
			_ = pc6.Close()
			r.v6 = nil
		}
	}
	if r.v4 == nil && r.v6 == nil {
		return nil, fmt.Errorf("lan discovery: cannot bind UDP %d: %w", mdnsPort, errors.Join(err4, err6))
	}
	if r.v4 != nil {
		r.wg.Add(1)
		go r.read4()
	}
	if r.v6 != nil {
		r.wg.Add(1)
		go r.read6()
	}
	return r, nil
}

// close releases the sockets; the read loops end with them.
func (r *responder) close() {
	if r.v4 != nil {
		_ = r.v4.Close()
	}
	if r.v6 != nil {
		_ = r.v6.Close()
	}
	r.wg.Wait()
}

func (r *responder) read4() {
	defer r.wg.Done()
	buf := make([]byte, 9000)
	for {
		n, cm, src, err := r.v4.ReadFrom(buf)
		if err != nil {
			return
		}
		if cm == nil || !cm.Dst.Equal(group4.IP) {
			continue
		}
		if udp, ok := src.(*net.UDPAddr); ok {
			r.handle(buf[:n], cm.IfIndex, udp, false)
		}
	}
}

func (r *responder) read6() {
	defer r.wg.Done()
	buf := make([]byte, 9000)
	for {
		n, cm, src, err := r.v6.ReadFrom(buf)
		if err != nil {
			return
		}
		if cm == nil || !cm.Dst.Equal(group6.IP) {
			continue
		}
		if udp, ok := src.(*net.UDPAddr); ok {
			r.handle(buf[:n], cm.IfIndex, udp, true)
		}
	}
}

// handle answers one packet. Only multicast queries from mDNS port 5353 on
// the receiving interface's link are considered: legacy unicast queriers
// (any other source port) get nothing, so the responder cannot be used to
// reflect traffic, and responses go only to the link's multicast group.
func (r *responder) handle(packet []byte, ifIndex int, src *net.UDPAddr, viaIPv6 bool) {
	defer func() {
		if p := recover(); p != nil {
			slog.Warn("LAN discovery dropped a packet it could not handle", "panic", fmt.Sprint(p))
		}
	}()
	if src.Port != mdnsPort {
		return
	}
	var msg dns.Msg
	if msg.Unpack(packet) != nil {
		return
	}
	r.mu.Lock()
	svc, active, probes := r.svc, r.active, r.probes
	r.mu.Unlock()
	// Most mDNS traffic on a busy link is about other services; settle that
	// before looking up the interface.
	switch {
	case msg.Response && probes == nil:
		return
	case !msg.Response && (!active || msg.Opcode != dns.OpcodeQuery || !svc.asksAbout(&msg)):
		return
	}
	iface, err := net.InterfaceByIndex(ifIndex)
	if err != nil {
		return
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return
	}
	from, ok := netip.AddrFromSlice(src.IP)
	if !ok || !onLink(from, addrs) {
		return
	}
	if msg.Response {
		select {
		case probes <- &msg:
		default:
		}
		return
	}
	resp := svc.answer(&msg, ipsOf(addrs))
	if resp == nil || !r.allow(ifIndex, resp.Answer[0].Header().Name) {
		return
	}
	r.send(*iface, resp, !viaIPv6, viaIPv6)
}

// allow rate-limits answers per link and name to one a second.
func (r *responder) allow(ifIndex int, name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := answerKey{ifIndex, strings.ToLower(name)}
	if time.Since(r.lastAnswer[key]) < time.Second {
		return false
	}
	r.lastAnswer[key] = time.Now()
	return true
}

// send multicasts msg on one interface over the requested families.
func (r *responder) send(iface net.Interface, msg *dns.Msg, overIPv4, overIPv6 bool) {
	b, err := msg.Pack()
	if err != nil {
		return
	}
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	if overIPv4 && r.v4 != nil && r.v4.SetMulticastInterface(&iface) == nil {
		_, _ = r.v4.WriteTo(b, nil, group4)
	}
	if overIPv6 && r.v6 != nil && r.v6.SetMulticastInterface(&iface) == nil {
		_, _ = r.v6.WriteTo(b, nil, group6)
	}
}

// sendAll sends a message built per interface on every served interface.
func (r *responder) sendAll(build func(ips []net.IP) *dns.Msg) {
	for _, iface := range r.servedInterfaces() {
		addrs, _ := iface.Addrs()
		r.send(iface, build(ipsOf(addrs)), true, true)
	}
}

func (r *responder) servedInterfaces() []net.Interface {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]net.Interface, 0, len(r.served))
	for _, iface := range r.served {
		out = append(out, iface)
	}
	return out
}

// eligible reports whether an interface can carry mDNS to clients.
// Point-to-point links (VPN tunnels, overlays) carry no multicast.
func eligible(iface net.Interface) bool {
	f := iface.Flags
	return f&net.FlagUp != 0 && f&net.FlagMulticast != 0 && f&net.FlagLoopback == 0 && f&net.FlagPointToPoint == 0
}

// joinInterfaces joins the mDNS groups on every eligible interface and
// returns the ones that were not joined before, which need an announcement.
// A join on an interface already joined fails harmlessly, so an interface
// that was removed and recreated, even under the same index, is caught too.
// One interface listing per call; no per-interface address dumps.
func (r *responder) joinInterfaces() []net.Interface {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	current := map[int]net.Interface{}
	var fresh []net.Interface
	for _, iface := range ifaces {
		if !eligible(iface) {
			continue
		}
		joined := false
		if r.v4 != nil && r.v4.JoinGroup(&iface, group4) == nil {
			joined = true
		}
		if r.v6 != nil && r.v6.JoinGroup(&iface, group6) == nil {
			joined = true
		}
		r.mu.Lock()
		_, known := r.served[iface.Index]
		r.mu.Unlock()
		if joined || known {
			current[iface.Index] = iface
		}
		if joined {
			fresh = append(fresh, iface)
		}
	}
	r.mu.Lock()
	r.served = current
	r.mu.Unlock()
	return fresh
}

// claim probes for svc's names and returns the service as it may be
// announced: renamed "Name (2)", "Name (3)" … while another host answers for
// the instance name, and with a new host label if the host name is taken.
func (r *responder) claim(ctx context.Context, svc service) (service, error) {
	base := svc.instance
	for rename := 1; rename <= maxRenames; rename++ {
		instanceTaken, hostTaken, err := r.probe(ctx, svc)
		if err != nil {
			return svc, err
		}
		if !instanceTaken && !hostTaken {
			return svc, nil
		}
		if instanceTaken {
			svc.instance = fmt.Sprintf("%s (%d)", base, rename+1)
		}
		if hostTaken {
			svc.host = newHostLabel(svc.serverID)
		}
	}
	slog.WarnContext(ctx, "LAN discovery could not claim a unique name; announcing anyway", "name", svc.instance)
	return svc, nil
}

func (r *responder) probe(ctx context.Context, svc service) (instanceTaken, hostTaken bool, err error) {
	responses := make(chan *dns.Msg, 32)
	r.mu.Lock()
	r.probes = responses
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.probes = nil
		r.mu.Unlock()
	}()
	for i := 0; i < probeCount; i++ {
		r.sendAll(svc.probe)
		timer := time.NewTimer(probeInterval)
	wait:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return false, false, ctx.Err()
			case msg := <-responses:
				inst, host := svc.conflicts(msg)
				instanceTaken = instanceTaken || inst
				hostTaken = hostTaken || host
			case <-timer.C:
				break wait
			}
		}
		if instanceTaken || hostTaken {
			return instanceTaken, hostTaken, nil
		}
	}
	return false, false, nil
}

// activate starts answering for svc.
func (r *responder) activate(svc service) {
	r.mu.Lock()
	r.svc, r.active = svc, true
	r.mu.Unlock()
}

// deactivate stops answering and returns the service that was answered.
func (r *responder) deactivate() service {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active = false
	return r.svc
}

// announce sends the service's records on the given interfaces.
func (r *responder) announce(svc service, ifaces []net.Interface) {
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		r.send(iface, svc.announcement(ipsOf(addrs), serviceTTL), true, true)
	}
}

// goodbye withdraws svc on every served interface (RFC 6762 §10.1). The
// packets go out back to back, so a host with many interfaces finishes in
// milliseconds.
func (r *responder) goodbye(svc service) {
	r.sendAll(func(ips []net.IP) *dns.Msg { return svc.announcement(ips, 0) })
}

// newHostLabel returns "silo-<first 8 of the server ID>-<random>": the ID
// part tells deployments apart, the random part tells apart the API
// processes of one deployment, which each answer for their own addresses.
func newHostLabel(serverID string) string {
	var suffix [3]byte
	_, _ = rand.Read(suffix[:])
	return hostLabel(serverID) + "-" + hex.EncodeToString(suffix[:])
}
