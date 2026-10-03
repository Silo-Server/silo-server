package landiscovery

import (
	"bytes"
	"net"
	"net/netip"
	"sort"
	"strings"

	"github.com/miekg/dns"
)

// Record lifetimes from RFC 6762 §10: host address records are short-lived,
// service records long-lived.
const (
	hostTTL    = 120
	serviceTTL = 4500
)

// cacheFlush marks a record set this responder alone owns (RFC 6762 §10.2).
const cacheFlush = 1 << 15

const (
	serviceDomain     = ServiceType + ".local."
	servicesEnumerate = "_services._dns-sd._udp.local."
)

// service is what one responder advertises.
type service struct {
	instance string // instance label, unescaped ("Silo", "Silo (2)")
	host     string // host label without ".local"
	serverID string
	port     int
	ipv4     bool // the API listener accepts IPv4
	ipv6     bool // the API listener accepts IPv6
}

func (s service) instanceName() string { return escapeLabel(s.instance) + "." + serviceDomain }
func (s service) hostName() string     { return s.host + ".local." }

// escapeLabel writes label in DNS presentation format so that dots, spaces
// and UTF-8 bytes stay inside one label. It escapes exactly as miekg/dns does
// when it unpacks a name, so names read off the wire compare equal.
func escapeLabel(label string) string {
	var b strings.Builder
	for i := 0; i < len(label); i++ {
		c := label[i]
		switch c {
		case '.', ' ', '\'', '@', ';', '(', ')', '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			if c < ' ' || c > '~' {
				b.WriteByte('\\')
				b.WriteByte('0' + c/100)
				b.WriteByte('0' + c/10%10)
				b.WriteByte('0' + c%10)
			} else {
				b.WriteByte(c)
			}
		}
	}
	return b.String()
}

func header(name string, rrtype uint16, ttl uint32, unique bool) dns.RR_Header {
	class := uint16(dns.ClassINET)
	if unique {
		class |= cacheFlush
	}
	return dns.RR_Header{Name: name, Rrtype: rrtype, Class: class, Ttl: ttl}
}

func (s service) ptr(ttl uint32) dns.RR {
	return &dns.PTR{Hdr: header(serviceDomain, dns.TypePTR, ttl, false), Ptr: s.instanceName()}
}

func (s service) enumeration(ttl uint32) dns.RR {
	return &dns.PTR{Hdr: header(servicesEnumerate, dns.TypePTR, ttl, false), Ptr: serviceDomain}
}

func (s service) srv(ttl uint32) dns.RR {
	return &dns.SRV{Hdr: header(s.instanceName(), dns.TypeSRV, ttl, true), Port: uint16(s.port), Target: s.hostName()}
}

func (s service) txt(ttl uint32) dns.RR {
	return &dns.TXT{Hdr: header(s.instanceName(), dns.TypeTXT, ttl, true),
		Txt: []string{TXTKeyVersion + "=" + TXTVersion, TXTKeyServerID + "=" + s.serverID}}
}

// addresses returns the host's A and AAAA records for one interface, limited
// to the families the API listener accepts. Link-local IPv6 is left out: a
// URL cannot carry the zone it needs, so clients must not pick it.
func (s service) addresses(ips []net.IP, ttl uint32) []dns.RR {
	var out []dns.RR
	for _, ip := range ips {
		switch {
		case ip.To4() != nil:
			if s.ipv4 && !ip.IsLoopback() {
				out = append(out, &dns.A{Hdr: header(s.hostName(), dns.TypeA, ttl, true), A: ip.To4()})
			}
		case s.ipv6 && !ip.IsLoopback() && !ip.IsLinkLocalUnicast():
			out = append(out, &dns.AAAA{Hdr: header(s.hostName(), dns.TypeAAAA, ttl, true), AAAA: ip})
		}
	}
	return out
}

// announcement is every record of the service for one interface: sent on
// start and on a newly joined interface, and with ttl 0 as a goodbye.
func (s service) announcement(ips []net.IP, ttl uint32) *dns.Msg {
	m := responseMsg()
	m.Answer = append([]dns.RR{s.ptr(ttl), s.enumeration(ttl), s.srv(ttl), s.txt(ttl)}, s.addresses(ips, min(ttl, hostTTL))...)
	return m
}

func responseMsg() *dns.Msg {
	m := new(dns.Msg)
	m.Response = true
	m.Authoritative = true
	return m
}

