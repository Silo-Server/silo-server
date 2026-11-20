package playback

import "testing"

// A retry narrower than an in-flight recovery warm joins that warm instead of
// starting a second relay read for a slice the warm is already producing. The
// budget-breach warm extracts the breached range plus one client cell, and the
// retry re-anchors a few seconds forward inside that range.
func TestWindowFlightCoveringWarmAbsorbsNarrowerRetry(t *testing.T) {
	c, source := newTestCache(t)
	wide := StreamExtractOpts{
		InputPath:       source,
		CacheIdentity:   "virtual-identity",
		TrackIndex:      0,
		SourceCodec:     "ass",
		TargetFormat:    "ass",
		SeekSeconds:     580,
		DurationSeconds: 150,
		WindowRequested: true,
	}
	wideKey, ok := c.windowResultKey(wide, subtitleFormatASS)
	if !ok {
		t.Fatal("wide window is not keyable")
	}
	if leader, _, _ := c.beginWindowFlight(wideKey, wide); !leader {
		t.Fatal("wide window did not become the flight leader")
	}

	narrow := wide
	narrow.SeekSeconds = 590
	narrow.DurationSeconds = 120
	narrowKey, ok := c.windowResultKey(narrow, subtitleFormatASS)
	if !ok {
		t.Fatal("narrow window is not keyable")
	}
	leader, joined, _ := c.beginWindowFlight(narrowKey, narrow)
	if leader {
		t.Fatal("narrow retry started its own extract despite a covering in-flight warm")
	}
	c.endWindowFlight(wideKey)
	select {
	case <-joined:
	default:
		t.Fatal("narrow retry's joined flight did not settle with the warm")
	}

	// A window outside the warm's range still extracts on its own.
	far := wide
	far.SeekSeconds = 900
	farKey, ok := c.windowResultKey(far, subtitleFormatASS)
	if !ok {
		t.Fatal("far window is not keyable")
	}
	if leader, _, _ := c.beginWindowFlight(farKey, far); !leader {
		t.Fatal("uncovered window did not become its own flight leader")
	}
	c.endWindowFlight(farKey)
}
