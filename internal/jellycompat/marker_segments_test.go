package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestMediaSegmentsPreservesOccurrencesAndLegacyIDs(t *testing.T) {
	version := &catalog.FileVersion{MarkerSegments: []models.MarkerSegment{
		{Kind: "intro", StartSeconds: 10, EndSeconds: 20},
		{Kind: "intro", StartSeconds: 40, EndSeconds: 50},
		{Kind: "credits", StartSeconds: 100, EndSeconds: 110},
	}}
	segments := buildMediaSegmentDTOs("item", version)
	if len(segments) != 3 || segments[0].EndTicks != secondsToTicks(20) || segments[1].StartTicks != secondsToTicks(40) || segments[2].Type != "Outro" {
		t.Fatalf("segments = %+v", segments)
	}
	if segments[0].Id == segments[1].Id || segments[0].Id != deriveSegmentID("item", "Intro") {
		t.Fatalf("occurrence IDs collide or changed legacy identity: %+v", segments)
	}
	legacy := buildMediaSegmentDTOs("item", &catalog.FileVersion{Intro: &catalog.Marker{Start: 10, End: 20}})
	if len(legacy) != 1 || legacy[0] != segments[0] {
		t.Fatalf("legacy marker fallback = %+v", legacy)
	}
}

// A file can carry an early credits block beside the end credits. Jellyfin has
// a single Outro type and hangs its next-episode affordance on it, so only the
// end credits are exposed as Outro; the early occurrence is omitted from this
// compatibility payload (the native API still serves it through
// marker_segments).
func TestMediaSegmentsEmitOutroOnlyForEndCredits(t *testing.T) {
	for _, tc := range []struct {
		name     string
		duration int
	}{
		{name: "with duration, the occurrence ending latest wins", duration: 1000},
		{name: "without duration, the last occurrence by start wins", duration: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			version := &catalog.FileVersion{Duration: tc.duration, MarkerSegments: []models.MarkerSegment{
				{Kind: "intro", StartSeconds: 10, EndSeconds: 20},
				{Kind: "credits", StartSeconds: 800, EndSeconds: 850},
				{Kind: "credits", StartSeconds: 900, EndSeconds: 950},
			}}
			segments := buildMediaSegmentDTOs("item", version)
			outros := 0
			for _, segment := range segments {
				if segment.Type != "Outro" {
					continue
				}
				outros++
				if segment.StartTicks != secondsToTicks(900) || segment.EndTicks != secondsToTicks(950) {
					t.Fatalf("Outro = %+v, want the end credits 900-950", segment)
				}
			}
			if outros != 1 {
				t.Fatalf("Outro segments = %d, want exactly the end credits: %+v", outros, segments)
			}
			// The end-credits Outro keeps the legacy identity clients cache by.
			if segments[len(segments)-1].Id != deriveSegmentID("item", "Outro") {
				t.Fatalf("Outro identity changed: %+v", segments)
			}
		})
	}
}

func TestMediaSegmentsOutroFollowsTheEndCreditsBounds(t *testing.T) {
	// The singular credits bounds are the end credits, so the Outro follows them
	// even when another credits occurrence ends later: ranking occurrences here
	// would move a client's next-episode affordance to a range the marker
	// pipeline does not call the end credits.
	version := &catalog.FileVersion{Duration: 2400, Credits: &catalog.Marker{Start: 2350, End: 2400},
		MarkerSegments: []models.MarkerSegment{
			{Kind: "credits", StartSeconds: 2000, EndSeconds: 2050},
			{Kind: "credits", StartSeconds: 2350, EndSeconds: 2400},
			{Kind: "credits", StartSeconds: 2395, EndSeconds: 2440},
		}}
	segments := buildMediaSegmentDTOs("item", version)
	if len(segments) != 1 || segments[0].Type != "Outro" || segments[0].StartTicks != secondsToTicks(2350) || segments[0].EndTicks != secondsToTicks(2400) {
		t.Fatalf("segments = %+v, want the end credits as the only Outro", segments)
	}
}

func TestMediaSegmentsIgnoreCreditsPastTheFileEnd(t *testing.T) {
	// A credits occurrence that runs past the file cannot be an Outro: an
	// affordance starting after playback ends never fires. Without a stored
	// singular the fallback must skip it rather than take the latest start.
	version := &catalog.FileVersion{Duration: 1000,
		MarkerSegments: []models.MarkerSegment{
			{Kind: "credits", StartSeconds: 950, EndSeconds: 999},
			{Kind: "credits", StartSeconds: 1100, EndSeconds: 1150},
		}}
	segments := buildMediaSegmentDTOs("item", version)
	if len(segments) != 1 || segments[0].Type != "Outro" || segments[0].StartTicks != secondsToTicks(950) {
		t.Fatalf("segments = %+v, want the in-bounds credits as the Outro", segments)
	}
}

func TestMediaSegmentsPublishEndCreditsThatEndPastTheFile(t *testing.T) {
	// A credits range that runs a second or two past the recorded duration is
	// the ordinary rounding of end credits running to the end of the file, and
	// it is the range that finishes the item: skipping it would move a client's
	// affordance to an earlier block, or drop it.
	version := &catalog.FileVersion{Duration: 2400, Credits: &catalog.Marker{Start: 2352, End: 2402},
		MarkerSegments: []models.MarkerSegment{
			{Kind: "credits", StartSeconds: 2000, EndSeconds: 2050},
			{Kind: "credits", StartSeconds: 2352, EndSeconds: 2402},
		}}
	segments := buildMediaSegmentDTOs("item", version)
	if len(segments) != 1 || segments[0].Type != "Outro" || segments[0].StartTicks != secondsToTicks(2352) {
		t.Fatalf("segments = %+v, want the end credits as the Outro", segments)
	}
}

func TestMediaSegmentsIgnoreEndCreditsStartingAtTheFileEnd(t *testing.T) {
	// A credits range starting at the recorded duration is never reached: the
	// end-credits selection counts only a start below it. A later credits
	// occurrence must not take the Outro on the strength of its start.
	version := &catalog.FileVersion{Duration: 1000, Credits: &catalog.Marker{Start: 1000, End: 1050},
		MarkerSegments: []models.MarkerSegment{
			{Kind: "credits", StartSeconds: 950, EndSeconds: 999},
			{Kind: "credits", StartSeconds: 1000, EndSeconds: 1050},
		}}
	segments := buildMediaSegmentDTOs("item", version)
	if len(segments) != 1 || segments[0].Type != "Outro" || segments[0].StartTicks != secondsToTicks(950) {
		t.Fatalf("segments = %+v, want the playable credits as the Outro", segments)
	}
}
