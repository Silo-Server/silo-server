package database

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

func TestRaiseMaxConnsToSupportedMinimum(t *testing.T) {
	tests := []struct {
		maxConns int32
		want     int32
		raised   bool
	}{
		{maxConns: 1, want: config.MinDatabaseMaxConnections, raised: true},
		{maxConns: config.MinDatabaseMaxConnections, want: config.MinDatabaseMaxConnections},
		{maxConns: 20, want: 20},
	}
	for _, tt := range tests {
		got, raised := raiseMaxConnsToSupportedMinimum(tt.maxConns)
		if got != tt.want || raised != tt.raised {
			t.Fatalf("raise(%d) = (%d, %t), want (%d, %t)", tt.maxConns, got, raised, tt.want, tt.raised)
		}
		if pinnedConnectionCapacity(got) < 1 {
			t.Fatalf("raised pool size %d leaves no pinned-connection capacity", got)
		}
	}
}
