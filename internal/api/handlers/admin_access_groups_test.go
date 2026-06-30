package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/auth"
)

type fakeAdminACLRepository struct {
	groups     map[string]auth.ACLGroup
	rules      map[string][]auth.ACLRule
	members    map[string][]auth.ACLGroupMember
	counts     map[string]int
	userGroups map[int][]auth.ACLGroup

	createInput       auth.CreateACLGroupInput
	updateSlug        string
	updateInput       auth.UpdateACLGroupInput
	deleteSlug        string
	replaceRulesSlug  string
	replaceRulesInput []auth.ACLRuleInput
	err               error
}

func newFakeAdminACLRepository() *fakeAdminACLRepository {
	return &fakeAdminACLRepository{
		groups:     map[string]auth.ACLGroup{},
		rules:      map[string][]auth.ACLRule{},
		members:    map[string][]auth.ACLGroupMember{},
		counts:     map[string]int{},
		userGroups: map[int][]auth.ACLGroup{},
	}
}

func (f *fakeAdminACLRepository) ListGroups(context.Context) ([]auth.ACLGroup, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]auth.ACLGroup, 0, len(f.groups))
	for _, group := range f.groups {
		out = append(out, group)
	}
	return out, nil
}

func (f *fakeAdminACLRepository) ListGroupsByUserIDs(_ context.Context, userIDs []int) (map[int][]auth.ACLGroup, error) {
	out := map[int][]auth.ACLGroup{}
	for _, userID := range userIDs {
		out[userID] = append([]auth.ACLGroup(nil), f.userGroups[userID]...)
	}
	return out, nil
}

func (f *fakeAdminACLRepository) ListGroupMemberCounts(context.Context) (map[string]int, error) {
	return f.counts, nil
}

func (f *fakeAdminACLRepository) ListGroupMembers(_ context.Context, slug string) ([]auth.ACLGroupMember, error) {
	return append([]auth.ACLGroupMember(nil), f.members[slug]...), nil
}

func (f *fakeAdminACLRepository) ReplaceUserGroups(context.Context, int, []string) error {
	return nil
}

func (f *fakeAdminACLRepository) GetGroup(ctx context.Context, slug string) (auth.ACLGroup, []auth.ACLRule, error) {
	if f.err != nil {
		return auth.ACLGroup{}, nil, f.err
	}
	group, ok := f.groups[slug]
	if !ok {
		return auth.ACLGroup{}, nil, auth.ErrNotFound
	}
	return group, f.rules[slug], nil
}

func (f *fakeAdminACLRepository) CreateGroup(ctx context.Context, input auth.CreateACLGroupInput) (auth.ACLGroup, error) {
	if f.err != nil {
		return auth.ACLGroup{}, f.err
	}
	f.createInput = input
	group := auth.ACLGroup{ID: 42, Slug: input.Slug, Name: input.Name, Description: input.Description, Policy: input.Policy}
	f.groups[group.Slug] = group
	return group, nil
}

func (f *fakeAdminACLRepository) UpdateGroup(ctx context.Context, slug string, input auth.UpdateACLGroupInput) (auth.ACLGroup, error) {
	if f.err != nil {
		return auth.ACLGroup{}, f.err
	}
	f.updateSlug = slug
	f.updateInput = input
	group, ok := f.groups[slug]
	if !ok {
		return auth.ACLGroup{}, auth.ErrNotFound
	}
	if group.Protected || group.BuiltIn {
		return auth.ACLGroup{}, auth.ErrProtectedACLGroup
	}
	group.Name = input.Name
	group.Description = input.Description
	group.Policy = input.Policy
	f.groups[slug] = group
	return group, nil
}

func (f *fakeAdminACLRepository) DeleteGroup(ctx context.Context, slug string) error {
	if f.err != nil {
		return f.err
	}
	f.deleteSlug = slug
	group, ok := f.groups[slug]
	if !ok {
		return auth.ErrNotFound
	}
	if group.Protected || group.BuiltIn {
		return auth.ErrProtectedACLGroup
	}
	delete(f.groups, slug)
	delete(f.rules, slug)
	return nil
}

func (f *fakeAdminACLRepository) ReplaceGroupRules(ctx context.Context, slug string, rules []auth.ACLRuleInput) ([]auth.ACLRule, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.replaceRulesSlug = slug
	f.replaceRulesInput = append([]auth.ACLRuleInput(nil), rules...)
	out := make([]auth.ACLRule, 0, len(rules))
	for i, rule := range rules {
		out = append(out, auth.ACLRule{
			ID:           int64(i + 1),
			SubjectType:  auth.SubjectGroup,
			SubjectID:    slug,
			Action:       rule.Action,
			ResourceType: rule.ResourceType,
			ResourceID:   rule.ResourceID,
			Effect:       rule.Effect,
			Conditions:   rule.Conditions,
			Priority:     rule.Priority,
			Name:         rule.Name,
			Description:  rule.Description,
		})
	}
	f.rules[slug] = out
	return out, nil
}

func accessGroupRequest(t *testing.T, method, path, slug, body string) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if slug != "" {
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("slug", slug)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	}
	return httptest.NewRecorder(), req
}

