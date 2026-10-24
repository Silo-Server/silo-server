package requests

import (
	"context"
	"testing"
)

func TestSeriesRouterConnectionsNeedAnEnabledSeriesServer(t *testing.T) {
	sonarr := Integration{Enabled: true, CapabilityID: "arr", SupportedMediaTypes: []string{string(MediaTypeSeries)}}
	disabled := sonarr
	disabled.Enabled = false
	radarr := Integration{Enabled: true, CapabilityID: "arr", SupportedMediaTypes: []string{string(MediaTypeMovie)}}
	tests := []struct {
		name string
		in   []Integration
		want int
	}{
		{"none", nil, 0},
		{"movies only", []Integration{radarr}, 0},
		{"disabled", []Integration{disabled}, 0},
		{"enabled series server", []Integration{radarr, sonarr}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := len(seriesRouterConnections(&fulfillContext{integrations: tt.in})); got != tt.want {
				t.Fatalf("series connections = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSeriesServerConnectedWithoutRouterReadsNothing(t *testing.T) {
	// A nil store would panic if the configuration were read.
	connected, err := (&Service{}).seriesServerConnected(context.Background())
	if err != nil || connected {
		t.Fatalf("seriesServerConnected = %v, %v; want false, nil", connected, err)
	}
}
