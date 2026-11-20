package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary"
)

// TestExplicitVirtualPinV3ReflectsFileSelection pins the single predicate the
// rotation doors read. An explicit user version pick is a pin; an auto
// selection is not. The decode-rotation demotion hold
// (virtualCandidateRotationPendingV3) and the terminal-driven alternate-file
// hunt read this one function so they cannot disagree about whether a release
// may rotate. A nil record is not a pin.
func TestExplicitVirtualPinV3ReflectsFileSelection(t *testing.T) {
	h := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	auto := &playback.AttemptRecordV3{NormalizedRequest: playback.StartRequestV3{FileSelection: playback.FileSelectionAutoV3}}
	explicit := &playback.AttemptRecordV3{NormalizedRequest: playback.StartRequestV3{FileSelection: playback.FileSelectionExplicitV3}}

	if h.explicitVirtualPinV3(auto) {
		t.Fatal("auto selection reported as an explicit pin")
	}
	if !h.explicitVirtualPinV3(explicit) {
		t.Fatal("explicit version pick not reported as a pin")
	}
	if h.explicitVirtualPinV3(nil) {
		t.Fatal("nil record reported as an explicit pin")
	}
}

// TestRehydrationRotationDistinguishesSameReleaseFromSubstitution pins the
// unified rotation policy's arbiter (issue #245 item 4). The rehydration retry
// is not refused wholesale for an explicit pin: the same-release assertion
// distinguishes an identity-proven provider renumbering — which the transport
// anchor and serve-layer doors already allow, and which must stay allowed for
// both selections so a harmless rotation does not strand playback — from a
// genuinely different release or a weak-identity candidate it cannot prove the
// same release as, both of which are refused on every door. The matrix is
// explicit/auto × renumbered-match / genuinely-different / weak-identity.
func TestRehydrationRotationDistinguishesSameReleaseFromSubstitution(t *testing.T) {
	const (
		neutralURI = "virtual://movie/tt-unified-rotation"
		pinnedURI  = neutralURI + "?result=pinned"
		siblingURI = neutralURI + "?result=sibling"
	)

	cases := []struct {
		name string
		// selection is the attempt's file selection.
		selection playback.FileSelectionV3
		// rowIdentity controls whether the pinned row carries durable provider
		// identity at all (weak-identity means none).
		weakIdentity bool
		// rematched marks the rotated resolution IdentityRematched, the
		// identity-proven renumbering the resolver reports.
		rematched bool
		// siblingHash is the rotated candidate's durable identity tier. A
		// different hash under the same release name is a genuine substitution.
		siblingHash string
		wantRotated bool
	}{
		{name: "explicit renumbered match rotates", selection: playback.FileSelectionExplicitV3, rematched: true, siblingHash: "hash-a", wantRotated: true},
		{name: "auto renumbered match rotates", selection: playback.FileSelectionAutoV3, rematched: true, siblingHash: "hash-a", wantRotated: true},
		{name: "explicit identity-proven match rotates", selection: playback.FileSelectionExplicitV3, siblingHash: "hash-a", wantRotated: true},
		{name: "auto identity-proven match rotates", selection: playback.FileSelectionAutoV3, siblingHash: "hash-a", wantRotated: true},
		{name: "explicit genuinely different refused", selection: playback.FileSelectionExplicitV3, siblingHash: "hash-b", wantRotated: false},
		{name: "auto genuinely different refused", selection: playback.FileSelectionAutoV3, siblingHash: "hash-b", wantRotated: false},
		{name: "explicit weak identity refused", selection: playback.FileSelectionExplicitV3, weakIdentity: true, siblingHash: "hash-a", wantRotated: false},
		{name: "auto weak identity refused", selection: playback.FileSelectionAutoV3, weakIdentity: true, siblingHash: "hash-a", wantRotated: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewPlaybackHandler(playback.NewSessionManager(0, 0))
			h.VirtualPlaybackResolver = VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
				return "http://127.0.0.1:9/unused", nil
			})
			var rotates []bool
			h.VirtualMediaDetailedResolver = VirtualMediaDetailedResolverFunc(func(ctx context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
				rotates = append(rotates, VirtualCandidateRotationAllowed(ctx))
				if !VirtualCandidateRotationAllowed(ctx) {
					return ResolvedVirtualMedia{}, absentSessionPinError("pinned")
				}
				return ResolvedVirtualMedia{
					URL: "http://127.0.0.1:9/sibling", URI: siblingURI, CandidateID: "sibling",
					IdentityRematched: tc.rematched, ProviderVideoHash: tc.siblingHash, ProviderReleaseName: "Movie.2024",
				}, nil
			})
			file := &models.MediaFile{ID: 7, ContentID: "movie-unified-rotation", FilePath: pinnedURI, VirtualOwnerInstallationID: 5}
			if !tc.weakIdentity {
				file.ProviderVideoHash = "hash-a"
				file.ProviderReleaseName = "Movie.2024"
			}

			r := httptest.NewRequest(http.MethodPost, "/api/v1/playback/replan", nil).WithContext(newAuthorizedPlaybackContext())
			resolved, err := h.resolveRehydratedVirtualSourceV3(r, file, "profile-1", nil, "pinned", "auto", 0,
				virtualResolveOptionsV3{sessionBound: true, sessionAnchorURI: pinnedURI, bypassProviderFloor: true})

			if tc.wantRotated {
				if err != nil {
					t.Fatalf("err = %v, want the rotated same-release sibling", err)
				}
				if got := virtualResultCandidateID(resolved.URI); got != "sibling" {
					t.Fatalf("resolved %q, want the rotated sibling", got)
				}
				if len(rotates) != 2 || rotates[0] || !rotates[1] {
					t.Fatalf("rotation intents = %v, want exactly [false true]", rotates)
				}
				return
			}
			if !errors.Is(err, virtuallibrary.ErrSessionBoundCandidateAbsent) {
				t.Fatalf("err = %v, want the original absent-pin refusal", err)
			}
			if got := virtualResultCandidateID(resolved.URI); got != "" {
				t.Fatalf("resolved %q, want no accepted rotation", got)
			}
			if len(rotates) != 2 || rotates[0] || !rotates[1] {
				t.Fatalf("rotation intents = %v, want the retry still attempted [false true]", rotates)
			}
		})
	}
}

