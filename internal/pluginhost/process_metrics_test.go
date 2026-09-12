package pluginhost

import (
	"context"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/prometheus/client_golang/prometheus"
)

func pluginExitCount(t *testing.T) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var total float64
	for _, family := range families {
		if family.GetName() != "silo_subprocess_exits_total" {
			continue
		}
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				if label.GetName() == "workload" && label.GetValue() == "plugin" {
					total += metric.GetCounter().GetValue()
				}
			}
		}
	}
	return total
}

func TestFailedPluginHandshakeAccountsCompletedProcess(t *testing.T) {
	before := pluginExitCount(t)
	host := NewHost(Config{})
	_, err := host.Start(context.Background(), StartRequest{InstallationID: 1, BinaryPath: "/bin/false", Manifest: &pluginv1.PluginManifest{PluginId: "metrics-fixture"}})
	if err == nil {
		t.Fatal("expected handshake failure")
	}
	if got := pluginExitCount(t); got != before+1 {
		t.Fatalf("completed plugin accounted %v times", got-before)
	}
}
