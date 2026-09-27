package playback

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestCompatExpiryRechecksLocalActivityAfterSharedRead(t *testing.T) {
	manager := NewSessionManager(0, 0)
	session, err := manager.StartSession(1, "profile", 1, PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	session.IsJellyfinCompat = true
	old := time.Now().Add(-time.Hour)
	session.LastActivityAt = old
	manager.SetCompatActivityReader(func(context.Context, []Session) (map[string]time.Time, error) {
		// This call proves the shared reader runs outside the manager lock.
		if err := manager.UpdateProgress(session.ID, 42, true); err != nil {
			t.Fatal(err)
		}
		return map[string]time.Time{session.ID: old}, nil
	})
	manager.SetCompatExpiryClaimer(func(context.Context, []SessionExpiryCandidate) (map[string]bool, error) {
		t.Fatal("claimed newly active session")
		return nil, nil
	})
	if expired := manager.CleanInactive(time.Minute, time.Minute); len(expired) != 0 {
		t.Fatal("expired concurrent local activity")
	}
	got, err := manager.GetSession(session.ID)
	if err != nil || got.Position != 42 || !got.IsPaused || !got.LastActivityAt.After(old) {
		t.Fatalf("session=%+v err=%v", got, err)
	}
}

func TestCompatExpiryClaimIsBoundedAndPreservesOverflow(t *testing.T) {
	manager := NewSessionManager(0, 0)
	for range 257 {
		session, err := manager.StartSession(1, "profile", 1, PlayDirect, false)
		if err != nil {
			t.Fatal(err)
		}
		session.IsJellyfinCompat = true
		session.LastActivityAt = time.Now().Add(-time.Hour)
	}
	manager.SetCompatExpiryClaimer(func(ctx context.Context, candidates []SessionExpiryCandidate) (map[string]bool, error) {
		if len(candidates) != 256 {
			t.Fatalf("batch=%d", len(candidates))
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 250*time.Millisecond {
			t.Fatal("missing bounded claim deadline")
		}
		result := make(map[string]bool, len(candidates))
		for _, candidate := range candidates {
			result[candidate.ID] = true
		}
		return result, nil
	})
	if expired := manager.CleanInactive(time.Minute, time.Minute); len(expired) != 256 {
		t.Fatalf("expired=%d", len(expired))
	}
	if len(manager.AllSessions()) != 1 {
		t.Fatal("unclaimed overflow session removed")
	}
}

func TestDurableCompatRetainsStreamAdmissionUntilExpiry(t *testing.T) {
	for _, tc := range []struct {
		name           string
		compat, shared bool
		wantBlocked    bool
	}{
		{"durable compatibility", true, true, true},
		{"native session", false, true, false},
		{"local compatibility", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewSessionManager(1, 0)
			session, err := manager.StartSession(1, "profile", 1, PlayDirect, false)
			if err != nil {
				t.Fatal(err)
			}
			session.IsJellyfinCompat = tc.compat
			session.LastActivityAt = time.Now().Add(-time.Hour)
			if tc.shared {
				manager.SetCompatExpiryClaimer(func(context.Context, []SessionExpiryCandidate) (map[string]bool, error) {
					return map[string]bool{session.ID: true}, nil
				})
			}
			_, err = manager.StartSession(1, "other-profile", 2, PlayDirect, false)
			if tc.wantBlocked {
				if !errors.Is(err, ErrTooManyStreams) {
					t.Fatalf("start=%v", err)
				}
				if _, err := manager.RegisterReconstructedWithLimits(t.Context(), &Session{ID: "reconstructed", UserID: 1, ProfileID: "profile", PlayMethod: PlayDirect}); !errors.Is(err, ErrTooManyStreams) {
					t.Fatalf("reconstruct=%v", err)
				}
				if expired := manager.CleanInactive(time.Minute, time.Minute); len(expired) != 1 {
					t.Fatalf("expired=%d", len(expired))
				}
				if _, err := manager.StartSession(1, "profile", 2, PlayDirect, false); err != nil {
					t.Fatalf("slot not released after durable expiry: %v", err)
				}
			} else if err != nil {
				t.Fatalf("legacy/native admission changed: %v", err)
			}
		})
	}
}

func TestDurableCompatRetainsTranscodeAndReplacementCapacity(t *testing.T) {
	for _, reserved := range []bool{false, true} {
		t.Run(fmt.Sprint("reservation=", reserved), func(t *testing.T) {
			manager := NewSessionManager(0, 1)
			method := PlayTranscode
			if reserved {
				method = PlayDirect
			}
			compat, err := manager.StartSession(1, "profile", 1, method, false)
			if err != nil {
				t.Fatal(err)
			}
			compat.IsJellyfinCompat = true
			if reserved {
				if err := manager.CheckReplacementAllowed(t.Context(), compat.ID, PlayTranscode, false); err != nil {
					t.Fatal(err)
				}
			}
			compat.LastActivityAt = time.Now().Add(-time.Hour)
			claimErr := errors.New("shared store unavailable")
			manager.SetCompatExpiryClaimer(func(context.Context, []SessionExpiryCandidate) (map[string]bool, error) {
				return map[string]bool{compat.ID: true}, claimErr
			})
			direct, err := manager.StartSession(1, "profile", 2, PlayDirect, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.StartSession(1, "profile", 3, PlayTranscode, false); !errors.Is(err, ErrTooManyTranscodes) {
				t.Fatalf("transcode start=%v", err)
			}
			if err := manager.CheckReplacementAllowed(t.Context(), direct.ID, PlayTranscode, false); !errors.Is(err, ErrTooManyTranscodes) {
				t.Fatalf("replacement=%v", err)
			}
			// The retained session still owns its own slot when replacing itself.
			if err := manager.CheckReplacementAllowed(t.Context(), compat.ID, PlayTranscode, false); err != nil {
				t.Fatalf("self replacement=%v", err)
			}
			if expired := manager.CleanInactive(time.Minute, time.Minute); len(expired) != 0 {
				t.Fatal("failed claim removed session")
			}
			if manager.TranscodeCount(1) != 1 {
				t.Fatal("failed claim freed capacity")
			}
			claimErr = nil
			if expired := manager.CleanInactive(time.Minute, time.Minute); len(expired) != 1 {
				t.Fatalf("expired=%d", len(expired))
			}
			if err := manager.CheckReplacementAllowed(t.Context(), direct.ID, PlayTranscode, false); err != nil {
				t.Fatalf("capacity not released: %v", err)
			}
		})
	}
}
