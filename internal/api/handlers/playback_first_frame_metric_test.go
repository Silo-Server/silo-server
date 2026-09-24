package handlers

import (
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// firstFrameSamples reads the web series of silo_playback_first_frame_seconds
// from the default registry: zero when the series does not exist yet.
func firstFrameSamples(t *testing.T) (count uint64, sum float64) {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "silo_playback_first_frame_seconds" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "client" && label.GetValue() == "web" {
					return metric.GetHistogram().GetSampleCount(), metric.GetHistogram().GetSampleSum()
				}
			}
		}
	}
	return 0, 0
}

// waitForFirstFrameSamples polls the histogram until the queued writer has
// observed want samples. Route events are written off the request path.
func waitForFirstFrameSamples(t *testing.T, want uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		count, _ := firstFrameSamples(t)
		if count == want {
			return
		}
		if count > want || time.Now().After(deadline) {
			t.Fatalf("first frame samples = %d, want %d", count, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestReportRouteEventV2ObservesFirstFrameOnce proves a first_frame report
// feeds silo_playback_first_frame_seconds exactly once. A client that retries
// after a lost 202 reuses the event_id; the store ignores the second row, and
// the histogram must ignore it too.
func TestReportRouteEventV2ObservesFirstFrameOnce(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	identity, err := f.handler.PlanStoreV3.GetAttemptIdentity(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The web player, like the native apps, names itself with X-Silo-Client
	// and sends no X-Client-Name: the v2 adapter passes it as SiloClientName.
	caller := f.caller
	caller.SiloClientName = "Silo Web"
	report := func(eventID, firstFrameMs string) {
		t.Helper()
		err := f.handler.ReportRouteEventV2(f.ctx, caller, PlaybackRouteEventCommand{
			EventID: eventID,
			Event: playback.RouteEventV3{
				ProtocolVersion:   playback.ProtocolV3,
				PlaybackAttemptID: identity.PlaybackAttemptID,
				SessionID:         f.session.ID,
				Event:             playback.RouteEventFirstFrameV3,
				Diagnostics:       map[string]string{"first_frame_ms": firstFrameMs},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	baseCount, baseSum := firstFrameSamples(t)
	first := uuid.NewString()
	report(first, "1250")
	waitForFirstFrameSamples(t, baseCount+1)

	report(first, "1250") // the retry
	report(uuid.NewString(), "750")
	// One consumer drains the queue in order, so once the second event is
	// counted the retry ahead of it has been handled.
	waitForFirstFrameSamples(t, baseCount+2)
	count, sum := firstFrameSamples(t)
	if count != baseCount+2 {
		t.Fatalf("samples = %d, want %d: the retry was counted", count-baseCount, 2)
	}
	if got := sum - baseSum; math.Abs(got-2.0) > 1e-9 {
		t.Fatalf("sum = %v s, want 2.0 s (1.25 + 0.75)", got)
	}
}
