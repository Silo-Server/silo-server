package jellycompat

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestStubDetailListFields_PopulatesSingleElementSlicesForRequestedFields
// verifies the helper turns nil detail-only slices into single-element
// placeholder slices when the client asked for them. Single-element (rather
// than empty) is required so the JSON key is present regardless of the
// omitempty tag — every real item in a populated catalog has cast, sources,
// and streams, so a missing field would be a never-before-seen shape for
// strict client deserializers.
func TestStubDetailListFields_PopulatesSingleElementSlicesForRequestedFields(t *testing.T) {
	dto := baseItemDTO{}
	fields := map[string]bool{
		"people":       true,
		"chapters":     true,
		"mediastreams": true,
		"mediasources": true,
	}

	stubDetailListFields(&dto, fields)

	if len(dto.People) != 1 {
		t.Errorf("People = %v, want single-element slice", dto.People)
	}
	if len(dto.Chapters) != 1 {
		t.Errorf("Chapters = %v, want single-element slice", dto.Chapters)
	}
	if len(dto.MediaStreams) != 1 {
		t.Errorf("MediaStreams = %v, want single-element slice", dto.MediaStreams)
	}
	if len(dto.MediaSources) != 1 {
		t.Errorf("MediaSources = %v, want single-element slice", dto.MediaSources)
	}
}

// TestStubDetailListFields_LeavesUnrequestedFieldsNil verifies that fields the
// client did NOT request stay nil — preserving the omitempty serialization
// (key absent in JSON), matching what the list mapper would have returned for
// a request without that field.
func TestStubDetailListFields_LeavesUnrequestedFieldsNil(t *testing.T) {
	dto := baseItemDTO{}
	stubDetailListFields(&dto, map[string]bool{"people": true})

	if dto.People == nil {
		t.Error("People should be non-nil when requested")
	}
	if dto.Chapters != nil {
		t.Errorf("Chapters should be nil when not requested; got %v", dto.Chapters)
	}
	if dto.MediaStreams != nil {
		t.Errorf("MediaStreams should be nil when not requested; got %v", dto.MediaStreams)
	}
	if dto.MediaSources != nil {
		t.Errorf("MediaSources should be nil when not requested; got %v", dto.MediaSources)
	}
}

// TestStubDetailListFields_NilOrEmptyFieldsIsNoop verifies the helper does
// nothing when the request didn't include any Fields= parameter. This is the
// common case for clients that just want a basic list.
func TestStubDetailListFields_NilOrEmptyFieldsIsNoop(t *testing.T) {
	dto := baseItemDTO{}
	stubDetailListFields(&dto, nil)
	if dto.People != nil || dto.Chapters != nil || dto.MediaStreams != nil || dto.MediaSources != nil {
		t.Error("nil fields map should not populate any detail field")
	}

	stubDetailListFields(&dto, map[string]bool{})
	if dto.People != nil || dto.Chapters != nil || dto.MediaStreams != nil || dto.MediaSources != nil {
		t.Error("empty fields map should not populate any detail field")
	}
}

// TestStubDetailListFields_PreservesNonNilExistingValues verifies the helper
// does not clobber values already present on the DTO. The list mapper does not
// populate these four fields today, but a future caller may.
func TestStubDetailListFields_PreservesNonNilExistingValues(t *testing.T) {
	existingPeople := []personDTO{{ID: "p1"}, {ID: "p2"}}
	existingChapters := []map[string]any{{"Name": "Cold Open"}}
	existingStreams := []mediaStreamDTO{{Index: 7}}
	existingSources := []mediaSourceDTO{{ID: "src1"}}

	dto := baseItemDTO{
		People:       existingPeople,
		Chapters:     existingChapters,
		MediaStreams: existingStreams,
		MediaSources: existingSources,
	}
	stubDetailListFields(&dto, map[string]bool{
		"people":       true,
		"chapters":     true,
		"mediastreams": true,
		"mediasources": true,
	})

	if len(dto.People) != 2 || dto.People[0].ID != "p1" {
		t.Errorf("existing People clobbered: %v", dto.People)
	}
	if len(dto.Chapters) != 1 {
		t.Errorf("existing Chapters clobbered: %v", dto.Chapters)
	}
	if len(dto.MediaStreams) != 1 || dto.MediaStreams[0].Index != 7 {
		t.Errorf("existing MediaStreams clobbered: %v", dto.MediaStreams)
	}
	if len(dto.MediaSources) != 1 || dto.MediaSources[0].ID != "src1" {
		t.Errorf("existing MediaSources clobbered: %v", dto.MediaSources)
	}
}

