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
	"sort"
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
// when it answers.
type responder struct {
	v4 *ipv4.PacketConn // nil when the host has no IPv4 mDNS socket
	v6 *ipv6.PacketConn // nil when the host has no IPv6 mDNS socket

	sendMu sync.Mutex // SetMulticastInterface and WriteTo go together

	mu     sync.Mutex
	svc    service
	active bool                  // answering queries for svc
	served map[int]net.Interface // eligible interfaces being served
	// members holds every interface index whose groups a socket joined. It
	// outlives `served`: an interface that goes down and comes back keeps
	// its membership in the kernel, so joining again fails.
	members  map[int]bool
	probing  service       // the names a running probe claims
	probes   chan *dns.Msg // messages about them while probing
	conflict chan struct{} // another responder took an active name
	// lastSent rate-limits each record per link and family (RFC 6762 §6).
	lastSent map[sentKey]time.Time
	// addrs and allAddrs track interface addresses, so a change is
	// announced (RFC 6762 §8.4) without listing every interface each tick.
	addrs    map[int][]net.IP
	allAddrs string

	wg sync.WaitGroup
}

type sentKey struct {
	ifIndex int
	ipv6    bool
	name    string
	rrtype  uint16
}

// openResponder binds the mDNS sockets and starts reading. It fails only
// when neither IPv4 nor IPv6 can be bound.
func openResponder() (*responder, error) {
	r := &responder{
		served:   map[int]net.Interface{},
		members:  map[int]bool{},
		conflict: make(chan struct{}, 1),
		lastSent: map[sentKey]time.Time{},
		addrs:    map[int][]net.IP{},
	}
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
		if udp, ok := src.(*net.UDPAddr); ok && cm != nil {
			r.handle(buf[:n], cm.IfIndex, udp, false, cm.Dst.Equal(group4.IP))
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
		if udp, ok := src.(*net.UDPAddr); ok && cm != nil {
			r.handle(buf[:n], cm.IfIndex, udp, true, cm.Dst.Equal(group6.IP))
		}
	}
}

