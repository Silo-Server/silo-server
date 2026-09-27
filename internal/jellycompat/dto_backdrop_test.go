package jellycompat

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestBaseItemDTOAlwaysSerializesBackdropImageTags covers #1097: Roku clients
// crash when BackdropImageTags is missing, so an item without backdrops, or
// one whose backdrops were cleared by an image filter, must still send [].
func TestBaseItemDTOAlwaysSerializesBackdropImageTags(t *testing.T) {
	cleared := []baseItemDTO{{ID: "a", BackdropImageTags: []string{"b"}}}
	applyImageTypeLimit(cleared, new(int))

	for name, item := range map[string]baseItemDTO{
		"nil":     {ID: "a"},
		"empty":   {ID: "a", BackdropImageTags: []string{}},
		"cleared": cleared[0],
	} {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), `"BackdropImageTags":[]`) {
				t.Fatalf("BackdropImageTags missing or not an empty array: %s", body)
			}
		})
	}

	body, err := json.Marshal([]*baseItemDTO{{ID: "a", BackdropImageTags: []string{"tag"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"BackdropImageTags":["tag"]`) || !strings.Contains(string(body), `"Id":"a"`) {
		t.Fatalf("backdrop tags or other fields lost through a pointer: %s", body)
	}
}
