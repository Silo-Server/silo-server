package directconn

import (
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/darthShadow/shunpike/engine"
)

const (
	featureDirect     = "direct"
	featureIPv6       = "ipv6"
	kindLocal         = "local"
	stateOn           = "on"
	stateOff          = "off"
	stateUnknown      = "unknown"
	stateDegraded     = "degraded"
	reasonDisabled    = "disabled"
	reasonNotMeasured = "not_measured"
	reasonProbeFailed = "probe_failed"
)

func featureStates(cfg settings, heldAtStart bool, caps engine.CapabilityReport, capErr error, nat engine.NATReport, natErr error, mapping engine.PortMappingReport, mappingErr error) []Feature {
	return append(localFeatureStates(cfg, heldAtStart, caps, capErr), networkFeatureStates(cfg, nat, natErr, mapping, mappingErr)...)
}

func localFeatureStates(cfg settings, heldAtStart bool, caps engine.CapabilityReport, capErr error) []Feature {
	rows := []Feature{{Feature: featureDirect, Kind: kindLocal, State: stateOff, Reason: reasonDisabled}}
	if cfg.enabled {
		rows[0].State = stateOn
		rows[0].Reason = ""
	}
	for _, item := range []struct {
		name    string
		support engine.Support
	}{{"gso", caps.GSO}, {"gro", caps.GRO}, {"pmtu_fallback", caps.DF}} {
		f := Feature{Feature: item.name, Kind: kindLocal, State: stateUnknown, Reason: reasonNotMeasured}
		switch item.support {
		case engine.Supported:
			f.State = stateOn
			f.Reason = ""
		case engine.Unsupported:
			f.State = stateOff
			f.Reason = "kernel_unsupported"
		}
		rows = append(rows, f)
	}
	for _, item := range []struct {
		name   string
		buffer engine.BufferReport
	}{{"socket_receive_buffer", caps.ReceiveBuffer}, {"socket_send_buffer", caps.SendBuffer}} {
		b := item.buffer
		f := Feature{Feature: item.name, Kind: kindLocal, State: "capped"}
		if b.Requested > 0 {
			f.Requested = strconv.Itoa(b.Requested)
			// Linux doubles socket-buffer readbacks; report usable bytes (socket(7)).
			f.Granted = strconv.Itoa(b.Granted / 2)
		}
		switch {
		case b.Requested > 0 && b.Granted >= 2*b.Requested:
			f.State = stateOn
		case b.Forced:
			f.Reason = "limit"
		case b.ForceErrno == "EPERM" && heldAtStart:
			f.Reason = "net_admin_dropped"
		case b.ForceErrno == "EPERM":
			f.Reason = "no_cap_net_admin"
		default:
			f.Reason = "force_failed"
		}
		rows = append(rows, f)
	}
	admin := Feature{Feature: "cap_net_admin", Kind: kindLocal, State: stateOff, Reason: "no_cap_net_admin"}
	if caps.ReceiveBuffer.Forced || caps.SendBuffer.Forced {
		admin.State = stateOn
		admin.Reason = ""
	} else if heldAtStart {
		admin.Reason = "net_admin_dropped"
	}
	rows = append(rows, admin)
	for i := range rows {
		if caps.Platform != "linux" {
			rows[i].State = stateOff
			rows[i].Reason = "unsupported_platform"
		} else if capErr != nil && rows[i].Feature != featureDirect {
			rows[i].State = stateUnknown
			rows[i].Reason = reasonNotMeasured
		}
	}
	return rows
}

func networkFeatureStates(cfg settings, nat engine.NATReport, natErr error, mapping engine.PortMappingReport, mappingErr error) []Feature {
	measuresV4, measuresV6 := stunFamilies(cfg.servers())
	rows := make([]Feature, 0, 6)
	for _, name := range []string{"nat_mapping", "ipv4", featureIPv6, "nat_hairpin", "port_mapping", "reachability"} {
		f := Feature{Feature: name, Kind: "network", State: stateOff, Reason: reasonDisabled}
		if !cfg.enabled {
			rows = append(rows, f)
			continue
		}
		f.State = stateUnknown
		f.Reason = reasonNotMeasured
		switch name {
		case "nat_mapping":
			f.Detail = observedAddress(nat)
			switch {
			case natErr != nil:
				f.State = stateDegraded
				f.Reason = reasonProbeFailed
			case !nat.UDP:
				f.State = stateOff
				f.Reason = reasonProbeFailed
			case nat.MappingVariesByDest == engine.TriNo:
				f.State = stateOn
				f.Reason = ""
			case nat.MappingVariesByDest == engine.TriYes:
				f.State = stateDegraded
				f.Reason = "mapping_varies"
			}
		case "ipv4", featureIPv6:
			available := nat.IPv4
			addr := nat.GlobalV4
			measured := measuresV4
			if name == featureIPv6 {
				available = nat.IPv6
				addr = nat.GlobalV6
				measured = measuresV6
			}
			switch {
			case natErr != nil:
				f.State = stateDegraded
				f.Reason = reasonProbeFailed
			case !nat.UDP:
				f.State = stateOff
				f.Reason = reasonProbeFailed
			case available:
				f.State = stateOn
				f.Reason = ""
				if addr.IsValid() {
					f.Detail = addr.String()
				}
			case !measured:
				// No configured server can answer in this family; f stays
				// unknown/not_measured.
			default:
				f.State = stateOff
				f.Reason = "not_available"
			}
		case "port_mapping":
			switch {
			case !cfg.portMapping:
				f.State = stateOff
				f.Reason = "admin_off"
			case mappingErr != nil:
				f.State = stateDegraded
				f.Reason = reasonProbeFailed
			case mapping.NoGateway:
				f.State = stateOff
				f.Reason = "no_gateway"
			default:
				var protocols []string
				if mapping.UPnP {
					protocols = append(protocols, "upnp")
				}
				if mapping.PMP {
					protocols = append(protocols, "pmp")
				}
				if mapping.PCP {
					protocols = append(protocols, "pcp")
				}
				f.State = stateOff
				f.Reason = "gateway_unsupported"
				if len(protocols) > 0 {
					f.State = stateOn
					f.Reason = ""
					f.Detail = strings.Join(protocols, ",")
				}
			}
		case "reachability":
			f.Detail = observedAddress(nat)
		}
		rows = append(rows, f)
	}
	return rows
}

// stunFamilies reports which address families the STUN servers can measure. The
// prober sends to an IP literal only in its own family, so a list of IPv4
// literals cannot show whether IPv6 works. A host name, or an entry that does not
// parse, can reach either family.
func stunFamilies(servers []string) (v4, v6 bool) {
	for _, server := range servers {
		host, _, err := net.SplitHostPort(server)
		if err != nil {
			return true, true
		}
		addr, err := netip.ParseAddr(host)
		switch {
		case err != nil:
			return true, true
		case addr.Is4():
			v4 = true
		default:
			v6 = true
		}
	}
	if !v4 && !v6 {
		return true, true
	}
	return v4, v6
}

func observedAddress(nat engine.NATReport) string {
	if nat.GlobalV4.IsValid() {
		return nat.GlobalV4.String()
	}
	if nat.GlobalV6.IsValid() {
		return nat.GlobalV6.String()
	}
	return ""
}