// handle processes one packet from mDNS port 5353; anything else is
// dropped. Queries are answered only when they arrive by multicast, which is
// link-local whatever its source subnet (RFC 6762 §11), and answers go only
// to the link's multicast group, never to the sender, so the responder cannot
// be used to reflect traffic. A unicast response is accepted only while
// probing and only from an address on the receiving interface's networks, in
// case a responder defends a name by unicast anyway (§5.4).
func (r *responder) handle(packet []byte, ifIndex int, src *net.UDPAddr, viaIPv6, multicast bool) {
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
	svc, active, probing, probes := r.svc, r.active, r.probing, r.probes
	r.mu.Unlock()
	// Most mDNS traffic on a busy link is about other services; settle that
	// before looking up the interface.
	probeRelevant := probes != nil && probing.mentions(&msg)
	var relevant bool
	if msg.Response {
		relevant = probeRelevant || (multicast && active && svc.mentions(&msg))
	} else {
		relevant = multicast && msg.Opcode == dns.OpcodeQuery && (probeRelevant || (active && svc.asksAbout(&msg)))
	}
	if !relevant {
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
	if !multicast {
		from, ok := netip.AddrFromSlice(src.IP)
		if !ok || !onLink(from, addrs) {
			return
		}
	}
	if probeRelevant {
		// Responses defend a name; queries with authority records are
		// another host probing at the same time.
		select {
		case probes <- &msg:
		default:
		}
	}
	if msg.Response {
		// After the probe, a response that claims an active name with other
		// data means another responder has it: start over (RFC 6762 §9).
		if inst, host := svc.conflicts(&msg, isLocalAddress); active && (inst || host) {
			select {
			case r.conflict <- struct{}{}:
			default:
			}
		}
		return
	}
	if !active || !svc.asksAbout(&msg) {
		return
	}
	resp := svc.answer(&msg, ipsOf(addrs))
	if resp == nil {
		return
	}
	if resp.Answer = r.allow(ifIndex, viaIPv6, resp.Answer); len(resp.Answer) == 0 {
		return
	}
	r.send(*iface, resp, !viaIPv6, viaIPv6)
}

// conflicts signals that another responder claimed an active name.
func (r *responder) conflicts() <-chan struct{} { return r.conflict }

// allow drops answer records already multicast on this link and family in
// the last second (RFC 6762 §6). Each record set is limited on its own, so
// an SRV answer does not hold back a TXT one, nor an IPv4 answer an IPv6 one.
func (r *responder) allow(ifIndex int, viaIPv6 bool, answers []dns.RR) []dns.RR {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if len(r.lastSent) > 512 {
		for k, t := range r.lastSent {
			if now.Sub(t) >= time.Second {
				delete(r.lastSent, k)
			}
		}
	}
	// Decide per record set before recording anything, so every address of
	// a set goes out together.
	var out []dns.RR
	sent := map[sentKey]bool{}
	for _, rr := range answers {
		key := sentKey{ifIndex, viaIPv6, strings.ToLower(rr.Header().Name), rr.Header().Rrtype}
		if now.Sub(r.lastSent[key]) < time.Second {
			continue
		}
		sent[key] = true
		out = append(out, rr)
	}
	for key := range sent {
		r.lastSent[key] = now
	}
	return out
}

// isLocalAddress reports whether ip belongs to this machine, so our own
// address records, looped back or heard on a second interface, are not
// mistaken for another host's. It lists addresses only when a response
// carries an address record for our host name.
func isLocalAddress(ip net.IP) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return true // cannot tell; do not start a rename on a guess
	}
	for _, ip2 := range ipsOf(addrs) {
		if ip2.Equal(ip) {
			return true
		}
	}
	return false
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
// returns the ones newly served, which need a probe and an announcement. A
// join on an interface already joined fails harmlessly, so an interface that
// was removed and recreated, even under the same index, is caught too; one
// that only went down and up keeps its membership and is served again from
// `members`. One interface listing per call; no per-interface address dumps.
func (r *responder) joinInterfaces() []net.Interface {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	fresh := r.serve(ifaces)
	// Record the addresses a newly served interface is announced with, so
	// a later change is noticed.
	for _, iface := range fresh {
		r.addressChange(iface)
	}
	return fresh
}

// serve joins and records the eligible interfaces among ifaces and returns
// the newly served ones.
func (r *responder) serve(ifaces []net.Interface) []net.Interface {
	current := map[int]net.Interface{}
	listed := map[int]bool{}
	var fresh []net.Interface
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, iface := range ifaces {
		listed[iface.Index] = true
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
		if joined {
			r.members[iface.Index] = true
		}
		_, served := r.served[iface.Index]
		if !r.members[iface.Index] {
			continue
		}
		current[iface.Index] = iface
		if joined || !served {
			fresh = append(fresh, iface)
		}
	}
	// A destroyed interface takes its memberships with it.
	for index := range r.members {
		if !listed[index] {
			delete(r.members, index)
		}
	}
	r.served = current
	for index := range r.addrs {
		if _, ok := current[index]; !ok {
			delete(r.addrs, index)
		}
	}
	return fresh
}

// addressChange is a served interface whose addresses changed, with the
// addresses it no longer has.
type addressChange struct {
	iface   net.Interface
	removed []net.IP
}

// changedAddresses returns the served interfaces whose addresses changed
// since they were last announced. One address listing tells whether
// anything changed; only then is each interface looked at.
func (r *responder) changedAddresses() []addressChange {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	all := addressKey(ipsOf(addrs))
	r.mu.Lock()
	unchanged := all == r.allAddrs
	r.allAddrs = all
	r.mu.Unlock()
	if unchanged {
		return nil
	}
	var changes []addressChange
	for _, iface := range r.servedInterfaces() {
		if change, ok := r.addressChange(iface); ok {
			changes = append(changes, change)
		}
	}
	return changes
}

