package playback

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMemoryLiveRouteRestorationDoesNotBlockOtherSessions(t *testing.T) {
	store := NewMemoryPlanStoreV3()
	record := AttemptRecordV3{SessionID: "restore-session", PlaybackAttemptID: "restore-attempt", CurrentPlanID: "plan", UserID: 1, ProfileID: "profile", EffectiveMediaFileID: 42, ExpiresAt: time.Now().Add(time.Hour), FrozenRecipe: ExecutableRecipeV3{TranscodeNodeURL: "https://old", TranscodeTransportID: "work"}}
	other := record
	other.SessionID, other.PlaybackAttemptID = "other-session", "other-attempt"
	for _, item := range []AttemptRecordV3{record, other} {
		if err := store.SaveAttempt(t.Context(), item); err != nil {
			t.Fatal(err)
		}
	}
	owned, err := store.AcquireRouteSession(t.Context(), record.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	entered, resume := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	}()
	done := make(chan error, 1)
	go func() {
		_, err := owned.WithLiveRoute(t.Context(), record, func(ctx context.Context) error {
			close(entered)
			select {
			case <-resume:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("restore callback did not begin")
	}
	progress := make(chan error, 1)
	go func() {
		_, err := store.ApplyProgress(t.Context(), other.SessionID, ProgressSampleV3{Sequence: 1, Position: 12})
		if err == nil {
			_, err = store.GetAttempt(t.Context(), other.SessionID)
		}
		progress <- err
	}()
	select {
	case err := <-progress:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("external restore callback held the global store mutex")
	}
	stopCtx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	_, _, err = store.StopAttempt(stopCtx, record.SessionID, uuid.NewString(), nil)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("same-session stop passed active restoration: %v", err)
	}
	close(resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.StopAttempt(t.Context(), record.SessionID, uuid.NewString(), nil); err != nil {
		t.Fatal("restore did not release the row", err)
	}
	called, err := owned.WithLiveRoute(t.Context(), record, func(context.Context) error {
		t.Error("stopped attempt revived its recipe")
		return nil
	})
	if err != nil || called {
		t.Fatalf("stopped route callback: called=%v err=%v", called, err)
	}
}

func TestMemoryLiveRouteRestorationRequiresExactPredecessor(t *testing.T) {
	for _, change := range []string{"attempt", "plan", "replan", "node", "work", "user", "profile", "file", "expired"} {
		t.Run(change, func(t *testing.T) {
			store := NewMemoryPlanStoreV3()
			before := AttemptRecordV3{SessionID: "session", PlaybackAttemptID: "attempt", CurrentPlanID: "plan", CurrentReplanRequestID: "replan", UserID: 1, ProfileID: "profile", EffectiveMediaFileID: 42, ExpiresAt: time.Now().Add(time.Hour), FrozenRecipe: ExecutableRecipeV3{TranscodeNodeURL: "https://old", TranscodeTransportID: "work"}}
			if err := store.SaveAttempt(t.Context(), before); err != nil {
				t.Fatal(err)
			}
			after := before
			switch change {
			case "attempt":
				after.PlaybackAttemptID = "successor"
			case "plan":
				after.CurrentPlanID = "successor"
			case "replan":
				after.CurrentReplanRequestID = "successor"
			case "node":
				after.FrozenRecipe.TranscodeNodeURL = "https://successor"
			case "work":
				after.FrozenRecipe.TranscodeTransportID = "successor"
			case "user":
				after.UserID++
			case "profile":
				after.ProfileID = "successor"
			case "file":
				after.EffectiveMediaFileID++
			case "expired":
				after.ExpiresAt = time.Now().Add(-time.Hour)
			}
			store.ReplaceAttempt(t.Context(), after)
			owned, err := store.AcquireRouteSession(t.Context(), before.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			defer owned.Close()
			called, err := owned.WithLiveRoute(t.Context(), before, func(context.Context) error {
				t.Error("replacement attempt accepted the predecessor callback")
				return nil
			})
			if err != nil || called {
				t.Fatalf("called=%v err=%v", called, err)
			}
		})
	}
}

func TestMemoryLiveRouteRestorationSerializesAttemptAliases(t *testing.T) {
	for _, order := range []string{"replace-first", "save-first"} {
		t.Run(order, func(t *testing.T) {
			store := NewMemoryPlanStoreV3()
			original := AttemptRecordV3{SessionID: "saved-session", PlaybackAttemptID: "shared-attempt", CurrentPlanID: "plan", ExpiresAt: time.Now().Add(time.Hour), FrozenRecipe: ExecutableRecipeV3{TranscodeNodeURL: "https://old", TranscodeTransportID: "work"}}
			replacement := original
			replacement.SessionID = "replacement-session"
			if order == "replace-first" {
				store.ReplaceAttempt(t.Context(), replacement)
				if err := store.SaveAttempt(t.Context(), original); !errors.Is(err, ErrPlaybackAttemptExistsV3) {
					t.Fatalf("Save bypassed an existing alias: %v", err)
				}
			} else {
				if err := store.SaveAttempt(t.Context(), original); err != nil {
					t.Fatal(err)
				}
				owned, err := store.AcquireRouteSession(t.Context(), original.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				defer owned.Close()
				entered, resume := make(chan struct{}), make(chan struct{})
				defer func() {
					select {
					case <-resume:
					default:
						close(resume)
					}
				}()
				done := make(chan error, 1)
				go func() {
					_, err := owned.WithLiveRoute(t.Context(), original, func(ctx context.Context) error {
						close(entered)
						select {
						case <-resume:
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					})
					done <- err
				}()
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("alias route callback did not begin")
				}
				cancelCtx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
				store.ReplaceAttempt(cancelCtx, replacement)
				cancel()
				current, getErr := store.GetAttempt(t.Context(), original.SessionID)
				if getErr != nil || current.SessionID != original.SessionID {
					t.Fatalf("replacement passed the owned alias: current=%v err=%v", current, getErr)
				}
				close(resume)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				store.ReplaceAttempt(t.Context(), replacement)
				called, err := owned.WithLiveRoute(t.Context(), original, func(context.Context) error { t.Error("replaced alias accepted original authority"); return nil })
				if called || !errors.Is(err, ErrSessionNotFound) {
					t.Fatalf("original callback after replacement: called=%v err=%v", called, err)
				}
			}
			owned, err := store.AcquireRouteSession(t.Context(), replacement.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			defer owned.Close()
			called, err := owned.WithLiveRoute(t.Context(), replacement, func(context.Context) error { return nil })
			if !called || err != nil {
				t.Fatalf("replacement lost its route: called=%v err=%v", called, err)
			}
		})
	}
}
