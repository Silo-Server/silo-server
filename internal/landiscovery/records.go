package landiscovery

import (
	"net"
	"net/netip"
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

// conflicts reports whether a response from another responder claims our
// instance or host name with different data (RFC 6762 §8.2, §9).
func (s service) conflicts(response *dns.Msg) (instance, host bool) {
	records := append(append([]dns.RR{}, response.Answer...), response.Extra...)
	records = append(records, response.Ns...)
	for _, rr := range records {
		name := strings.ToLower(rr.Header().Name)
		switch {
		case name == strings.ToLower(s.instanceName()):
			if srv, ok := rr.(*dns.SRV); ok && (!strings.EqualFold(srv.Target, s.hostName()) || int(srv.Port) != s.port) {
				instance = true
			}
		case name == strings.ToLower(s.hostName()):
			if _, ok := rr.(*dns.A); ok {
				host = true
			}
			if _, ok := rr.(*dns.AAAA); ok {
				host = true
			}
		}
	}
	return instance, host
}

// probe is the query that claims the instance and host names before they are
// announced (RFC 6762 §8.1).
func (s service) probe(ips []net.IP) *dns.Msg {
	m := new(dns.Msg)
	m.Question = []dns.Question{
		{Name: s.instanceName(), Qtype: dns.TypeANY, Qclass: dns.ClassINET | cacheFlush},
		{Name: s.hostName(), Qtype: dns.TypeANY, Qclass: dns.ClassINET | cacheFlush},
	}
	m.Ns = append([]dns.RR{s.srv(serviceTTL), s.txt(serviceTTL)}, s.addresses(ips, hostTTL)...)
	return m
}

// onLink reports whether src is an address on one of the receiving
// interface's networks. mDNS answers only its own link (RFC 6762 §11); a
// unicast query from anywhere else must not get a reply.
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
