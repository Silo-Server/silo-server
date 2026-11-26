package directconn

import (
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/tuning"
)

// Feature is one observed local capability or network result.
type Feature struct {
	Feature   string `json:"feature" enum:"direct,engine,nat_mapping,nat_hairpin,ipv4,ipv6,port_mapping,reachability,socket_receive_buffer,socket_send_buffer,gso,gro,pmtu_fallback,cap_net_admin"`
	Kind      string `json:"kind" enum:"local,network"`
	State     string `json:"state" enum:"on,off,capped,fallback,degraded,unknown"`
	Reason    string `json:"reason,omitempty" enum:"disabled,admin_off,kernel_unsupported,limit,force_failed,no_cap_net_admin,net_admin_dropped,unsupported_platform,probe_failed,not_available,no_gateway,gateway_unsupported,mapping_varies,not_measured,multiple_api_replicas,replica_census_unavailable,replica_lease_pending,replica_lease_held,udp_port_invalid,udp_port_bind_failed,engine_start_failed"`
	Requested string `json:"requested,omitempty"`
	Granted   string `json:"granted,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// SendBuffer is a listener's TCP send-buffer request and usable grant in bytes.
type SendBuffer struct {
	Requested int    `json:"requested"`
	Granted   int    `json:"granted"`
	State     string `json:"state"`
	Reason    string `json:"reason"`
	Errno     string `json:"errno,omitempty"`
}

// Listener records a bound client-facing listener, supplied by main.
type Listener struct {
	Listener   string     `json:"listener" enum:"api,jellyfin,abs,proxy"`
	Requested  string     `json:"requested"`
	Granted    string     `json:"granted"`
	State      string     `json:"state"`
	Reason     string     `json:"reason"`
	Errno      string     `json:"errno,omitempty"`
	SendBuffer SendBuffer `json:"send_buffer"`
}

// Knob combines tuning metadata with this process's effective startup value.
type Knob struct {
	Env       string   `json:"env"`
	Default   string   `json:"default"`
	Allowed   []string `json:"allowed,omitempty"`
	Min       string   `json:"min,omitempty"`
	Max       string   `json:"max,omitempty"`
	Requested string   `json:"requested,omitempty"`
	Effective string   `json:"effective"`
	Reason    string   `json:"reason,omitempty"`
}

// Status is an immutable snapshot of the API host's last completed check.
type Status struct {
	Enabled   bool
	CheckedAt time.Time
	Features  []Feature
	Listeners []Listener
	Knobs     []Knob
}

// SetListeners replaces the bound-listener snapshot, copying the caller's slice.
func (s *Service) SetListeners(listeners []Listener) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append([]Listener(nil), listeners...)
}

func statusKnobs(cfg tuning.Config, adjustments []tuning.Adjustment) []Knob {
	out := make([]Knob, 0, len(tuning.Knobs()))
	for _, metadata := range tuning.Knobs() {
		row := Knob{Env: metadata.Env, Default: metadata.Default, Allowed: metadata.Allowed, Min: metadata.Min, Max: metadata.Max}
		switch metadata.Env {
		case tuning.EnvTCPCongestion:
			row.Effective = string(cfg.TCPCongestion)
		case tuning.EnvTCPSendBuffer:
			row.Effective = strconv.Itoa(cfg.TCPSendBuffer)
			if cfg.TCPSendBuffer == 0 {
				row.Effective = stateOff
			}
		case tuning.EnvDirectUDPPort:
			row.Effective = strconv.Itoa(cfg.DirectUDPPort)
		case tuning.EnvDirectGSO:
			row.Effective = stateOff
			if cfg.DirectGSO {
				row.Effective = stateOn
			}
		case tuning.EnvDirectBBRLoss:
			row.Effective = cfg.DirectBBRLoss
		case tuning.EnvDirectReceiveWindow:
			row.Effective = strconv.Itoa(cfg.DirectReceiveWindowMiB)
		case tuning.EnvDirectACKDecimation:
			row.Effective = stateOff
			if cfg.DirectACKDecimation {
				row.Effective = stateOn
			}
		}
		for _, a := range adjustments {
			if a.Env == row.Env {
				row.Requested = a.Requested
				row.Reason = a.Reason
			}
		}
		out = append(out, row)
	}
	return out
}
