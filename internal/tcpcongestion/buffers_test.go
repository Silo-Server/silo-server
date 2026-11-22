package tcpcongestion

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/netadmin"
)

func TestDecideSendBuffer(t *testing.T) {
	tests := []struct {
		name                 string
		request, granted     int
		state, reason        string
		requested, wantGrant int
	}{
		{name: "custom size", request: 16777216, granted: 33554432, state: "on", requested: 16777216, wantGrant: 16777216},
		{name: "custom grant too small", request: 16777216, granted: 33554431, state: "capped", reason: "granted_below_request", requested: 16777216, wantGrant: 16777215},
		{name: "grant too small", request: 33554432, granted: 33554432, state: "capped", reason: "granted_below_request", requested: 33554432, wantGrant: 16777216},
		{name: "forced", request: 33554432, granted: 67108864, state: "on", requested: 33554432, wantGrant: 33554432},
		{name: "usable grant above request", request: 16777216, granted: 50331648, state: "on", requested: 16777216, wantGrant: 25165824},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideSendBuffer(sendBufferInput{
				listener: ListenerAPI, request: tt.request, linux: true, limits: Limits{State: "capped"}, helper: netadmin.HelperRunning,
				granted: tt.granted,
			})
			if got.State != tt.state || got.Reason != tt.reason {
				t.Errorf("state/reason = %q/%q, want %q/%q", got.State, got.Reason, tt.state, tt.reason)
			}
			if got.Requested != tt.requested || got.Granted != tt.wantGrant {
				t.Errorf("Requested/Granted = %d/%d, want %d/%d", got.Requested, got.Granted, tt.requested, tt.wantGrant)
			}
		})
	}
}