// TestVirtualCandidateRotationPendingHonorsExplicitPin proves the other
// rotation door — the failure-recovery demotion hold
// (virtualCandidateRotationPendingV3) that keeps a server-transcode delivery
// eligible for a candidate rotation — reads the same explicit-pin predicate. On
// one live rejected generation, an auto selection holds the delivery for
// rotation while an explicit pin does not: the demotion hold must not keep a
// delivery alive for a rotation an explicit pick will not take.
func TestVirtualCandidateRotationPendingHonorsExplicitPin(t *testing.T) {
	f := newDecodeRotationFixture(t, decodeRotationOptions{
		candidateIDs:        []string{"A", "B"},
		decodeFailCalls:     1,
		rejectAfterManifest: true,
	})
	start := f.request()

	code, started := f.start(t, start)
	if code != http.StatusCreated || started.PlaybackPlan == nil {
		t.Fatalf("start status=%d response=%+v", code, started)
	}
	defer f.handler.tm.CloseTranscodeSession(started.SessionID, "")
	if got := f.sessionVirtualURI(t, started.SessionID); got != decodeRotationCandidateURI("A") {
		t.Fatalf("start candidate = %q, want A", got)
	}
	// The rejection must land on the committed generation before the hold can
	// observe the live verdict.
	f.waitForSourceRejected(t, started.SessionID)

	record, err := f.handler.PlanStoreV3.GetAttempt(t.Context(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	req := playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3,
		Operation:       playback.ReplanOperationFailureRecoveryV3,
		Failure:         playback.FailureV3{Classification: "decode_error"},
	}
	if !f.handler.virtualCandidateRotationPendingV3(record, req) {
		t.Fatal("auto selection with a live rejected generation did not hold the delivery for rotation")
	}

	explicit := *record
	explicit.NormalizedRequest.FileSelection = playback.FileSelectionExplicitV3
	if f.handler.virtualCandidateRotationPendingV3(&explicit, req) {
		t.Fatal("explicit pin held the delivery for a rotation the explicit pick will not take")
	}
}

// TestTrustedPinDegradesForAutoSelectionOnly pins the delisted-pin degrade. The
// trust window keys on the row's own updated_at, not on presence in the listing,
// so a candidate dropped today stays "trusted" for up to candidate_store_hours
// and every replan refuses. Once the bounded forced relist has also failed and
// the row has no usable stored URL, an automatic selection degrades the pin to
// rotatable; an explicit pick keeps the refusal, because the viewer chose that
// release and must never be silently moved off it.
func TestTrustedPinDegradesForAutoSelectionOnly(t *testing.T) {
	const (
		neutralURI = "virtual://movie/tt-delisted-trust"
		pinnedURI  = neutralURI + "?result=pinned"
		siblingURI = neutralURI + "?result=sibling"
	)

	for _, tc := range []struct {
		name        string
		selection   playback.FileSelectionV3
		wantRotated bool
	}{
		{name: "auto selection degrades and rotates", selection: playback.FileSelectionAutoV3, wantRotated: true},
		{name: "explicit pick keeps the refusal", selection: playback.FileSelectionExplicitV3, wantRotated: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewPlaybackHandler(playback.NewSessionManager(0, 0))
			h.VirtualCandidateTrustWindow = func() time.Duration { return 720 * time.Hour }
			h.VirtualPlaybackResolver = VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
				return "http://127.0.0.1:9/unused", nil
			})
			var trustSeen []bool
			h.VirtualMediaDetailedResolver = VirtualMediaDetailedResolverFunc(func(ctx context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
				trusted := virtuallibrary.PersistedCandidateTrusted(ctx)
				trustSeen = append(trustSeen, trusted)
				if trusted {
					return ResolvedVirtualMedia{}, fmt.Errorf("trusted persisted virtual candidate %q is no longer listed: %w", "pinned", virtuallibrary.ErrPersistedCandidateTrusted)
				}
				return ResolvedVirtualMedia{
					URL: "http://127.0.0.1:9/sibling", URI: siblingURI, CandidateID: "sibling",
					ProviderVideoHash: "hash-b", ProviderReleaseName: "Other.Release.2024",
				}, nil
			})
			delivered := time.Now()
			file := &models.MediaFile{
				ID: 7, ContentID: "movie-delisted-trust", FilePath: pinnedURI, Container: "virtual",
				VirtualOwnerInstallationID: 5, UpdatedAt: time.Now(),
				ProviderVideoHash: "hash-a", ProviderReleaseName: "Movie.2024",
				// Delivery evidence without a stored URL: the trust window
				// applies, but there is nothing on the row to re-serve, which is
				// the state the degrade exists for.
				LastDeliveredAt: &delivered,
			}
			record := &playback.AttemptRecordV3{NormalizedRequest: playback.StartRequestV3{FileSelection: tc.selection}}

			r := httptest.NewRequest(http.MethodPost, "/api/v1/playback/replan", nil).WithContext(newAuthorizedPlaybackContext())
			resolved, err := h.resolveRehydratedVirtualSourceWithAttemptV3(r, file, "profile-1", nil, "pinned", "auto", 0,
				virtualResolveOptionsV3{sessionBound: true, sessionAnchorURI: pinnedURI, bypassProviderFloor: true}, record)

			if tc.wantRotated {
				if err != nil {
					t.Fatalf("err = %v, want the rotated sibling", err)
				}
				if got := virtualResultCandidateID(resolved.URI); got != "sibling" {
					t.Fatalf("resolved %q, want the rotated sibling", got)
				}
				// The pin was trusted for the first attempt and the forced
				// relist, and only the degraded attempt ran without it.
				if len(trustSeen) != 3 || !trustSeen[0] || !trustSeen[1] || trustSeen[2] {
					t.Fatalf("trust intents = %v, want [true true false]", trustSeen)
				}
				return
			}
			if !errors.Is(err, virtuallibrary.ErrPersistedCandidateTrusted) {
				t.Fatalf("err = %v, want the trusted-absent refusal", err)
			}
			if got := virtualResultCandidateID(resolved.URI); got != "" {
				t.Fatalf("resolved %q, want no release swap for an explicit pick", got)
			}
			if len(trustSeen) != 1 {
				t.Fatalf("trusted resolve attempts = %d, want 1 (no rotation retry for an explicit pin)", len(trustSeen))
			}
		})
	}
}
