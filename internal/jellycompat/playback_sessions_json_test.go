package jellycompat

import (
	"encoding/json"
	"testing"
)

func TestPlaybackSessionJSONPreservesUnknownFieldsAcrossRewrite(t *testing.T) {
	// A document written by a newer binary: this binary knows neither the
	// top-level key nor the per-source key. A read-modify-write must carry
	// both through verbatim, or a rolling deployment erases the newer
	// generation's negotiation state.
	doc := []byte(`{
		"ID": "play-1",
		"CompatToken": "token-1",
		"FutureSessionField": {"nested": true},
		"MediaSources": [{
			"ID": "source-1",
			"FileID": 42,
			"TranscodeAudio": false,
			"FutureSourceField": [1, 2, 3]
		}]
	}`)

	var session PlaybackSession
	if err := json.Unmarshal(doc, &session); err != nil {
		t.Fatal(err)
	}
	if session.ID != "play-1" || len(session.MediaSources) != 1 || session.MediaSources[0].FileID != 42 {
		t.Fatalf("known fields did not decode: %+v", session)
	}

	// The mutation an older binary would apply mid-deploy.
	session.MediaSources[0].SelectedAudioStreamIndex = intPtr(2)

	rewritten, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rewritten, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["FutureSessionField"]) != `{"nested":true}` {
		t.Fatalf("top-level unknown field lost or altered: %s", raw["FutureSessionField"])
	}
	var sources []map[string]json.RawMessage
	if err := json.Unmarshal(raw["MediaSources"], &sources); err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || string(sources[0]["FutureSourceField"]) != `[1,2,3]` {
		t.Fatalf("per-source unknown field lost or altered: %v", sources)
	}
	if string(sources[0]["SelectedAudioStreamIndex"]) != `2` {
		t.Fatalf("mutation not applied: %s", sources[0]["SelectedAudioStreamIndex"])
	}
}

func TestPlaybackSessionJSONKnownFieldsShadowStalePreservedCopies(t *testing.T) {
	doc := []byte(`{"ID": "play-1", "TranscodeStarted": true}`)
	var session PlaybackSession
	if err := json.Unmarshal(doc, &session); err != nil {
		t.Fatal(err)
	}
	session.TranscodeStarted = false

	rewritten, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rewritten, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["TranscodeStarted"]) != `false` {
		t.Fatalf("declared field lost to a stale raw copy: %s", raw["TranscodeStarted"])
	}
}
