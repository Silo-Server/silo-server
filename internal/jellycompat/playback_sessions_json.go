package jellycompat

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
)

// Compat playback sessions are shared across processes as one JSONB document,
// and every durable mutation is a read-modify-write of that whole document
// (updateDB / stageTerminalDB). During a rolling deployment the process
// applying a mutation may be older than the process that negotiated the
// session, so any field the older binary's struct does not declare would be
// silently erased on the rewrite — e.g. a route-version flag whose loss makes
// every URL the client is actively playing 404. The custom JSON round-trip
// below captures keys this binary does not recognize and re-emits them
// verbatim, so a document only ever loses fields to a binary that predates
// this envelope.
//
// The envelope covers the jellycompat-owned structs. Fields added to nested
// foreign types (catalog.FileVersion, playback.RecipeCard) still need their
// own compatibility story.

type playbackSessionJSON PlaybackSession
type playbackMediaSourceJSON PlaybackMediaSource

var playbackSessionKnownJSONKeys = sync.OnceValue(func() map[string]struct{} {
	return knownJSONKeys(reflect.TypeFor[playbackSessionJSON]())
})

var playbackMediaSourceKnownJSONKeys = sync.OnceValue(func() map[string]struct{} {
	return knownJSONKeys(reflect.TypeFor[playbackMediaSourceJSON]())
})

func (s *PlaybackSession) UnmarshalJSON(data []byte) error {
	var known playbackSessionJSON
	if err := json.Unmarshal(data, &known); err != nil {
		return err
	}
	*s = PlaybackSession(known)
	s.preservedJSON = unknownJSONFields(data, playbackSessionKnownJSONKeys())
	return nil
}

func (s PlaybackSession) MarshalJSON() ([]byte, error) {
	return marshalWithPreservedJSON(playbackSessionJSON(s), s.preservedJSON)
}

func (s *PlaybackMediaSource) UnmarshalJSON(data []byte) error {
	var known playbackMediaSourceJSON
	if err := json.Unmarshal(data, &known); err != nil {
		return err
	}
	*s = PlaybackMediaSource(known)
	s.preservedJSON = unknownJSONFields(data, playbackMediaSourceKnownJSONKeys())
	return nil
}

func (s PlaybackMediaSource) MarshalJSON() ([]byte, error) {
	return marshalWithPreservedJSON(playbackMediaSourceJSON(s), s.preservedJSON)
}

// knownJSONKeys lists the JSON object keys the given struct type produces, so
// unknownJSONFields can subtract them. The session structs use field names as
// keys today; the json-tag parsing keeps the subtraction correct if a tag is
// ever added.
func knownJSONKeys(t reflect.Type) map[string]struct{} {
	keys := make(map[string]struct{}, t.NumField())
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		name := field.Name
		if tag, _, _ := strings.Cut(field.Tag.Get("json"), ","); tag == "-" {
			continue
		} else if tag != "" {
			name = tag
		}
		keys[name] = struct{}{}
	}
	return keys
}

func unknownJSONFields(data []byte, known map[string]struct{}) map[string]json.RawMessage {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	for key := range raw {
		if _, ok := known[key]; ok {
			delete(raw, key)
		}
	}
	if len(raw) == 0 {
		return nil
	}
	return raw
}

func marshalWithPreservedJSON(known any, preserved map[string]json.RawMessage) ([]byte, error) {
	data, err := json.Marshal(known)
	if err != nil || len(preserved) == 0 {
		return data, err
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(data, &merged); err != nil {
		return nil, err
	}
	// This binary's fields are authoritative; preserved keys are by
	// construction unknown here, so a collision can only mean the document was
	// written by a newer schema this binary also declares — let the declared
	// field win.
	for key, value := range preserved {
		if _, exists := merged[key]; !exists {
			merged[key] = value
		}
	}
	return json.Marshal(merged)
}
