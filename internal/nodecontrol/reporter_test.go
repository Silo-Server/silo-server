package nodecontrol

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodesessions"
)

type recordingPoster struct {
	mu      sync.Mutex
	reports []Report
	sizes   []int
}

func (p *recordingPoster) PostReport(_ context.Context, body []byte) error {
	var report Report
	if err := json.Unmarshal(body, &report); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reports = append(p.reports, report)
	p.sizes = append(p.sizes, len(body))
	return nil
}

func (p *recordingPoster) all() []Report {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Report(nil), p.reports...)
}

// newestFirst lists n sessions, newest first, as LocalTracker.Snapshot does.
func newestFirst(n int) []nodesessions.SessionInfo {
	sessions := make([]nodesessions.SessionInfo, n)
	for i := range sessions {
		sessions[i] = nodesessions.SessionInfo{SessionID: "s" + strconv.Itoa(i), Type: "transcode", StartedAtUnixNano: int64(n - i)}
	}
	return sessions
}

// The node reports once a second with one boot id and a sequence that
// increases with every report, which is what the core fences on.
func TestReporterReportsEverySecondWithAnIncreasingSequence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		poster := &recordingPoster{}
		reporter := NewReporter(poster, func() []nodesessions.SessionInfo { return newestFirst(1) }, nil)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); reporter.Run(ctx) }()
		time.Sleep(3*ReportInterval + ReportInterval/2) //nolint:forbidigo // advances the synctest bubble's fake clock
		synctest.Wait()
		cancel()
		<-done

		reports := poster.all()
		if len(reports) != 3 {
			t.Fatalf("sent %d reports in 3.5 report intervals, want 3", len(reports))
		}
		for i, report := range reports {
			if report.BootID != reports[0].BootID || report.Seq != uint64(i+1) {
				t.Fatalf("report %d = boot %s seq %d, want boot %s seq %d", i, report.BootID, report.Seq, reports[0].BootID, i+1)
			}
		}
	})
}

// A node with more sessions than one report carries sends the newest and
// says so; one whose report would still exceed the body bound halves until it
// fits rather than sending nothing.
func TestReporterKeepsTheNewestSessionsWithinItsBounds(t *testing.T) {
	poster := &recordingPoster{}
	sessions := newestFirst(MaxReportSessions + 500)
	reporter := NewReporter(poster, func() []nodesessions.SessionInfo { return sessions }, nil)
	if err := reporter.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	report := poster.all()[0]
	if len(report.Sessions) != MaxReportSessions || !report.Truncated || report.Sessions[0].SessionID != "s0" {
		t.Fatalf("report carries %d sessions (truncated=%v, first %q), want the newest %d and truncated",
			len(report.Sessions), report.Truncated, report.Sessions[0].SessionID, MaxReportSessions)
	}

	reporter.maxBody = 64 << 10
	if err := reporter.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	halved := poster.all()[1]
	if poster.sizes[1] > reporter.maxBody || len(halved.Sessions) == 0 || len(halved.Sessions) >= MaxReportSessions ||
		!halved.Truncated || halved.Sessions[0].SessionID != "s0" {
		t.Fatalf("oversized report sent %d bytes with %d sessions (truncated=%v), want at most %d bytes of the newest",
			poster.sizes[1], len(halved.Sessions), halved.Truncated, reporter.maxBody)
	}
}
