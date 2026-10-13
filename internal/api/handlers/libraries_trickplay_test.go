package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// The trickplay switch is a v2-only member. The frozen /api/v1 library
// bodies must neither read nor write it.
func TestV1LibraryBodiesOmitTrickplay(t *testing.T) {
	var create createLibraryRequest
	if err := json.Unmarshal([]byte(`{"paths":["/m"],"type":"movies","name":"M","trickplay_enabled":true}`), &create); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if create.TrickplayEnabled {
		t.Fatal("v1 create decoded trickplay_enabled")
	}
	var update updateLibraryRequest
	if err := json.Unmarshal([]byte(`{"name":"M","trickplay_enabled":true}`), &update); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if update.TrickplayEnabled != nil {
		t.Fatalf("v1 update decoded trickplay_enabled = %v, want nil", *update.TrickplayEnabled)
	}
	h := &LibraryHandler{}
	view := h.toLibraryResponseWithPoster(context.Background(), &models.MediaFolder{ID: 1, Name: "M", TrickplayEnabled: true})
	if !view.TrickplayEnabled || view.TrickplaySupported {
		t.Fatalf("view %+v, want enabled and unsupported without artwork storage", view)
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("encode view: %v", err)
	}
	if strings.Contains(string(raw), "trickplay") {
		t.Fatalf("v1 library response carries trickplay: %s", raw)
	}
}

type countingReconciler struct{ calls int }

func (c *countingReconciler) ReconcileSoon() { c.calls++ }

// Turning seek previews on needs artwork storage, like chapter thumbnails.
func TestLibraryTrickplayRequiresArtworkStorage(t *testing.T) {
	h := &LibraryHandler{}
	_, err := h.CreateLibrary(context.Background(), LibraryCreateRequest{Paths: []string{"/m"}, Type: "movies", Name: "M", TrickplayEnabled: true})
	if err == nil || !strings.Contains(err.Error(), "artwork storage") {
		t.Fatalf("create error %v, want the storage requirement", err)
	}
	on := true
	_, err = h.UpdateLibrary(context.Background(), 1, 1, LibraryUpdateRequest{TrickplayEnabled: &on})
	if err == nil || !strings.Contains(err.Error(), "artwork storage") {
		t.Fatalf("update error %v, want the storage requirement", err)
	}
	reconciler := &countingReconciler{}
	h.Trickplay = reconciler
	h.reconcileTrickplay()
	if reconciler.calls != 1 {
		t.Fatalf("reconciled %d times", reconciler.calls)
	}
}

// An update reconciles seek previews when it can change which files get
// them, including enabling or disabling the library.
func TestLibraryUpdateAffectsTrickplay(t *testing.T) {
	yes, kind := true, "audiobooks"
	for _, tt := range []struct {
		name string
		req  updateLibraryRequest
		want bool
	}{
		{"setting", updateLibraryRequest{TrickplayEnabled: &yes}, true},
		{"type", updateLibraryRequest{Type: &kind}, true},
		{"enabled", updateLibraryRequest{Enabled: &yes}, true},
		{"unrelated", updateLibraryRequest{IntroDetectionEnabled: &yes}, false},
	} {
		if got := tt.req.affectsTrickplay(); got != tt.want {
			t.Errorf("%s: affectsTrickplay = %v, want %v", tt.name, got, tt.want)
		}
	}
}