func TestHandleGetAccessGroupIncludesMembers(t *testing.T) {
	repo := newFakeAdminACLRepository()
	repo.groups["standard_user"] = auth.ACLGroup{
		ID:          5,
		Slug:        "standard_user",
		Name:        "User",
		Description: "Normal media access.",
		BuiltIn:     true,
	}
	repo.rules["standard_user"] = []auth.ACLRule{
		{ID: 1, SubjectType: auth.SubjectGroup, SubjectID: "standard_user", Action: auth.ActionPlaybackPlay, ResourceType: auth.ResourceMediaItem, ResourceID: "*", Effect: auth.EffectAllow, Name: "User playback"},
	}
	repo.members["standard_user"] = []auth.ACLGroupMember{
		{UserID: 7, Username: "tom", Email: "tom@example.com", Role: "user", Enabled: true},
		{UserID: 8, Username: "martha", Email: "martha@example.com", Role: "user", Enabled: false},
	}
	h := &AdminHandler{AccessGroups: repo}
	rec, req := accessGroupRequest(t, http.MethodGet, "/admin/access-groups/standard_user", "standard_user", "")

	h.HandleGetAccessGroup(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp adminACLGroupDetailResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Slug != "standard_user" {
		t.Fatalf("slug = %q, want standard_user", resp.Slug)
	}
	if len(resp.Members) != 2 {
		t.Fatalf("members = %#v, want two", resp.Members)
	}
	if resp.Members[0].Username != "tom" || !resp.Members[0].Enabled {
		t.Fatalf("first member = %#v, want enabled tom", resp.Members[0])
	}
	if resp.Members[1].Username != "martha" || resp.Members[1].Enabled {
		t.Fatalf("second member = %#v, want disabled martha", resp.Members[1])
	}
}

func TestHandleCreateAccessGroupCreatesCustomGroupWithRules(t *testing.T) {
	repo := newFakeAdminACLRepository()
	h := &AdminHandler{AccessGroups: repo}
	rec, req := accessGroupRequest(t, http.MethodPost, "/admin/access-groups", "", `{
		"slug": "curators",
		"name": "Curators",
		"description": "Can curate metadata in selected libraries.",
		"policy": {"max_profiles": 4, "max_streams": 5},
		"rules": [
			{
				"action": "metadata.curate",
				"resource_type": "media_item",
				"resource_id": "*",
				"effect": "allow",
				"conditions": {"library_ids": [1, 2]},
				"priority": 25,
				"name": "Curate selected libraries"
			}
		]
	}`)

	h.HandleCreateAccessGroup(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if repo.createInput.Slug != "curators" || repo.createInput.Name != "Curators" {
		t.Fatalf("create input = %#v", repo.createInput)
	}
	if repo.createInput.Policy.MaxProfiles == nil || *repo.createInput.Policy.MaxProfiles != 4 {
		t.Fatalf("create policy max profiles = %#v, want 4", repo.createInput.Policy.MaxProfiles)
	}
	if repo.createInput.Policy.MaxStreams == nil || *repo.createInput.Policy.MaxStreams != 5 {
		t.Fatalf("create policy max streams = %#v, want 5", repo.createInput.Policy.MaxStreams)
	}
	if repo.replaceRulesSlug != "curators" || len(repo.replaceRulesInput) != 1 {
		t.Fatalf("replace rules slug/input = %q %#v", repo.replaceRulesSlug, repo.replaceRulesInput)
	}
	gotRule := repo.replaceRulesInput[0]
	if gotRule.Action != auth.ActionMetadataCurate || gotRule.ResourceType != auth.ResourceMediaItem {
		t.Fatalf("rule = %#v", gotRule)
	}
	if !reflect.DeepEqual(gotRule.Conditions.LibraryIDs, []int{1, 2}) {
		t.Fatalf("library ids = %#v, want [1 2]", gotRule.Conditions.LibraryIDs)
	}

	var resp adminACLGroupDetailResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Slug != "curators" || len(resp.Rules) != 1 {
		t.Fatalf("response = %#v", resp)
	}
}

func TestHandleUpdateAccessGroupRejectsBuiltInGroup(t *testing.T) {
	repo := newFakeAdminACLRepository()
	repo.groups["admin"] = auth.ACLGroup{ID: 1, Slug: "admin", Name: "Admin", BuiltIn: true, Protected: true}
	h := &AdminHandler{AccessGroups: repo}
	rec, req := accessGroupRequest(t, http.MethodPut, "/admin/access-groups/admin", "admin", `{
		"name": "Changed",
		"description": "",
		"rules": []
	}`)

	h.HandleUpdateAccessGroup(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleDeleteAccessGroupDeletesCustomGroup(t *testing.T) {
	repo := newFakeAdminACLRepository()
	repo.groups["curators"] = auth.ACLGroup{ID: 42, Slug: "curators", Name: "Curators"}
	h := &AdminHandler{AccessGroups: repo}
	rec, req := accessGroupRequest(t, http.MethodDelete, "/admin/access-groups/curators", "curators", "")

	h.HandleDeleteAccessGroup(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if repo.deleteSlug != "curators" {
		t.Fatalf("delete slug = %q, want curators", repo.deleteSlug)
	}
}

func TestHandleCreateAccessGroupMapsDuplicateGroupToConflict(t *testing.T) {
	repo := newFakeAdminACLRepository()
	repo.err = auth.ErrACLGroupExists
	h := &AdminHandler{AccessGroups: repo}
	rec, req := accessGroupRequest(t, http.MethodPost, "/admin/access-groups", "", `{
		"slug": "curators",
		"name": "Curators",
		"rules": []
	}`)

	h.HandleCreateAccessGroup(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCreateAccessGroupRejectsInvalidRule(t *testing.T) {
	repo := newFakeAdminACLRepository()
	h := &AdminHandler{AccessGroups: repo}
	rec, req := accessGroupRequest(t, http.MethodPost, "/admin/access-groups", "", `{
		"slug": "curators",
		"name": "Curators",
		"rules": [{"action": "server.destroy", "resource_type": "server", "effect": "allow"}]
	}`)

	h.HandleCreateAccessGroup(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !errors.Is(repo.err, nil) {
		t.Fatalf("repo should not be called with invalid rule")
	}
}
