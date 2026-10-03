package landiscovery

import (
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

var testService = service{
	instance: "Living Room (Den)", host: "silo-6f1c2a9b-abcdef", serverID: "6f1c2a9b-0d4e",
	port: 8080, ipv4: true, ipv6: true,
}

var testIPs = []net.IP{net.ParseIP("192.168.1.20"), net.ParseIP("fe80::1"), net.ParseIP("2001:db8::20")}

// roundTrip packs and unpacks a message, as it travels on the wire.
func roundTrip(t *testing.T, m *dns.Msg) *dns.Msg {
	t.Helper()
	b, err := m.Pack()
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	var out dns.Msg
	if err := out.Unpack(b); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	return &out
}

func query(name string, qtype uint16) *dns.Msg {
	m := new(dns.Msg)
	m.Question = []dns.Question{{Name: name, Qtype: qtype, Qclass: dns.ClassINET}}
	return m
}

func TestBrowseAnswer(t *testing.T) {
	resp := roundTrip(t, testService.answer(roundTrip(t, query(serviceDomain, dns.TypePTR)), testIPs))
	ptr, ok := resp.Answer[0].(*dns.PTR)
	if !ok || len(resp.Answer) != 1 || !strings.EqualFold(ptr.Ptr, testService.instanceName()) {
		t.Fatalf("answer = %v", resp.Answer)
	}
	var txt string
	var srvPort uint16
	var a, aaaa []string
	for _, rr := range resp.Extra {
		switch r := rr.(type) {
		case *dns.TXT:
			txt = strings.Join(r.Txt, " ")
		case *dns.SRV:
			srvPort = r.Port
		case *dns.A:
			a = append(a, r.A.String())
		case *dns.AAAA:
			aaaa = append(aaaa, r.AAAA.String())
		}
	}
	if txt != "v=1 id=6f1c2a9b-0d4e" || srvPort != 8080 {
		t.Fatalf("TXT %q, SRV port %d", txt, srvPort)
	}
	// Link-local IPv6 needs a zone a URL cannot carry, so it is never offered.
	if strings.Join(a, ",") != "192.168.1.20" || strings.Join(aaaa, ",") != "2001:db8::20" {
		t.Fatalf("A %v, AAAA %v", a, aaaa)
	}
}

func TestAnswersOnlyTheListenersFamilies(t *testing.T) {
	ipv4Only := testService
	ipv4Only.ipv6 = false
	resp := ipv4Only.answer(query(testService.hostName(), dns.TypeANY), testIPs)
	for _, rr := range resp.Answer {
		if _, ok := rr.(*dns.AAAA); ok {
			t.Fatalf("IPv4-only listener advertised %v", rr)
		}
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("answer = %v", resp.Answer)
	}
}

func TestInstanceNamesSurviveTheWire(t *testing.T) {
	// Spaces, parentheses, dots and UTF-8 must stay one label and compare
	// equal after a round trip, or the responder would ignore questions about
	// its own name.
	for _, name := range []string{"Living Room (Den)", "Silo v1.0", "Wohnzimmer – Ö", `quote"back\slash`} {
		svc := testService
		svc.instance = name
		q := roundTrip(t, query(svc.instanceName(), dns.TypeSRV))
		if !svc.asksAbout(q) || svc.answer(q, testIPs) == nil {
			t.Errorf("%q: question about own name not recognized (wire name %q)", name, q.Question[0].Name)
		}
	}
}

func TestIgnoresOtherQuestions(t *testing.T) {
	q := query("_googlecast._tcp.local.", dns.TypePTR)
	if testService.asksAbout(q) || testService.answer(q, testIPs) != nil {
		t.Fatal("answered a question about another service")
	}
}

func TestKnownAnswerSuppression(t *testing.T) {
	q := query(serviceDomain, dns.TypePTR)
	q.Answer = []dns.RR{testService.ptr(serviceTTL)}
	if testService.answer(q, testIPs) != nil {
		t.Fatal("repeated a PTR the querier already holds")
	}
	q.Answer = []dns.RR{testService.ptr(10)}
	if testService.answer(q, testIPs) == nil {
		t.Fatal("a nearly expired known answer must be refreshed")
	}
}

func TestGoodbyeHasZeroTTL(t *testing.T) {
	for _, rr := range testService.announcement(testIPs, 0).Answer {
		if rr.Header().Ttl != 0 {
			t.Fatalf("goodbye record %v keeps a TTL", rr)
		}
	}
}

func TestConflicts(t *testing.T) {
	local := func(ip net.IP) bool { return ip.Equal(testIPs[0]) }
	other := testService
	other.host = "someone-else"
	resp := responseMsg()
	resp.Answer = []dns.RR{other.srv(serviceTTL)}
	if inst, _ := testService.conflicts(roundTrip(t, resp), local); !inst {
		t.Fatal("another host's SRV for our instance name is a conflict")
	}
	// Our own announcement, looped back or heard on a second interface.
	own := roundTrip(t, testService.announcement(testIPs[:1], serviceTTL))
	if inst, host := testService.conflicts(own, local); inst || host {
		t.Fatal("our own records are not a conflict")
	}
	// Our goodbye for an address this machine no longer has, looped back.
	gone := responseMsg()
	gone.Answer = testService.addresses([]net.IP{net.ParseIP("192.168.1.99")}, 0)
	if _, host := testService.conflicts(roundTrip(t, gone), local); host {
		t.Fatal("a goodbye is not a claim")
	}
	hostClaim := responseMsg()
	hostClaim.Answer = testService.addresses([]net.IP{net.ParseIP("192.168.1.99")}, hostTTL)
	if _, host := testService.conflicts(roundTrip(t, hostClaim), local); !host {
		t.Fatal("another machine's address for our host name is a conflict")
	}
}

func TestMentions(t *testing.T) {
	if !testService.mentions(roundTrip(t, testService.announcement(testIPs, serviceTTL))) {
		t.Fatal("announcement mentions the service")
	}
	resp := responseMsg()
	resp.Answer = []dns.RR{&dns.PTR{Hdr: header("_googlecast._tcp.local.", dns.TypePTR, 120, false), Ptr: "tv._googlecast._tcp.local."}}
	if testService.mentions(resp) {
		t.Fatal("another service's records do not mention ours")
	}
}

func TestSimultaneousProbeTiebreak(t *testing.T) {
	// Two replicas of one deployment probe the same instance name with
	// different host labels; exactly one must yield.
	a, b := testService, testService
	a.host, b.host = "silo-6f1c2a9b-aaaaaa", "silo-6f1c2a9b-bbbbbb"
	aLoses := a.losesTiebreak(roundTrip(t, b.probe(testIPs)))
	bLoses := b.losesTiebreak(roundTrip(t, a.probe(testIPs)))
	if aLoses == bLoses {
		t.Fatalf("tiebreak must pick one winner: a loses %v, b loses %v", aLoses, bLoses)
	}
	if !aLoses {
		t.Fatal("the lexicographically later SRV target wins")
	}
	if a.losesTiebreak(roundTrip(t, a.probe(testIPs))) {
		t.Fatal("our own probe, looped back, is no contest")
	}
}

func TestRateLimitIsPerRecordAndFamily(t *testing.T) {
	r := &responder{lastSent: map[sentKey]time.Time{}}
	srv, txt := testService.srv(serviceTTL), testService.txt(serviceTTL)
	if got := r.allow(1, false, []dns.RR{srv}); len(got) != 1 {
		t.Fatal("first SRV answer is sent")
	}
	if got := r.allow(1, false, []dns.RR{srv, txt}); len(got) != 1 || got[0] != txt {
		t.Fatalf("a recent SRV holds back only itself, got %v", got)
	}
	if got := r.allow(1, true, []dns.RR{srv}); len(got) != 1 {
		t.Fatal("an IPv4 answer does not hold back an IPv6 one")
	}
	if got := r.allow(2, false, []dns.RR{srv}); len(got) != 1 {
		t.Fatal("another interface is limited separately")
	}
	// Every address of an interface is one record set and goes out whole.
	two := testService.addresses([]net.IP{net.ParseIP("192.168.1.20"), net.ParseIP("10.0.0.20")}, hostTTL)
	if got := r.allow(3, false, two); len(got) != 2 {
		t.Fatalf("an A record set was cut to %v", got)
	}
}

func TestOnLink(t *testing.T) {
	_, lan, _ := net.ParseCIDR("192.168.1.20/24")
	_, v6, _ := net.ParseCIDR("2001:db8::20/64")
	addrs := []net.Addr{&net.IPNet{IP: net.ParseIP("192.168.1.20"), Mask: lan.Mask}, &net.IPNet{IP: net.ParseIP("2001:db8::20"), Mask: v6.Mask}}
	for addr, want := range map[string]bool{
		"192.168.1.77":   true,
		"192.168.2.77":   false,
		"203.0.113.9":    false,
		"fe80::1234":     true,
		"2001:db8::99":   true,
		"2001:db8:1::99": false,
	} {
		if got := onLink(netip.MustParseAddr(addr), addrs); got != want {
			t.Errorf("onLink(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestInterfaceKeepsItsMembershipAcrossAFlap(t *testing.T) {
	// No sockets: every join fails, as it does in the kernel for an
	// interface that went down and up with its membership kept.
	r := &responder{served: map[int]net.Interface{}, members: map[int]bool{5: true}, addrs: map[int][]net.IP{}}
	up := net.Interface{Index: 5, Name: "eth0", Flags: net.FlagUp | net.FlagMulticast}
	down := up
	down.Flags = net.FlagMulticast

	if fresh := r.serve([]net.Interface{down}); len(fresh) != 0 || len(r.served) != 0 {
		t.Fatal("a down interface is not served")
	}
	if fresh := r.serve([]net.Interface{up}); len(fresh) != 1 {
		t.Fatal("an interface back up with its membership kept is served again, as new")
	}
	if fresh := r.serve([]net.Interface{up}); len(fresh) != 0 {
		t.Fatal("an interface already served is not new")
	}
	r.serve(nil)
	if r.members[5] {
		t.Fatal("a destroyed interface drops its membership")
	}
}
