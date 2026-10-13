package workercontrolcore

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestRecipeNodeRequiresValidatedCanonicalOrigin(t *testing.T) {
	node := Node{ID: 17, URL: "https://worker.example"}
	for _, tc := range []struct {
		origin string
		id     int
		match  bool
	}{
		{"https://WORKER.example:443/", 17, true},
		{"https://WORKER.example:443/", 0, true},
		{"https://worker.example/", 0, true},
		{"", 17, true},
		{"https://worker.example?query=changed", 17, false},
		{"https://worker.example#fragment", 17, false},
		{"https://user@worker.example", 17, false},
		{"https://worker.example/path", 17, false},
		{"https://worker.example:444", 17, false},
		{"https://worker.example", 18, false},
		{"", 0, false},
	} {
		if matched := recipeNode(playback.RecipeCard{TranscodeNodeURL: tc.origin, RoutingExecutionNodeID: tc.id}, node); matched != tc.match {
			t.Errorf("recipe origin%q id%d matched%v want%v", tc.origin, tc.id, matched, tc.match)
		}
	}
}
