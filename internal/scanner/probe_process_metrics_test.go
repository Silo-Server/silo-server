package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func probeExitCount(t *testing.T) float64 {
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
				if label.GetName() == "workload" && label.GetValue() == "probe" {
					total += metric.GetCounter().GetValue()
				}
			}
		}
	}
	return total
}

func TestProbeAccountsAtOutputCompletion(t *testing.T) {
	probe := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nprintf '%s' '{\"format\":{\"duration\":\"1\"},\"streams\":[]}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	before := probeExitCount(t)
	if _, err := ProbeFile(context.Background(), probe, "fixture"); err != nil {
		t.Fatal(err)
	}
	if got := probeExitCount(t); got != before+1 {
		t.Fatalf("one probe accounted %v times", got-before)
	}
}
