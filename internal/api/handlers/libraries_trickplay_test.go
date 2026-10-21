package handlers

import (
	"context"
	"strings"
	"testing"
)

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
