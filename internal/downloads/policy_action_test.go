package downloads

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	policyengine "github.com/Silo-Server/silo-server/internal/policy"
)

func TestPolicyActionDeciderMatchesLegacyCapability(t *testing.T) {
	ctx := context.Background()
	pdp := newDownloadPolicyPDP(t)

	for _, downloadsEnabled := range []bool{false, true} {
		for _, transcodeEnabled := range []bool{false, true} {
			for _, downloadAllowed := range []bool{false, true} {
				for _, downloadTranscodeAllowed := range []bool{false, true} {
					for _, artifactsAvailable := range []bool{false, true} {
						cfg := config.DownloadConfig{Enabled: downloadsEnabled, TranscodeEnabled: transcodeEnabled}
						user := &models.User{ID: 9, DownloadAllowed: downloadAllowed, DownloadTranscodeAllowed: downloadTranscodeAllowed}
						legacy := newPolicyActionTestService(user, cfg, artifactsAvailable, nil)
						withPolicy := newPolicyActionTestService(user, cfg, artifactsAvailable, pdp)

						legacyCap, err := legacy.Capability(ctx, user.ID)
						if err != nil {
							t.Fatalf("legacy Capability error: %v", err)
						}
						policyCap, err := withPolicy.Capability(ctx, user.ID)
						if err != nil {
							t.Fatalf("policy Capability error: %v", err)
						}
						if !reflect.DeepEqual(policyCap, legacyCap) {
							t.Fatalf("policy capability = %+v, want legacy %+v for cfg=%+v user=%+v artifacts=%v",
								policyCap, legacyCap, cfg, user, artifactsAvailable)
						}
					}
				}
			}
		}
	}
}

func TestPolicyActionDeciderMatchesLegacyCreateGate(t *testing.T) {
	ctx := context.Background()
	pdp := newDownloadPolicyPDP(t)

	for _, downloadsEnabled := range []bool{false, true} {
		for _, transcodeEnabled := range []bool{false, true} {
			for _, downloadAllowed := range []bool{false, true} {
				for _, downloadTranscodeAllowed := range []bool{false, true} {
					for _, artifactsAvailable := range []bool{false, true} {
						cfg := config.DownloadConfig{Enabled: downloadsEnabled, TranscodeEnabled: transcodeEnabled}
						user := &models.User{ID: 9, DownloadAllowed: downloadAllowed, DownloadTranscodeAllowed: downloadTranscodeAllowed}
						legacy := newPolicyActionTestService(user, cfg, artifactsAvailable, nil)
						withPolicy := newPolicyActionTestService(user, cfg, artifactsAvailable, pdp)

						_, _, legacyErr := legacy.downloadConfigForUser(ctx, user.ID, "device-1")
						_, _, policyErr := withPolicy.downloadConfigForUser(ctx, user.ID, "device-1")
						if !sameDownloadGateError(policyErr, legacyErr) {
							t.Fatalf("policy create gate error = %v, want legacy %v for cfg=%+v user=%+v artifacts=%v",
								policyErr, legacyErr, cfg, user, artifactsAvailable)
						}
					}
				}
			}
		}
	}
}

func newPolicyActionTestService(
	user *models.User,
	cfg config.DownloadConfig,
	artifactsAvailable bool,
	decider ActionDecider,
) *Service {
	svc := NewService(nil, nil, nil, nil, nil, nil, fakeUserRepo{user}, nil, nil, &cfg)
	if artifactsAvailable {
		svc.SetArtifactManager(&ArtifactManager{})
	}
	if decider != nil {
		svc.SetActionDecider(decider)
	}
	return svc
}

func sameDownloadGateError(got, want error) bool {
	switch {
	case want == nil:
		return got == nil
	case errors.Is(want, ErrFeatureDisabled):
		return errors.Is(got, ErrFeatureDisabled)
	case errors.Is(want, ErrDownloadNotAllowed):
		return errors.Is(got, ErrDownloadNotAllowed)
	default:
		return errors.Is(got, want)
	}
}

func newDownloadPolicyPDP(t *testing.T) *policyengine.PDP {
	t.Helper()
	engine, err := policyengine.NewEngine(context.Background())
	if err != nil {
		t.Fatalf("NewEngine() error: %v", err)
	}
	return policyengine.NewPDP(engine)
}
