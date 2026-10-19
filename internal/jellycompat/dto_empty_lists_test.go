package jellycompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

// TestItemListFieldsSendEmptyArraysWhenFilled covers the rest of #1097.
// Jellyfin leaves out null fields but sends empty arrays, and Roku clients
// index some of these arrays without checking for them. A list field the
// response fills is [] when the item has none; one it doesn't fill is absent.
func TestItemListFieldsSendEmptyArraysWhenFilled(t *testing.T) {
	m := newMapper(NewResourceIDCodec(), &config.Config{})
	item := upstreamListItem{ContentID: "movie-1", Type: "movie", Title: "No Lists"}

	requested := marshalItemJSON(t, m.itemFromList(item, false, nil, map[string]bool{"genres": true, "tags": true, "studios": true, "taglines": true}))
	for _, want := range []string{`"Genres":[]`, `"GenreItems":[]`, `"Tags":[]`, `"Studios":[]`, `"Taglines":[]`} {
		if !strings.Contains(requested, want) {
			t.Errorf("requested list field missing %s: %s", want, requested)
		}
	}

	unrequested := marshalItemJSON(t, m.itemFromList(item, false, nil, nil))
	for _, key := range []string{`"Genres"`, `"GenreItems"`, `"Tags"`, `"Studios"`, `"People"`, `"MediaSources"`} {
		if strings.Contains(unrequested, key) {
			t.Errorf("unrequested field %s present: %s", key, unrequested)
		}
	}

	detail := upstreamItemDetail{ContentID: "movie-1", Type: "movie", Title: "No Lists"}
	withPeople := marshalItemJSON(t, m.itemFromDetailWithFields(detail, false, nil, map[string]bool{"people": true}))
	if !strings.Contains(withPeople, `"People":[]`) {
		t.Errorf("requested People missing or not []: %s", withPeople)
	}

	// List endpoints that need detail data map every field, then keep only
	// the empty lists the request asked for.
	sourcesOnly := marshalItemJSON(t, m.itemFromDetailWithFields(detail, false, nil, map[string]bool{"mediasources": true}))
	if !strings.Contains(sourcesOnly, `"MediaSources":[]`) {
		t.Errorf("requested MediaSources on a movie without versions missing or not []: %s", sourcesOnly)
	}
	series := upstreamItemDetail{ContentID: "series-1", Type: "series", Title: "No Lists"}
	if got := marshalItemJSON(t, m.itemFromDetailWithFields(series, false, nil, map[string]bool{"mediasources": true})); strings.Contains(got, `"MediaSources"`) {
		t.Errorf("series has MediaSources: %s", got)
	}
	for _, key := range []string{`"MediaStreams"`, `"Chapters"`, `"Genres"`, `"GenreItems"`, `"Studios"`, `"Tags"`, `"Taglines"`,
		`"ProductionLocations"`, `"ExternalUrls"`, `"RemoteTrailers"`, `"LockedFields"`, `"People"`} {
		if strings.Contains(sourcesOnly, key) {
			t.Errorf("unrequested empty field %s present on the detail path: %s", key, sourcesOnly)
		}
	}

	// A full detail response still carries every list field.
	full := marshalItemJSON(t, m.itemFromDetail(detail, false, nil))
	for _, want := range []string{`"MediaSources":[]`, `"MediaStreams":[]`, `"Chapters":[]`, `"Tags":[]`, `"LockedFields":[]`, `"People":[]`} {
		if !strings.Contains(full, want) {
			t.Errorf("full detail missing %s: %s", want, full)
		}
	}
}

func marshalItemJSON(t *testing.T, item baseItemDTO) string {
	t.Helper()
	body, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
