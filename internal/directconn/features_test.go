package directconn

import (
	"errors"
	"net/netip"
	"slices"
	"strconv"
	"testing"

	"github.com/darthShadow/shunpike/engine"
)

func TestFeatureStates(t *testing.T) {
	const request = 7 << 20
	base := engine.CapabilityReport{Platform: "linux", GSO: engine.Supported, GRO: engine.Unsupported, DF: engine.SupportUnknown, ReceiveBuffer: engine.BufferReport{Requested: request, Granted: 8 << 20, ForceErrno: "EPERM"}, SendBuffer: engine.BufferReport{Requested: request, Granted: 14 << 20, Forced: true}}
	testLocalFeatureStates(t, request, base)
	testNetworkFeatureStates(t, base)
	testUnsupportedPlatformFeatureStates(t)
}

func testLocalFeatureStates(t *testing.T, request int, base engine.CapabilityReport) {
	t.Helper()
	for _, tc := range []struct {
		name                  string
		held                  bool
		caps                  engine.CapabilityReport
		target, state, reason string
		capErr                error
	}{
		{"receive capped", false, base, "socket_receive_buffer", "capped", "no_cap_net_admin", nil},
		{"privilege dropped", true, base, "socket_receive_buffer", "capped", "net_admin_dropped", nil},
		{"send doubled", false, base, "socket_send_buffer", "on", "", nil},
		{"GSO", false, base, "gso", "on", "", nil}, {"GRO", false, base, "gro", "off", "kernel_unsupported", nil}, {"DF unknown", false, base, "pmtu_fallback", "unknown", "not_measured", nil},
		{"admin forced", false, base, "cap_net_admin", "on", "", nil},
		{"capability probe failed", false, engine.CapabilityReport{Platform: "linux"}, "gso", "unknown", "not_measured", errors.New("capability probe failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := featureStates(settings{enabled: true, portMapping: true}, tc.held, tc.caps, tc.capErr, engine.NATReport{}, nil, engine.PortMappingReport{}, nil)
			f := feature(t, rows, tc.target)
			if f.State != tc.state || f.Reason != tc.reason {
				t.Fatalf("feature=%+v, want %s/%s", f, tc.state, tc.reason)
			}
			if tc.capErr != nil {
				for _, row := range rows {
					if row.Kind == "local" && row.Feature != "direct" && (row.State != "unknown" || row.Reason != "not_measured") {
						t.Fatalf("failed capability probe row = %+v", row)
					}
				}
				if direct := feature(t, rows, "direct"); direct.State != "on" || direct.Reason != "" {
					t.Fatalf("direct toggle after probe failure = %+v", direct)
				}
			}
		})
	}
	for _, held := range []bool{false, true} {
		caps := base
		caps.SendBuffer.Forced = false
		caps.SendBuffer.ForceErrno = "EPERM"
		f := feature(t, featureStates(settings{}, held, caps, nil, engine.NATReport{}, nil, engine.PortMappingReport{}, nil), "cap_net_admin")
		want := "no_cap_net_admin"
		if held {
			want = "net_admin_dropped"
		}
		if f.State != "off" || f.Reason != want {
			t.Fatalf("admin=%+v", f)
		}
	}
	for _, tc := range []struct {
		name, target           string
		buffer                 engine.BufferReport
		state, reason, granted string
	}{
		{"forced limit", "socket_receive_buffer", engine.BufferReport{Requested: request, Granted: 8 << 20, Forced: true}, "capped", "limit", "4194304"},
		{"force failure", "socket_receive_buffer", engine.BufferReport{Requested: request, Granted: 8 << 20, ForceErrno: "EINVAL"}, "capped", "force_failed", "4194304"},
		{"receive usable grant", "socket_receive_buffer", engine.BufferReport{Requested: request, Granted: 14 << 20, Forced: true}, "on", "", "7340032"},
		{"send usable grant", "socket_send_buffer", engine.BufferReport{Requested: request, Granted: 14 << 20, Forced: true}, "on", "", "7340032"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caps := base
			if tc.target == "socket_send_buffer" {
				caps.SendBuffer = tc.buffer
			} else {
				caps.ReceiveBuffer = tc.buffer
			}
			f := feature(t, featureStates(settings{}, false, caps, nil, engine.NATReport{}, nil, engine.PortMappingReport{}, nil), tc.target)
			if f.State != tc.state || f.Reason != tc.reason {
				t.Fatalf("buffer=%+v", f)
			}
			if want := strconv.Itoa(request); f.Requested != want || f.Granted != tc.granted {
				t.Errorf("Requested/Granted = %s/%s, want %s/%s", f.Requested, f.Granted, want, tc.granted)
			}
		})
	}
}

