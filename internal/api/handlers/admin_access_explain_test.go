package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeAdminAccessExplainAuthorizer struct {
	explanations  map[auth.ACLAction]auth.AccessExplanation
	defaultPolicy auth.EffectivePolicy
	requests      []auth.AccessRequest
}

func (f *fakeAdminAccessExplainAuthorizer) Authorize(_ context.Context, request auth.AccessRequest) (auth.AccessDecision, error) {
	explanation, ok := f.explanations[request.Action]
	if !ok {
		return auth.AccessDecision{
			Allowed:         false,
			ReasonCode:      "default_deny",
			EffectivePolicy: f.defaultPolicy,
		}, nil
	}
	return explanation.Decision, nil
}

func (f *fakeAdminAccessExplainAuthorizer) Explain(_ context.Context, request auth.AccessRequest) (auth.AccessExplanation, error) {
	f.requests = append(f.requests, request)
	explanation, ok := f.explanations[request.Action]
	if !ok {
		return auth.AccessExplanation{
			Request: request,
			Decision: auth.AccessDecision{
				Allowed:         false,
				ReasonCode:      "default_deny",
				EffectivePolicy: f.defaultPolicy,
			},
		}, nil
	}
	explanation.Request = request
	return explanation, nil
}

func TestHandleGetUserAccessExplanationShowsCascadeSources(t *testing.T) {
	winningRule := auth.ACLRule{
		ID:           7,
		SubjectType:  auth.SubjectGroup,
		SubjectID:    "standard_user",
		Action:       auth.ActionPlaybackPlay,
		ResourceType: auth.ResourceMediaItem,
		ResourceID:   "*",
		Effect:       auth.EffectAllow,
		Priority:     10,
		Name:         "User playback",
	}
	authorizer := &fakeAdminAccessExplainAuthorizer{
		explanations: map[auth.ACLAction]auth.AccessExplanation{
			auth.ActionPlaybackPlay: {
				Decision: auth.AccessDecision{
					Allowed:      true,
					ReasonCode:   "rule_allow",
					WinningRule:  &winningRule,
					MatchedRules: []auth.ACLRule{winningRule},
					EffectivePolicy: auth.EffectivePolicy{
						MaxStreams:                 4,
						MaxTranscodes:              2,
						MaxProfiles:                5,
						DirectDownloadsAllowed:     true,
						TranscodedDownloadsAllowed: false,
					},
				},
				EvaluatedRules: []auth.ACLRule{winningRule},
			},
		},
		defaultPolicy: auth.EffectivePolicy{
			MaxStreams:                 4,
			MaxTranscodes:              2,
			MaxProfiles:                5,
			DirectDownloadsAllowed:     true,
			TranscodedDownloadsAllowed: false,
		},
	}
	repo := newFakeAdminACLRepository()
	repo.groups["standard_user"] = auth.ACLGroup{ID: 5, Slug: "standard_user", Name: "User", BuiltIn: true}
	repo.userGroups[7] = []auth.ACLGroup{repo.groups["standard_user"]}
	h := &AdminHandler{
		userRepo: &fakeAdminUpdateUserRepo{
			user: &models.User{ID: 7, Username: "tom", Email: "tom@example.com", Role: "user", Enabled: true},
		},
		AccessGroups:    repo,
		AdminAuthorizer: authorizer,
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/users/7/access-explain", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", "7")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rec := httptest.NewRecorder()

	h.HandleGetUserAccessExplanation(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp adminUserAccessExplanationResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.User.Username != "tom" {
		t.Fatalf("user = %#v, want tom", resp.User)
	}
	if resp.EffectivePolicy.MaxStreams != 4 {
		t.Fatalf("max streams = %d, want 4", resp.EffectivePolicy.MaxStreams)
	}
	playback := findAdminAccessExplanation(resp.Actions, auth.ActionPlaybackPlay)
	if playback == nil {
		t.Fatalf("missing playback.play explanation in %#v", resp.Actions)
	}
	if !playback.Allowed || playback.ReasonCode != "rule_allow" {
		t.Fatalf("playback explanation = %#v, want allowed rule_allow", playback)
	}
	if playback.Source.Type != "group" || playback.Source.ID != "standard_user" || playback.Source.Name != "User" {
		t.Fatalf("playback source = %#v, want standard_user group", playback.Source)
	}
	if playback.WinningRule == nil || playback.WinningRule.Name != "User playback" {
		t.Fatalf("winning rule = %#v, want User playback", playback.WinningRule)
	}
	security := findAdminAccessExplanation(resp.Actions, auth.ActionSecurityManage)
	if security == nil {
		t.Fatalf("missing security.manage explanation")
	}
	if security.Allowed || security.Source.Type != "default" || security.ReasonCode != "default_deny" {
		t.Fatalf("security explanation = %#v, want default deny", security)
	}
	if len(authorizer.requests) == 0 || authorizer.requests[0].ResourceType != auth.ResourceServer {
		t.Fatalf("first request = %#v, want server resource", authorizer.requests)
	}
}

func findAdminAccessExplanation(rows []adminACLActionExplanationResponse, action auth.ACLAction) *adminACLActionExplanationResponse {
	for i := range rows {
		if rows[i].Action == action {
			return &rows[i]
		}
	}
	return nil
}
