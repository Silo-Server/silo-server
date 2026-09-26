package contract

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestNativeSubtitleFixtureHasConsistentTrackIdentity(t *testing.T) {
	var response playback.DecisionResponseV3
	data := mustReadFile(t, filepath.Join(schemaRootV3, "v3", "fixtures", "valid", "native-decision_response.json"))
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	plan := response.PlaybackPlan
	if plan == nil || plan.Subtitle.Embedded == nil || plan.SelectedTracks.Subtitle == nil || plan.SelectedTracks.Subtitle.Index == nil {
		t.Fatal("native fixture must select an exact subtitle identity and index")
	}
	if plan.Source.Container != "mp4" || plan.Stream.Container != plan.Source.Container || plan.Subtitle.Embedded.ContainerTrackID != "4" {
		t.Fatal("native fixture must preserve its MP4 container and probed track ID")
	}
	selected := plan.SelectedTracks.Subtitle
	var item *playback.SubtitleInventoryItemV3
	for i := range plan.Subtitle.Inventory {
		candidate := &plan.Subtitle.Inventory[i]
		if candidate.TrackID == selected.ID && candidate.CombinedIndex == *selected.Index {
			item = candidate
			break
		}
	}
	if item == nil || item.Source != playback.SubtitleSourceEmbeddedV3 || item.Codec != "mov_text" || item.TrackID != plan.Subtitle.TrackID {
		t.Fatal("native fixture selection must resolve to the same embedded inventory row")
	}
	pinnedURL, err := url.Parse(item.URL)
	if err != nil || pinnedURL.Query().Get(playback.EmbeddedSubtitleStreamIndexParamV3) != strconv.Itoa(plan.Subtitle.Embedded.StreamIndex) {
		t.Fatalf("native identity must match the selected row's extraction pin: %s", item.URL)
	}
	if plan.Delivery != playback.DeliveryOriginalHTTPV3 || plan.Subtitle.Mode != playback.SubtitleRenderV3 || plan.Subtitle.Artifact != nil {
		t.Fatal("native fixture must render from original media without a sidecar artifact")
	}
	if want := playback.DeterministicPlanIDV3("attempt-golden-0001", plan.RequestedMediaFileID, plan.EffectiveMediaFileID, *plan); plan.PlanID != want {
		t.Errorf("native plan ID = %s, want %s", plan.PlanID, want)
	}
	if want := playback.PlanAttemptKeyV3(*plan, "7", nil); plan.PlanAttemptKey != want {
		t.Errorf("native attempt key = %s, want %s", plan.PlanAttemptKey, want)
	}
}

// A negative vector must differ only at the field whose schema rule it tests.
// Otherwise an unrelated invalid selection can mask the intended violation.
func TestNativeSubtitleNegativeFixturesIsolateTheirViolation(t *testing.T) {
	root := filepath.Join(schemaRootV3, "v3", "fixtures")
	valid := decodeJSONValue(t, mustReadFile(t, filepath.Join(root, "valid", "native-decision_response.json")))
	for _, kind := range []string{"and-artifact", "convert", "negative-index", "remux"} {
		t.Run(kind, func(t *testing.T) {
			value := decodeJSONValue(t, mustReadFile(t, filepath.Join(root, "invalid", "native-"+kind+"-decision_response.json")))
			plan := value.(map[string]any)["playback_plan"].(map[string]any)
			subtitle := plan["subtitle"].(map[string]any)
			switch kind {
			case "and-artifact":
				delete(subtitle, "artifact")
			case "convert":
				subtitle["mode"] = "render"
			case "negative-index":
				subtitle["embedded"].(map[string]any)["stream_index"] = float64(3)
			case "remux":
				plan["delivery"] = "original_http"
			}
			if !reflect.DeepEqual(value, valid) {
				t.Fatal("negative native fixture contains unrelated changes from its valid source")
			}
		})
	}
}
