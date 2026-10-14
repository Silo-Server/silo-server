package playback_test

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestSessionManagerConfirmedSampleFencesEqualPositionRollback(t *testing.T) {
	for _, paused := range []bool{false, true} {
		for _, sequence := range []int64{3, 2, 1, 0} {
			name := map[bool]string{false: "playing", true: "paused"}[paused] + map[int64]string{3: "-newer", 2: "-same", 1: "-older", 0: "-missing"}[sequence]
			t.Run(name, func(t *testing.T) {
				manager := playback.NewSessionManager(0, 0)
				session, err := manager.StartSession(1, "profile", 100, playback.PlayDirect, false)
				if err != nil {
					t.Fatal(err)
				}
				if err := manager.UpdateProgressSample(session.ID, playback.ProgressSampleV3{Sequence: 1, Position: 30, IsPaused: paused}); err != nil {
					t.Fatal(err)
				}
				position := 321.0
				rollback, err := manager.ApplyReplacement(session.ID, playback.SessionReplacement{EffectiveMediaFileID: 100, PositionSeconds: &position, PositionSequence: 2, PreservePaused: true})
				if err != nil {
					t.Fatal(err)
				}
				var latest *playback.ProgressSampleV3
				if sequence > 0 {
					latest = &playback.ProgressSampleV3{Sequence: sequence, Position: position, IsPaused: paused}
					if sequence <= 2 {
						latest.Position = 30
					}
				}
				// A newer accepted observation may equal the speculative seek's
				// displayed values. It still supersedes that seek's rollback,
				// without reporting another pause transition to its caller.
				if changed, err := manager.SetPaused(session.ID, paused, 3, latest); err != nil || changed {
					t.Fatal("unchanged pause emitted a transition", err)
				}
				current, err := manager.GetSession(session.ID)
				if err != nil || current.Position != position {
					t.Fatal("confirmation rewound the later seek", err)
				}
				err = manager.RollbackReplacement(session.ID, rollback)
				if sequence > 2 {
					if !errors.Is(err, playback.ErrSessionReplacementSuperseded) {
						t.Fatal("newer equal-valued sample did not fence the failed seek rollback", err)
					}
				} else if err != nil {
					t.Fatal("same, older or absent sample superseded the existing rollback", err)
				}
				current, err = manager.GetSession(session.ID)
				if err != nil || current.IsPaused != paused || sequence > 2 && current.Position != position || sequence <= 2 && current.Position != 30 {
					t.Fatal("rollback lost the authoritative observation", err)
				}
			})
		}
	}
}