// answer builds the multicast response to a query received on an interface
// with the given addresses, or nil when none of its questions are ours.
func (s service) answer(query *dns.Msg, ips []net.IP) *dns.Msg {
	m := responseMsg()
	var extra []dns.RR
	addExtraService := func() {
		extra = append(extra, s.srv(serviceTTL), s.txt(serviceTTL))
		extra = append(extra, s.addresses(ips, hostTTL)...)
	}
	for _, q := range query.Question {
		name := strings.ToLower(q.Name)
		switch {
		case name == serviceDomain && (q.Qtype == dns.TypePTR || q.Qtype == dns.TypeANY):
			if knownAnswer(query, s.instanceName()) {
				continue
			}
			m.Answer = append(m.Answer, s.ptr(serviceTTL))
			addExtraService()
		case name == servicesEnumerate && (q.Qtype == dns.TypePTR || q.Qtype == dns.TypeANY):
			m.Answer = append(m.Answer, s.enumeration(serviceTTL))
		case name == strings.ToLower(s.instanceName()):
			if q.Qtype == dns.TypeSRV || q.Qtype == dns.TypeANY {
				m.Answer = append(m.Answer, s.srv(serviceTTL))
			}
			if q.Qtype == dns.TypeTXT || q.Qtype == dns.TypeANY {
				m.Answer = append(m.Answer, s.txt(serviceTTL))
			}
			extra = append(extra, s.addresses(ips, hostTTL)...)
		case name == strings.ToLower(s.hostName()):
			for _, rr := range s.addresses(ips, hostTTL) {
				t := rr.Header().Rrtype
				if q.Qtype == dns.TypeANY || q.Qtype == t {
					m.Answer = append(m.Answer, rr)
				}
			}
		}
	}
	if len(m.Answer) == 0 {
		return nil
	}
	m.Extra = dedupe(m.Answer, extra)
	return m
}

// asksAbout reports whether a query has a question this service answers.
func (s service) asksAbout(query *dns.Msg) bool {
	for _, q := range query.Question {
		switch strings.ToLower(q.Name) {
		case serviceDomain, servicesEnumerate, strings.ToLower(s.instanceName()), strings.ToLower(s.hostName()):
			return true
		}
	}
	return false
}

// knownAnswer reports whether the querier already holds our PTR with at
// least half its lifetime left (RFC 6762 §7.1), so it need not be repeated.
func knownAnswer(query *dns.Msg, target string) bool {
	for _, rr := range query.Answer {
		if p, ok := rr.(*dns.PTR); ok && strings.EqualFold(p.Ptr, target) && p.Hdr.Ttl >= serviceTTL/2 {
			return true
		}
	}
	return false
}

// dedupe drops extra records that already appear in the answer.
func dedupe(answer, extra []dns.RR) []dns.RR {
	seen := map[string]bool{}
	for _, rr := range answer {
		seen[rr.String()] = true
	}
	var out []dns.RR
	for _, rr := range extra {
		if !seen[rr.String()] {
			seen[rr.String()] = true
			out = append(out, rr)
		}
	}
	return out
}

// mentions reports whether a message carries a record for one of the
// service's own names; nothing else can conflict with it.
func (s service) mentions(msg *dns.Msg) bool {
	instance, host := strings.ToLower(s.instanceName()), strings.ToLower(s.hostName())
	for _, section := range [][]dns.RR{msg.Answer, msg.Ns, msg.Extra} {
		for _, rr := range section {
			if name := strings.ToLower(rr.Header().Name); name == instance || name == host {
				return true
			}
		}
	}
	return false
}

// conflicts reports whether records from another responder claim our
// instance or host name with different data (RFC 6762 §8.2, §9). An SRV for
// our instance name that points elsewhere takes the instance name; an
// address record for our host name that is not one of this machine's
// addresses takes the host name. Our own records, looped back or seen on a
// second interface, match and are not conflicts.
func (s service) conflicts(msg *dns.Msg, isLocal func(net.IP) bool) (instance, host bool) {
	instanceName, hostName := strings.ToLower(s.instanceName()), strings.ToLower(s.hostName())
	for _, section := range [][]dns.RR{msg.Answer, msg.Ns, msg.Extra} {
		for _, rr := range section {
			if rr.Header().Ttl == 0 {
				continue // a goodbye withdraws a record; it claims nothing
			}
			switch strings.ToLower(rr.Header().Name) {
			case instanceName:
				if srv, ok := rr.(*dns.SRV); ok && (!strings.EqualFold(srv.Target, s.hostName()) || int(srv.Port) != s.port) {
					instance = true
				}
			case hostName:
				switch r := rr.(type) {
				case *dns.A:
					host = host || !isLocal(r.A)
				case *dns.AAAA:
					host = host || !isLocal(r.AAAA)
				}
			}
		}
	}
	return instance, host
}