// addressChange records an interface's current addresses and reports how
// they differ from the last record; ok is false for no change or a first
// record.
func (r *responder) addressChange(iface net.Interface) (addressChange, bool) {
	addrs, err := iface.Addrs()
	if err != nil {
		return addressChange{}, false
	}
	now := ipsOf(addrs)
	r.mu.Lock()
	old, known := r.addrs[iface.Index]
	r.addrs[iface.Index] = now
	r.mu.Unlock()
	if !known || addressKey(old) == addressKey(now) {
		return addressChange{}, false
	}
	var removed []net.IP
	for _, ip := range old {
		if !containsIP(now, ip) {
			removed = append(removed, ip)
		}
	}
	return addressChange{iface: iface, removed: removed}, true
}

// announceAddressChanges announces the current records on each changed
// interface and withdraws removed addresses with goodbyes. The cache-flush
// bit replaces a changed address set, but not one whose family vanished.
func (r *responder) announceAddressChanges(svc service, changes []addressChange) {
	for _, c := range changes {
		r.announce(svc, []net.Interface{c.iface})
		if gone := svc.addresses(c.removed, 0); len(gone) > 0 {
			m := responseMsg()
			m.Answer = gone
			r.send(c.iface, m, true, true)
		}
	}
}

func containsIP(ips []net.IP, ip net.IP) bool {
	for _, x := range ips {
		if x.Equal(ip) {
			return true
		}
	}
	return false
}

func addressKey(ips []net.IP) string {
	parts := make([]string, 0, len(ips))
	for _, ip := range ips {
		parts = append(parts, ip.String())
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// claim probes for svc's names and returns the service as it may be
// announced: renamed "Name (2)", "Name (3)" … while another host answers for
// the instance name, and with a new host label if the host name is taken.
// Losing a simultaneous-probe tiebreak waits a second and probes again
// (RFC 6762 §8.2); the winner's announcement then shows as a conflict.
// Only a name that probed clean is returned; after maxRenames attempts the
// claim fails and Advertise retries later.
func (r *responder) claim(ctx context.Context, svc service) (service, error) {
	base, suffix := svc.instance, 1
	for try := 0; try < maxRenames; try++ {
		result, err := r.probe(ctx, svc)
		if err != nil {
			return svc, err
		}
		switch {
		case result.lostTiebreak:
			if err := sleep(ctx, time.Second); err != nil {
				return svc, err
			}
			continue
		case !result.instanceTaken && !result.hostTaken:
			return svc, nil
		}
		if result.instanceTaken {
			suffix++
			svc.instance = fmt.Sprintf("%s (%d)", base, suffix)
		}
		if result.hostTaken {
			svc.host = newHostLabel(svc.serverID)
		}
	}
	return svc, fmt.Errorf("lan discovery: no free instance name after %d attempts", maxRenames)
}

type probeResult struct {
	instanceTaken, hostTaken, lostTiebreak bool
}

func (r *responder) probe(ctx context.Context, svc service) (probeResult, error) {
	responses := make(chan *dns.Msg, 32)
	r.mu.Lock()
	r.probing, r.probes = svc, responses
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.probes = nil
		r.mu.Unlock()
	}()
	var result probeResult
	for i := 0; i < probeCount; i++ {
		r.sendAll(svc.probe)
		timer := time.NewTimer(probeInterval)
	wait:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return probeResult{}, ctx.Err()
			case msg := <-responses:
				inst, host := svc.conflicts(msg, isLocalAddress)
				if msg.Response {
					result.instanceTaken = result.instanceTaken || inst
				} else {
					// A competing probe's SRV is a contest, not a claim.
					result.lostTiebreak = result.lostTiebreak || svc.losesTiebreak(msg)
				}
				result.hostTaken = result.hostTaken || host
			case <-timer.C:
				break wait
			}
		}
		if result.instanceTaken || result.hostTaken || result.lostTiebreak {
			return result, nil
		}
	}
	return result, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
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
