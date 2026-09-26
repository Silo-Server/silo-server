package processmetrics

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

func TestProcessMetricsChild(t *testing.T) {
	switch os.Getenv("SILO_PROCESS_METRICS_CHILD") {
	case "allocate":
		memory := make([]byte, 64<<20)
		for i := range memory {
			memory[i] = byte(i)
		}
		runtime.KeepAlive(memory)
		os.Exit(0)
	case "wait":
		fmt.Println("ready")
		<-time.NewTimer(time.Hour).C
		os.Exit(0)
	}
}

func rssCount(t *testing.T, label string) uint64 {
	t.Helper()
	m := &dto.Metric{}
	if err := peakRSS.WithLabelValues(label).(interface{ Write(*dto.Metric) error }).Write(m); err != nil {
		t.Fatal(err)
	}
	return m.GetHistogram().GetSampleCount()
}

func TestRecordsRealChildCPUAndPeakRSS(t *testing.T) {
	beforeCPU := testutil.ToFloat64(cpu.WithLabelValues("probe", "user"))
	beforeExits := testutil.ToFloat64(exits.WithLabelValues("probe", "success"))
	beforeRSS := rssCount(t, "probe")
	cmd := exec.Command(os.Args[0], "-test.run=^TestProcessMetricsChild$")
	cmd.Env = append(os.Environ(), "SILO_PROCESS_METRICS_CHILD=allocate")
	err := cmd.Run()
	if err != nil {
		t.Fatal(err)
	}
	Record(Probe, cmd.ProcessState, err, nil)
	if got := testutil.ToFloat64(cpu.WithLabelValues("probe", "user")) - beforeCPU; got != cmd.ProcessState.UserTime().Seconds() {
		t.Fatalf("CPU %v differs from Wait's %v", got, cmd.ProcessState.UserTime())
	}
	if got := testutil.ToFloat64(exits.WithLabelValues("probe", "success")); got != beforeExits+1 {
		t.Fatalf("completion count: %v", got)
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		rss, ok := maximumRSS(cmd.ProcessState)
		if !ok || rss < 64<<20 || rss > 1<<30 {
			t.Fatalf("RSS units incorrect for 64 MiB child: %v available=%v", rss, ok)
		}
		if got := rssCount(t, "probe"); got != beforeRSS+1 {
			t.Fatalf("RSS sample count: %d", got)
		}
	}
}

func TestCanceledChildAndStartFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessMetricsChild$")
	cmd.Env = append(os.Environ(), "SILO_PROCESS_METRICS_CHILD=wait")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("child readiness: %q %v", line, err)
	}
	cancel()
	before := testutil.ToFloat64(exits.WithLabelValues("transcode", "canceled"))
	err = cmd.Wait()
	Record(Transcode, cmd.ProcessState, err, ctx.Err())
	if got := testutil.ToFloat64(exits.WithLabelValues("transcode", "canceled")); got != before+1 {
		t.Fatalf("canceled completion count: %v", got)
	}
	before = testutil.ToFloat64(exits.WithLabelValues("remux", "start_error"))
	beforeRSS := rssCount(t, "remux")
	missing := exec.Command("/nonexistent-silo-metrics-test")
	err = missing.Run()
	Record(Remux, missing.ProcessState, err, nil)
	if got := testutil.ToFloat64(exits.WithLabelValues("remux", "start_error")); got != before+1 {
		t.Fatalf("start failure count: %v", got)
	}
	if got := rssCount(t, "remux"); got != beforeRSS {
		t.Fatal("failure to start invented memory usage")
	}
}
