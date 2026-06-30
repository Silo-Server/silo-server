package api

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeSessionLimitUserRepo struct {
	user *models.User
}

func (f fakeSessionLimitUserRepo) GetByID(context.Context, int) (*models.User, error) {
	return f.user, nil
}

type fakeSessionLimitAuthorizer struct {
	decision auth.AccessDecision
	request  auth.AccessRequest
}

func (f *fakeSessionLimitAuthorizer) Authorize(_ context.Context, request auth.AccessRequest) (auth.AccessDecision, error) {
	f.request = request
	return f.decision, nil
}

func (f *fakeSessionLimitAuthorizer) Explain(_ context.Context, request auth.AccessRequest) (auth.AccessExplanation, error) {
	decision, err := f.Authorize(context.Background(), request)
	return auth.AccessExplanation{Request: request, Decision: decision}, err
}

func TestResolvePlaybackSessionLimitsUsesEffectiveACLPolicy(t *testing.T) {
	authorizer := &fakeSessionLimitAuthorizer{
		decision: auth.AccessDecision{
			Allowed: true,
			EffectivePolicy: auth.EffectivePolicy{
				MaxStreams:    5,
				MaxTranscodes: 1,
			},
		},
	}

	limits, err := resolvePlaybackSessionLimits(
		context.Background(),
		fakeSessionLimitUserRepo{user: &models.User{ID: 7, MaxStreams: 6, MaxTranscodes: 2}},
		authorizer,
		7,
	)
	if err != nil {
		t.Fatalf("resolvePlaybackSessionLimits() error = %v", err)
	}
	if limits.MaxStreams != 5 || limits.MaxTranscodes != 1 {
		t.Fatalf("limits = %#v, want max streams 5 and transcodes 1", limits)
	}
	if authorizer.request.Action != auth.ActionPlaybackPlay {
		t.Fatalf("authorizer action = %q, want %q", authorizer.request.Action, auth.ActionPlaybackPlay)
	}
}