// TestStubDetailListFields_JSONShapeKeepsFieldsPresent locks in the wire
// shape clients see: each requested detail field is present in the JSON as a
// non-empty array. Regression guard against accidentally reverting to empty
// slices (which would be omitted under omitempty).
func TestStubDetailListFields_JSONShapeKeepsFieldsPresent(t *testing.T) {
	dto := baseItemDTO{}
	stubDetailListFields(&dto, map[string]bool{
		"people":       true,
		"chapters":     true,
		"mediastreams": true,
		"mediasources": true,
	})

	encoded, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	out := string(encoded)
	for _, key := range []string{`"People":[`, `"Chapters":[`, `"MediaStreams":[`, `"MediaSources":[`} {
		if !strings.Contains(out, key) {
			t.Errorf("expected JSON to contain %s; got %s", key, out)
		}
	}
}

// TestStubDetailListFields_FreshChapterMapPerCall guards against accidental
// sharing of the chapter map across responses. Chapters is `[]map[string]any`
// — maps are reference-typed in Go, so a shared package-level value would let
// downstream code mutate every Resume response's chapter at once.
func TestStubDetailListFields_FreshChapterMapPerCall(t *testing.T) {
	a := baseItemDTO{}
	b := baseItemDTO{}
	fields := map[string]bool{"chapters": true}

	stubDetailListFields(&a, fields)
	stubDetailListFields(&b, fields)

	if len(a.Chapters) != 1 || len(b.Chapters) != 1 {
		t.Fatalf("expected one chapter per dto; got a=%v b=%v", a.Chapters, b.Chapters)
	}
	a.Chapters[0]["Name"] = "mutated-by-a"
	if got := b.Chapters[0]["Name"]; got != "" {
		t.Errorf("mutating dto a's chapter map leaked into dto b: b.Chapters[0][Name] = %v", got)
	}
}

// TestStubDetailListFields_MediaSourceStubLooksPlayable locks in that the
// MediaSources stub advertises playability. Some clients (SenPlayer) decide
// whether to render a Resume row entry from the listing's MediaSources; an
// all-false playability stub made them drop every entry (the Continue
// Watching row showed empty while item-page resume worked fine).
func TestStubDetailListFields_MediaSourceStubLooksPlayable(t *testing.T) {
	dto := baseItemDTO{Name: "Pilot", RunTimeTicks: 34800000000}
	stubDetailListFields(&dto, map[string]bool{"mediasources": true})

	if len(dto.MediaSources) != 1 {
		t.Fatalf("MediaSources = %v, want single-element slice", dto.MediaSources)
	}
	src := dto.MediaSources[0]
	if !src.SupportsDirectPlay || !src.SupportsDirectStream || !src.SupportsTranscoding || !src.SupportsProbing {
		t.Errorf("stub source must advertise playability; got %+v", src)
	}
	if src.ID != "0" {
		t.Errorf("stub source ID = %q, want \"0\" (never a registered media source owner)", src.ID)
	}
	if src.Name != "Pilot" || src.RunTimeTicks != 34800000000 {
		t.Errorf("stub source should mirror item Name/RunTimeTicks; got Name=%q ticks=%d", src.Name, src.RunTimeTicks)
	}
	if len(src.MediaStreams) != 1 {
		t.Errorf("stub source should nest a stream stub; got %v", src.MediaStreams)
	}
	// Null arrays (vs empty) break strict client deserializers — real servers
	// always serialize these as []/{}.
	if src.Formats == nil || src.RequiredHTTPHeaders == nil || src.MediaAttachments == nil {
		t.Errorf("stub source must not serialize null collections; got Formats=%v RequiredHTTPHeaders=%v MediaAttachments=%v",
			src.Formats, src.RequiredHTTPHeaders, src.MediaAttachments)
	}
}

// TestStubDetailListFields_FreshMediaSourcePerCall guards against reference
// sharing across responses: the stub is built by copying the package-level
// stubDetailMediaSource value, which only stays safe while that var's
// reference-typed fields (slices/maps) remain nil. Mirrors the existing
// chapter-map guard.
func TestStubDetailListFields_FreshMediaSourcePerCall(t *testing.T) {
	a := baseItemDTO{}
	b := baseItemDTO{}
	fields := map[string]bool{"mediasources": true}

	stubDetailListFields(&a, fields)
	stubDetailListFields(&b, fields)

	a.MediaSources[0].RequiredHTTPHeaders["X-Mutated"] = "by-a"
	a.MediaSources[0].MediaStreams[0].Index = 99
	if len(b.MediaSources[0].RequiredHTTPHeaders) != 0 {
		t.Error("mutating dto a's RequiredHTTPHeaders leaked into dto b")
	}
	if b.MediaSources[0].MediaStreams[0].Index == 99 {
		t.Error("mutating dto a's nested stream leaked into dto b")
	}
}
