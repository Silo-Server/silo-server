package playback

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMemoryRouteSessionPreservesAttemptAndRejectsStaleMoves(t *testing.T) {
	for _, change := range []string{"current", "local", "attempt", "plan", "replan", "node", "transport", "owner", "profile", "file", "expired", "stopped"} {
		t.Run(change, func(t *testing.T) {
			store := NewMemoryPlanStoreV3()
			record := AttemptRecordV3{SessionID: "session", PlaybackAttemptID: "attempt", UserID: 1, ProfileID: "profile", EffectiveMediaFileID: 42, CurrentPlanID: "plan", CurrentReplanRequestID: "replan", ExpiresAt: time.Now().Add(time.Hour), FrozenRecipe: ExecutableRecipeV3{Version: 1, PlanID: "plan", TranscodeNodeURL: "https://old", TranscodeTransportID: "transport"}}
			if change == "expired" {
				record.ExpiresAt = time.Now().Add(-time.Hour)
			}
			if err := store.SaveAttempt(t.Context(), record); err != nil {
				t.Fatal(err)
			}
			store.mu.Lock()
			stored := store.attempts[record.PlaybackAttemptID]
			stored.LastSequence, stored.LastSample = 11, &ProgressSampleV3{Sequence: 11, Position: 123, IsPaused: true}
			stored.LastSampleAt = time.Now()
			store.attempts[record.PlaybackAttemptID] = stored
			store.mu.Unlock()
			if change == "stopped" {
				if _, _, err := store.StopAttempt(t.Context(), record.SessionID, uuid.NewString(), nil); err != nil {
					t.Fatal(err)
				}
			}
			capability, ok := any(store).(RouteSessionStoreV3)
			if !ok {
				t.Fatal("memory plan store cannot own a route session")
			}
			owned, err := capability.AcquireRouteSession(t.Context(), record.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			defer owned.Close()
			fresh, err := owned.GetAttempt(t.Context())
			if err != nil || fresh == nil {
				t.Fatalf("raw authoritative presence lost expired/stopped row: %v", err)
			}
			expected := *fresh
			switch change {
			case "attempt":
				expected.PlaybackAttemptID = "other"
			case "plan":
				expected.CurrentPlanID = "other"
			case "replan":
				expected.CurrentReplanRequestID = "other"
			case "node":
				expected.FrozenRecipe.TranscodeNodeURL = "https://other"
			case "transport":
				expected.FrozenRecipe.TranscodeTransportID = "other"
			case "owner":
				expected.UserID++
			case "profile":
				expected.ProfileID = "other"
			case "file":
				expected.EffectiveMediaFileID++
			}
			next := TranscodeRoute{NodeURL: "https://new", TransportID: "transport"}
			if change == "local" {
				next.NodeURL = ""
			}
			moved, err := owned.CompareAndSwap(t.Context(), expected, next)
			if err != nil {
				t.Fatal(err)
			}
			wantMove := change == "current" || change == "local"
			if moved != wantMove {
				t.Fatalf("moved=%v want=%v", moved, wantMove)
			}
			after, err := owned.GetAttempt(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			want := *fresh
			if wantMove {
				want.FrozenRecipe.TranscodeNodeURL, want.FrozenRecipe.TranscodeTransportID = next.NodeURL, next.TransportID
			}
			beforeJSON, _ := json.Marshal(want)
			afterJSON, _ := json.Marshal(after)
			if string(beforeJSON) != string(afterJSON) {
				t.Fatalf("route CAS changed other durable fields:\nwant%s\ngot%s", beforeJSON, afterJSON)
			}
		})
	}
}

func TestMemoryRouteSessionSharesCancellableReplanLock(t *testing.T) {
	store := NewMemoryPlanStoreV3()
	capability, ok := any(store).(RouteSessionStoreV3)
	if !ok {
		t.Fatal("memory plan store cannot own a route session")
	}
	owned, err := capability.AcquireRouteSession(t.Context(), "session")
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	if _, err = owned.GetAttempt(t.Context()); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("absent attempt: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if release, err := store.AcquireSessionLock(ctx, "session"); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			release()
		}
		t.Fatalf("shared store replan lock bypassed move/cancellation: %v", err)
	}
	release, err := store.AcquireSessionLock(t.Context(), "other")
	if err != nil {
		t.Fatal(err)
	}
	release()
	owned.Close()
	release, err = store.AcquireSessionLock(t.Context(), "session")
	if err != nil {
		t.Fatal(err)
	}
	release()
}