// losesTiebreak reports whether a probe query from another host, claiming
// our instance name at the same time as we do, wins the simultaneous-probe
// tiebreak (RFC 6762 §8.2): the side whose proposed records sort
// lexicographically later keeps the name. Identical records, such as our own
// probe looped back, are no contest.
func (s service) losesTiebreak(query *dns.Msg) bool {
	name := strings.ToLower(s.instanceName())
	var theirs []dns.RR
	for _, rr := range query.Ns {
		if strings.ToLower(rr.Header().Name) == name {
			theirs = append(theirs, rr)
		}
	}
	if len(theirs) == 0 {
		return false
	}
	return compareRecordSets(theirs, []dns.RR{s.srv(serviceTTL), s.txt(serviceTTL)}) > 0
}

// compareRecordSets orders two proposed record sets as RFC 6762 §8.2 does:
// each sorted by class, type and raw rdata, compared pairwise, and a set
// that is a prefix of the other sorts first.
func compareRecordSets(a, b []dns.RR) int {
	a, b = sortedRecords(a), sortedRecords(b)
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := compareRecords(a[i], b[i]); c != 0 {
			return c
		}
	}
	return len(a) - len(b)
}

func sortedRecords(rrs []dns.RR) []dns.RR {
	out := append([]dns.RR(nil), rrs...)
	sort.Slice(out, func(i, j int) bool { return compareRecords(out[i], out[j]) < 0 })
	return out
}

func compareRecords(a, b dns.RR) int {
	ca, cb := a.Header().Class&^cacheFlush, b.Header().Class&^cacheFlush
	switch {
	case ca != cb:
		return int(ca) - int(cb)
	case a.Header().Rrtype != b.Header().Rrtype:
		return int(a.Header().Rrtype) - int(b.Header().Rrtype)
	}
	return bytes.Compare(rdata(a), rdata(b))
}

// rdata returns a record's uncompressed wire-format data.
func rdata(rr dns.RR) []byte {
	buf := make([]byte, 1024)
	end, err := dns.PackRR(rr, buf, 0, nil, false)
	if err != nil {
		return nil
	}
	nameEnd, err := dns.PackDomainName(rr.Header().Name, make([]byte, 256), 0, nil, false)
	if err != nil {
		return nil
	}
	return buf[nameEnd+10 : end] // type, class, TTL and rdlength take 10 bytes
}

// probe is the query that claims the instance and host names before they are
// announced (RFC 6762 §8.1). It asks for multicast replies, not unicast ones:
// the responder shares port 5353 with the operating system's daemon, and a
// unicast defense would reach only the first socket bound (§15.1).
func (s service) probe(ips []net.IP) *dns.Msg {
	m := new(dns.Msg)
	m.Question = []dns.Question{
		{Name: s.instanceName(), Qtype: dns.TypeANY, Qclass: dns.ClassINET},
		{Name: s.hostName(), Qtype: dns.TypeANY, Qclass: dns.ClassINET},
	}
	m.Ns = append([]dns.RR{s.srv(serviceTTL), s.txt(serviceTTL)}, s.addresses(ips, hostTTL)...)
	return m
}

// onLink reports whether src is an address on one of the receiving
// interface's networks. A unicast packet carries no proof it came from the
// link (RFC 6762 §11), so a unicast response is trusted only from there.
func onLink(src netip.Addr, ifaceAddrs []net.Addr) bool {
	src = src.Unmap()
	if src.Is6() && src.IsLinkLocalUnicast() {
		return true
	}
	for _, a := range ifaceAddrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		prefix, ok := netip.AddrFromSlice(ipnet.IP)
		if !ok {
			continue
		}
		ones, _ := ipnet.Mask.Size()
		if netip.PrefixFrom(prefix.Unmap(), ones).Contains(src) {
			return true
		}
	}
	return false
}

func ipsOf(addrs []net.Addr) []net.IP {
	var out []net.IP
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			out = append(out, ipnet.IP)
		}
	}
	return out
}