func testNetworkFeatureStates(t *testing.T, base engine.CapabilityReport) {
	t.Helper()
	nat := engine.NATReport{UDP: true, IPv4: true, IPv6: true, GlobalV4: netip.MustParseAddrPort("192.0.2.1:1234"), GlobalV6: netip.MustParseAddrPort("[2001:db8::1]:1234"), MappingVariesByDest: engine.TriNo}
	rows := featureStates(settings{enabled: true, portMapping: true}, false, base, nil, nat, nil, engine.PortMappingReport{UPnP: true, PMP: true, PCP: true}, nil)
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Feature)
	}
	want := []string{"direct", "gso", "gro", "pmtu_fallback", "socket_receive_buffer", "socket_send_buffer", "cap_net_admin", "nat_mapping", "ipv4", "ipv6", "nat_hairpin", "port_mapping", "reachability"}
	if !slices.Equal(names, want) {
		t.Errorf("features=%v, want %v", names, want)
	}
	for _, name := range []string{"direct", "nat_mapping", "ipv4", "ipv6", "port_mapping"} {
		if f := feature(t, rows, name); f.State != "on" {
			t.Fatalf("%s=%+v", name, f)
		}
	}
	if f := feature(t, rows, "port_mapping"); f.Detail != "upnp,pmp,pcp" {
		t.Fatalf("protocols=%+v", f)
	}
	for _, name := range []string{"nat_hairpin", "reachability"} {
		if f := feature(t, rows, name); f.State != "unknown" || f.Reason != "not_measured" {
			t.Fatalf("%s=%+v", name, f)
		}
	}
	if f := feature(t, rows, "reachability"); f.Detail != "192.0.2.1:1234" {
		t.Fatalf("mapped address=%+v", f)
	}
	for _, tc := range []struct {
		name, target string
		nat          engine.NATReport
		reason       string
	}{
		{"no UDP", "nat_mapping", engine.NATReport{}, "probe_failed"},
		{"IPv4 no UDP", "ipv4", engine.NATReport{}, "probe_failed"},
		{"IPv6 no UDP", "ipv6", engine.NATReport{}, "probe_failed"},
		{"IPv4 only", "ipv6", engine.NATReport{UDP: true, IPv4: true}, "not_available"},
		{"IPv6 only", "ipv4", engine.NATReport{UDP: true, IPv6: true}, "not_available"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := featureStates(settings{enabled: true}, false, base, nil, tc.nat, nil, engine.PortMappingReport{}, nil)
			f := feature(t, rows, tc.target)
			if f.State != "off" || f.Reason != tc.reason {
				t.Fatalf("feature=%+v, want off/%s", f, tc.reason)
			}
		})
	}
	v6only := nat
	v6only.GlobalV4 = netip.AddrPort{}
	if f := feature(t, featureStates(settings{enabled: true}, false, base, nil, v6only, nil, engine.PortMappingReport{}, nil), "reachability"); f.Detail != "[2001:db8::1]:1234" {
		t.Fatalf("IPv6 mapped=%+v", f)
	}
	for _, tri := range []engine.Tri{engine.TriYes, engine.TriUnknown} {
		nat.MappingVariesByDest = tri
		f := feature(t, featureStates(settings{enabled: true}, false, base, nil, nat, nil, engine.PortMappingReport{}, nil), "nat_mapping")
		want := "mapping_varies"
		if tri == engine.TriUnknown {
			want = "not_measured"
		}
		if f.Reason != want {
			t.Fatalf("mapping=%+v", f)
		}
	}
	for _, mapping := range []engine.PortMappingReport{{NoGateway: true}, {}} {
		f := feature(t, featureStates(settings{enabled: true, portMapping: true}, false, base, nil, nat, nil, mapping, nil), "port_mapping")
		want := "gateway_unsupported"
		if mapping.NoGateway {
			want = "no_gateway"
		}
		if f.State != "off" || f.Reason != want {
			t.Fatalf("gateway=%+v", f)
		}
	}
	rows = featureStates(settings{enabled: true, portMapping: true}, false, base, nil, nat, errors.New("NAT failed"), engine.PortMappingReport{}, errors.New("mapping failed"))
	for _, name := range []string{"nat_mapping", "ipv4", "ipv6", "port_mapping"} {
		f := feature(t, rows, name)
		if f.State != "degraded" || f.Reason != "probe_failed" {
			t.Fatalf("failure=%+v", f)
		}
	}
}

func testUnsupportedPlatformFeatureStates(t *testing.T) {
	t.Helper()
	rows := featureStates(settings{enabled: true}, false, engine.CapabilityReport{Platform: "darwin"}, nil, engine.NATReport{}, nil, engine.PortMappingReport{}, nil)
	for _, f := range rows {
		if f.Kind != "local" {
			continue
		}
		if f.Requested != "" || f.Granted != "" {
			t.Fatalf("unmeasured platform byte counts = %+v", f)
		}
		if f.State != "off" || f.Reason != "unsupported_platform" {
			t.Fatalf("platform=%+v", f)
		}
	}
}

// A family that no configured STUN server can answer in is not measured, rather
// than reported unavailable.
func TestAddressFamilyMeasuredOnlyWithAServerInIt(t *testing.T) {
	base := engine.CapabilityReport{Platform: "linux"}
	for _, tc := range []struct {
		name, servers, target, state, reason string
		nat                                  engine.NATReport
	}{
		{"IPv4 literals leave IPv6 unmeasured", "198.51.100.1:3478,198.51.100.2:3478", "ipv6", "unknown", "not_measured", engine.NATReport{UDP: true, IPv4: true}},
		{"IPv6 literals leave IPv4 unmeasured", "[2001:db8::2]:3478", "ipv4", "unknown", "not_measured", engine.NATReport{UDP: true, IPv6: true}},
		{"a host name measures both", "198.51.100.1:3478,stun.example:3478", "ipv6", "off", "not_available", engine.NATReport{UDP: true, IPv4: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := settings{enabled: true, stunServers: tc.servers}
			f := feature(t, featureStates(cfg, false, base, nil, tc.nat, nil, engine.PortMappingReport{}, nil), tc.target)
			if f.State != tc.state || f.Reason != tc.reason {
				t.Fatalf("%s = %s/%s, want %s/%s", tc.target, f.State, f.Reason, tc.state, tc.reason)
			}
		})
	}
}
