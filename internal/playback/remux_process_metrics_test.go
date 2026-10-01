package playback

import (
	"context"
	"io"
	"os/exec"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func remuxCompletedCount(t *testing.T) float64 {
	return remuxOutcomeCount(t, "")
}

func remuxOutcomeCount(t *testing.T, outcome string) float64 {
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
			matchesOutcome := outcome == ""
			for _, label := range metric.Label {
				if label.GetName() == "outcome" && label.GetValue() == outcome {
					matchesOutcome = true
				}
			}
			if !matchesOutcome {
				continue
			}
			for _, label := range metric.Label {
				if label.GetName() == "workload" && label.GetValue() == "remux" {
					total += metric.GetCounter().GetValue()
				}
			}
		}
	}
	return total
}

func TestRemuxCleanupPreservesCompletedFailureOutcome(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "printf failed; exit 7")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	session := &RemuxSession{cmd: cmd, ctx: ctx, cancel: cancel, outputPipe: stdout}
	if _, err := io.Copy(io.Discard, session); err != nil {
		t.Fatal(err)
	}
	before := remuxOutcomeCount(t, "error")
	if err := session.Close(); err == nil {
		t.Fatal("expected child failure")
	}
	if cmd.ProcessState.ExitCode() != 7 {
		t.Fatalf("child exit: %v", cmd.ProcessState)
	}
	if got := remuxOutcomeCount(t, "error"); got != before+1 {
		t.Fatalf("cleanup hid completed error: count=%v", got-before)
	}
}

func TestRemuxRepeatedCloseReapsAndAccountsOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "printf complete")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	session := &RemuxSession{cmd: cmd, ctx: ctx, cancel: cancel, outputPipe: stdout}
	if _, err := io.Copy(io.Discard, session); err != nil {
		t.Fatal(err)
	}
	before := remuxCompletedCount(t)
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() { _ = session.Close() })
	}
	wg.Wait()
	if got := remuxCompletedCount(t); got != before+1 {
		t.Fatalf("one process counted %v completions", got-before)
	}
	if cmd.ProcessState == nil {
		t.Fatal("child was not reaped")
	}
}
