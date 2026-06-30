# Security ACL Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the behavior-preserving ACL foundation for Silo's future tiered/customizable security model.

**Architecture:** Introduce a central ACL model and evaluator in `internal/auth` without changing existing API route behavior. Seed durable ACL tables and compatibility mappings so current `admin`, `user`, `marker_edit`, `metadata_curation`, library, playback, and download policy inputs can be resolved through the new engine.

**Tech Stack:** Go, PostgreSQL, goose SQL migrations, pgx, existing Silo auth/user models, `go test`.

## Global Constraints

- Preserve current behavior in the first rollout.
- Existing admins remain admins.
- Existing users keep their library restrictions, playback limits, download settings, and current feature permissions.
- Built-in roles are seeded as default groups, not special cases spread across the codebase.
- API code should move toward action/resource authorization instead of role-string checks.
- Access decisions must be explainable.
- Conditions must be structured data, not free-form expressions.
- Disabled users are always denied.
- Existing `marker_edit` maps to `markers.edit`.
- Existing `metadata_curation` maps to `metadata.curate`.
- The first migration promotes the oldest enabled admin account to Owner and every other enabled admin to Admin.
- Do not remove or repurpose legacy `users.role`, `users.permissions`, `users.library_ids`, playback/download/profile limit columns, or `users.access_policy_revision` in this phase.

---

## File Structure

- Create `internal/auth/acl_actions.go`: action constants, resource constants, effect constants, and built-in group slugs.
- Create `internal/auth/acl_types.go`: request, rule, condition, decision, explanation, and effective policy structs.
- Create `internal/auth/acl_evaluator.go`: pure in-memory evaluator for deny/allow precedence, condition matching, and explanation output.
- Create `internal/auth/acl_compat.go`: compatibility adapter from existing `models.User` fields and legacy permissions into seeded group/rule inputs.
- Create `internal/auth/acl_repository.go`: repository methods for reading groups, memberships, rules, and policy revision.
- Create `internal/auth/acl_*_test.go`: unit tests for constants, compatibility mapping, evaluator precedence, limits, and explanations.
- Add migration `migrations/sql/20260629193000_acl_foundation.sql`: ACL groups, memberships, rules, policy revision table, and safe seed data.
- No frontend files in this foundation plan.
- No route middleware conversion in this foundation plan.

---

### Task 1: Action, Resource, and Type Definitions

**Files:**
- Create: `internal/auth/acl_actions.go`
- Create: `internal/auth/acl_types.go`
- Test: `internal/auth/acl_actions_test.go`

**Interfaces:**
- Produces: `type ACLAction string`
- Produces: `type ACLResourceType string`
- Produces: `type ACLEffect string`
- Produces: `type ACLSubjectType string`
- Produces: constants such as `ActionUsersManage`, `ActionPlaybackPlay`, `ResourceLibrary`, `EffectAllow`, `SubjectGroup`
- Produces: `type AccessRequest struct`
- Produces: `type ACLRule struct`
- Produces: `type ACLCondition struct`
- Produces: `type AccessDecision struct`
- Produces: `type AccessExplanation struct`
- Produces: `type EffectivePolicy struct`

- [ ] **Step 1: Write failing constant coverage tests**

Create `internal/auth/acl_actions_test.go`:

```go
package auth

import "testing"

func TestACLActionConstantsCoverLegacyPermissions(t *testing.T) {
	if LegacyPermissionAction(PermissionMarkerEdit) != ActionMarkersEdit {
		t.Fatalf("marker_edit maps to %q, want %q", LegacyPermissionAction(PermissionMarkerEdit), ActionMarkersEdit)
	}
	if LegacyPermissionAction(PermissionMetadataCuration) != ActionMetadataCurate {
		t.Fatalf("metadata_curation maps to %q, want %q", LegacyPermissionAction(PermissionMetadataCuration), ActionMetadataCurate)
	}
}

func TestBuiltInGroupSlugsAreStable(t *testing.T) {
	tests := map[string]BuiltInGroupSlug{
		"owner":            GroupOwner,
		"admin":            GroupAdmin,
		"library_manager":  GroupLibraryManager,
		"metadata_curator": GroupMetadataCurator,
		"viewer":           GroupViewer,
		"restricted_viewer": GroupRestrictedViewer,
	}
	for want, got := range tests {
		if string(got) != want {
			t.Fatalf("group slug = %q, want %q", got, want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
GOWORK=off GOCACHE=${SILO_GO_BUILD_CACHE:-.cache/go-build} GOMODCACHE=${SILO_GO_MOD_CACHE:-.cache/go-mod} go test ./internal/auth -run 'TestACLActionConstantsCoverLegacyPermissions|TestBuiltInGroupSlugsAreStable'
```

Expected: FAIL with undefined identifiers such as `LegacyPermissionAction`, `ActionMarkersEdit`, and `GroupOwner`.

- [ ] **Step 3: Add action/resource constants**

Create `internal/auth/acl_actions.go`:

```go
package auth

type ACLAction string
type ACLResourceType string
type ACLEffect string
type ACLSubjectType string
type BuiltInGroupSlug string

const (
	EffectAllow ACLEffect = "allow"
	EffectDeny  ACLEffect = "deny"
)

const (
	SubjectUser        ACLSubjectType = "user"
	SubjectGroup       ACLSubjectType = "group"
	SubjectBuiltInRole ACLSubjectType = "builtin_role"
	SubjectEveryone    ACLSubjectType = "everyone"
)

const (
	GroupOwner            BuiltInGroupSlug = "owner"
	GroupAdmin            BuiltInGroupSlug = "admin"
	GroupLibraryManager   BuiltInGroupSlug = "library_manager"
	GroupMetadataCurator  BuiltInGroupSlug = "metadata_curator"
	GroupViewer           BuiltInGroupSlug = "viewer"
	GroupRestrictedViewer BuiltInGroupSlug = "restricted_viewer"
)

const (
	ActionServerView         ACLAction = "server.view"
	ActionServerConfigure    ACLAction = "server.configure"
	ActionSecurityManage     ACLAction = "security.manage"
	ActionUsersView          ACLAction = "users.view"
	ActionUsersManage        ACLAction = "users.manage"
	ActionUsersImpersonate   ACLAction = "users.impersonate"
	ActionLibrariesView      ACLAction = "libraries.view"
	ActionLibrariesManage    ACLAction = "libraries.manage"
	ActionTasksView          ACLAction = "tasks.view"
	ActionTasksRun           ACLAction = "tasks.run"
	ActionLogsView           ACLAction = "logs.view"
	ActionPluginsView        ACLAction = "plugins.view"
	ActionPluginsManage      ACLAction = "plugins.manage"
	ActionNodesView          ACLAction = "nodes.view"
	ActionNodesManage        ACLAction = "nodes.manage"
	ActionMetadataCurate     ACLAction = "metadata.curate"
	ActionMarkersEdit        ACLAction = "markers.edit"
	ActionPlaybackPlay       ACLAction = "playback.play"
	ActionPlaybackTranscode  ACLAction = "playback.transcode"
	ActionDownloadsDirect    ACLAction = "downloads.direct"
	ActionDownloadsTranscode ACLAction = "downloads.transcode"
	ActionProfilesManage     ACLAction = "profiles.manage"
	ActionRequestsCreate     ACLAction = "requests.create"
	ActionRequestsApprove    ACLAction = "requests.approve"
)

const (
	ResourceServer           ACLResourceType = "server"
	ResourceSecuritySettings ACLResourceType = "security_settings"
	ResourceUser             ACLResourceType = "user"
	ResourceGroup            ACLResourceType = "group"
	ResourceLibrary          ACLResourceType = "library"
	ResourceMediaItem        ACLResourceType = "media_item"
	ResourceMediaType        ACLResourceType = "media_type"
	ResourceTask             ACLResourceType = "task"
	ResourceLog              ACLResourceType = "log"
	ResourcePlugin           ACLResourceType = "plugin"
	ResourceRemoteNode       ACLResourceType = "remote_node"
	ResourceProfile          ACLResourceType = "profile"
	ResourceRequest          ACLResourceType = "request"
)

func LegacyPermissionAction(permission Permission) ACLAction {
	switch permission {
	case PermissionMarkerEdit:
		return ActionMarkersEdit
	case PermissionMetadataCuration:
		return ActionMetadataCurate
	default:
		return ""
	}
}
```

- [ ] **Step 4: Add ACL request/decision types**

Create `internal/auth/acl_types.go`:

```go
package auth

import "time"

type AccessRequest struct {
	UserID       int
	Action       ACLAction
	ResourceType ACLResourceType
	ResourceID   string
	LibraryIDs   []int
	MediaType    string
	ProfileID    string
	PrimaryProfile bool
}

type ACLRule struct {
	ID          int64
	SubjectType ACLSubjectType
	SubjectID   string
	Action      ACLAction
	ResourceType ACLResourceType
	ResourceID   string
	Effect      ACLEffect
	Conditions  ACLCondition
	Priority    int
	Name        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type ACLCondition struct {
	LibraryIDs                 []int
	MediaTypes                 []string
	PrimaryProfileRequired     *bool
	MaxPlaybackQuality         string
	MaxStreams                 *int
	MaxTranscodes              *int
	DirectDownloadsAllowed     *bool
	TranscodedDownloadsAllowed *bool
	MaxContentRating           string
}

type AccessDecision struct {
	Allowed         bool
	ReasonCode      string
	WinningRule     *ACLRule
	MatchedRules    []ACLRule
	EffectivePolicy EffectivePolicy
}

type AccessExplanation struct {
	Request      AccessRequest
	Decision     AccessDecision
	EvaluatedRules []ACLRule
}

type EffectivePolicy struct {
	LibraryIDs                 []int
	MediaTypes                 []string
	MaxPlaybackQuality         string
	MaxStreams                 int
	MaxTranscodes              int
	DirectDownloadsAllowed     bool
	TranscodedDownloadsAllowed bool
	MaxContentRating           string
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run:

```bash
GOWORK=off GOCACHE=${SILO_GO_BUILD_CACHE:-.cache/go-build} GOMODCACHE=${SILO_GO_MOD_CACHE:-.cache/go-mod} go test ./internal/auth -run 'TestACLActionConstantsCoverLegacyPermissions|TestBuiltInGroupSlugsAreStable'
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/auth/acl_actions.go internal/auth/acl_types.go internal/auth/acl_actions_test.go
git commit -m "Add ACL action and resource types"
```

---

### Task 2: Behavior-Preserving Compatibility Mapping

**Files:**
- Create: `internal/auth/acl_compat.go`
- Test: `internal/auth/acl_compat_test.go`

**Interfaces:**
- Consumes: `models.User`
- Consumes: action/group constants from Task 1
- Produces: `func CompatibilityGroupsForUser(user *models.User) []BuiltInGroupSlug`
- Produces: `func CompatibilityRulesForUser(user *models.User) []ACLRule`
- Produces: `func CompatibilityEffectivePolicyForUser(user *models.User) EffectivePolicy`

- [ ] **Step 1: Write failing compatibility tests**

Create `internal/auth/acl_compat_test.go`:

```go
package auth

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestCompatibilityGroupsForAdminAndUser(t *testing.T) {
	adminGroups := CompatibilityGroupsForUser(&models.User{ID: 1, Role: "admin", Enabled: true})
	if len(adminGroups) != 1 || adminGroups[0] != GroupAdmin {
		t.Fatalf("admin groups = %#v, want [%q]", adminGroups, GroupAdmin)
	}

	userGroups := CompatibilityGroupsForUser(&models.User{ID: 2, Role: "user", Enabled: true})
	if len(userGroups) != 1 || userGroups[0] != GroupViewer {
		t.Fatalf("user groups = %#v, want [%q]", userGroups, GroupViewer)
	}
}

func TestCompatibilityRulesForLegacyPermissions(t *testing.T) {
	user := &models.User{
		ID:          7,
		Role:        "user",
		Enabled:     true,
		Permissions: []string{"marker_edit", "metadata_curation"},
	}

	rules := CompatibilityRulesForUser(user)
	actions := map[ACLAction]bool{}
	for _, rule := range rules {
		if rule.Effect == EffectAllow {
			actions[rule.Action] = true
		}
	}

	if !actions[ActionMarkersEdit] {
		t.Fatalf("expected marker_edit compatibility rule, got %#v", rules)
	}
	if !actions[ActionMetadataCurate] {
		t.Fatalf("expected metadata_curation compatibility rule, got %#v", rules)
	}
}

func TestCompatibilityEffectivePolicyPreservesUserLimits(t *testing.T) {
	user := &models.User{
		ID:                       7,
		Enabled:                  true,
		LibraryIDs:               []int{10, 20},
		MaxPlaybackQuality:       "1080p",
		MaxStreams:               3,
		MaxTranscodes:            1,
		DownloadAllowed:          true,
		DownloadTranscodeAllowed: false,
	}

	policy := CompatibilityEffectivePolicyForUser(user)
	if len(policy.LibraryIDs) != 2 || policy.LibraryIDs[0] != 10 || policy.LibraryIDs[1] != 20 {
		t.Fatalf("library ids = %#v, want [10 20]", policy.LibraryIDs)
	}
	if policy.MaxPlaybackQuality != "1080p" {
		t.Fatalf("max quality = %q, want 1080p", policy.MaxPlaybackQuality)
	}
	if policy.MaxStreams != 3 {
		t.Fatalf("max streams = %d, want 3", policy.MaxStreams)
	}
	if policy.MaxTranscodes != 1 {
		t.Fatalf("max transcodes = %d, want 1", policy.MaxTranscodes)
	}
	if !policy.DirectDownloadsAllowed {
		t.Fatalf("direct downloads should be allowed")
	}
	if policy.TranscodedDownloadsAllowed {
		t.Fatalf("transcoded downloads should not be allowed")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
GOWORK=off GOCACHE=${SILO_GO_BUILD_CACHE:-.cache/go-build} GOMODCACHE=${SILO_GO_MOD_CACHE:-.cache/go-mod} go test ./internal/auth -run 'TestCompatibility'
```

Expected: FAIL with undefined compatibility functions.

- [ ] **Step 3: Implement compatibility mapping**

Create `internal/auth/acl_compat.go`:

```go
package auth

import (
	"fmt"

	"github.com/Silo-Server/silo-server/internal/models"
)

func CompatibilityGroupsForUser(user *models.User) []BuiltInGroupSlug {
	if user == nil || !user.Enabled {
		return []BuiltInGroupSlug{}
	}
	if user.Role == "admin" {
		return []BuiltInGroupSlug{GroupAdmin}
	}
	return []BuiltInGroupSlug{GroupViewer}
}

func CompatibilityRulesForUser(user *models.User) []ACLRule {
	if user == nil || !user.Enabled {
		return []ACLRule{}
	}

	rules := make([]ACLRule, 0, len(user.Permissions)+4)
	subjectID := fmt.Sprintf("%d", user.ID)

	for _, permission := range user.Permissions {
		action := LegacyPermissionAction(Permission(permission))
		if action == "" {
			continue
		}
		rules = append(rules, ACLRule{
			SubjectType:  SubjectUser,
			SubjectID:    subjectID,
			Action:       action,
			ResourceType: ResourceServer,
			ResourceID:   "*",
			Effect:       EffectAllow,
			Priority:     1000,
			Name:         "legacy permission " + permission,
		})
	}

	if user.Role == "admin" {
		for _, action := range []ACLAction{
			ActionServerView,
			ActionServerConfigure,
			ActionSecurityManage,
			ActionUsersView,
			ActionUsersManage,
			ActionUsersImpersonate,
			ActionLibrariesView,
			ActionLibrariesManage,
			ActionTasksView,
			ActionTasksRun,
			ActionLogsView,
			ActionPluginsView,
			ActionPluginsManage,
			ActionNodesView,
			ActionNodesManage,
			ActionMetadataCurate,
			ActionMarkersEdit,
			ActionPlaybackPlay,
			ActionPlaybackTranscode,
			ActionDownloadsDirect,
			ActionDownloadsTranscode,
			ActionProfilesManage,
			ActionRequestsCreate,
			ActionRequestsApprove,
		} {
			rules = append(rules, ACLRule{
				SubjectType:  SubjectBuiltInRole,
				SubjectID:    string(GroupAdmin),
				Action:       action,
				ResourceType: ResourceServer,
				ResourceID:   "*",
				Effect:       EffectAllow,
				Priority:     100,
				Name:         "legacy admin grant",
			})
		}
	}

	return rules
}

func CompatibilityEffectivePolicyForUser(user *models.User) EffectivePolicy {
	if user == nil || !user.Enabled {
		return EffectivePolicy{}
	}
	return EffectivePolicy{
		LibraryIDs:                 append([]int(nil), user.LibraryIDs...),
		MaxPlaybackQuality:         user.MaxPlaybackQuality,
		MaxStreams:                 user.MaxStreams,
		MaxTranscodes:              user.MaxTranscodes,
		DirectDownloadsAllowed:     user.DownloadAllowed,
		TranscodedDownloadsAllowed: user.DownloadTranscodeAllowed,
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:

```bash
GOWORK=off GOCACHE=${SILO_GO_BUILD_CACHE:-.cache/go-build} GOMODCACHE=${SILO_GO_MOD_CACHE:-.cache/go-mod} go test ./internal/auth -run 'TestCompatibility'
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/auth/acl_compat.go internal/auth/acl_compat_test.go
git commit -m "Map legacy users into ACL compatibility policy"
```

---

### Task 3: ACL Evaluator and Explain Decisions

**Files:**
- Create: `internal/auth/acl_evaluator.go`
- Test: `internal/auth/acl_evaluator_test.go`

**Interfaces:**
- Consumes: `AccessRequest`, `ACLRule`, `EffectivePolicy`
- Produces: `type ACLEvaluator struct{}`
- Produces: `func NewACLEvaluator() *ACLEvaluator`
- Produces: `func (e *ACLEvaluator) Authorize(request AccessRequest, rules []ACLRule, basePolicy EffectivePolicy, userEnabled bool) AccessDecision`
- Produces: `func (e *ACLEvaluator) Explain(request AccessRequest, rules []ACLRule, basePolicy EffectivePolicy, userEnabled bool) AccessExplanation`

- [ ] **Step 1: Write failing evaluator tests**

Create `internal/auth/acl_evaluator_test.go`:

```go
package auth

import "testing"

func TestACLEvaluatorDisabledUserDenied(t *testing.T) {
	evaluator := NewACLEvaluator()
	decision := evaluator.Authorize(AccessRequest{Action: ActionPlaybackPlay, ResourceType: ResourceLibrary, ResourceID: "1"}, nil, EffectivePolicy{}, false)
	if decision.Allowed {
		t.Fatalf("disabled user should be denied")
	}
	if decision.ReasonCode != "user_disabled" {
		t.Fatalf("reason = %q, want user_disabled", decision.ReasonCode)
	}
}

func TestACLEvaluatorUserDenyBeatsUserAllow(t *testing.T) {
	evaluator := NewACLEvaluator()
	request := AccessRequest{UserID: 7, Action: ActionDownloadsDirect, ResourceType: ResourceLibrary, ResourceID: "10"}
	rules := []ACLRule{
		{ID: 1, SubjectType: SubjectUser, SubjectID: "7", Action: ActionDownloadsDirect, ResourceType: ResourceLibrary, ResourceID: "10", Effect: EffectAllow, Priority: 10, Name: "allow direct downloads"},
		{ID: 2, SubjectType: SubjectUser, SubjectID: "7", Action: ActionDownloadsDirect, ResourceType: ResourceLibrary, ResourceID: "10", Effect: EffectDeny, Priority: 10, Name: "deny direct downloads"},
	}

	decision := evaluator.Authorize(request, rules, EffectivePolicy{}, true)
	if decision.Allowed {
		t.Fatalf("user deny should win over user allow")
	}
	if decision.WinningRule == nil || decision.WinningRule.ID != 2 {
		t.Fatalf("winning rule = %#v, want rule 2", decision.WinningRule)
	}
}

func TestACLEvaluatorUserAllowBeatsGroupDeny(t *testing.T) {
	evaluator := NewACLEvaluator()
	request := AccessRequest{UserID: 7, Action: ActionMetadataCurate, ResourceType: ResourceLibrary, ResourceID: "10"}
	rules := []ACLRule{
		{ID: 1, SubjectType: SubjectGroup, SubjectID: "curators", Action: ActionMetadataCurate, ResourceType: ResourceLibrary, ResourceID: "10", Effect: EffectDeny, Priority: 10, Name: "group deny"},
		{ID: 2, SubjectType: SubjectUser, SubjectID: "7", Action: ActionMetadataCurate, ResourceType: ResourceLibrary, ResourceID: "10", Effect: EffectAllow, Priority: 10, Name: "user override"},
	}

	decision := evaluator.Authorize(request, rules, EffectivePolicy{}, true)
	if !decision.Allowed {
		t.Fatalf("user allow should beat group deny: %#v", decision)
	}
	if decision.WinningRule == nil || decision.WinningRule.ID != 2 {
		t.Fatalf("winning rule = %#v, want rule 2", decision.WinningRule)
	}
}

func TestACLEvaluatorExplainIncludesMatchedRules(t *testing.T) {
	evaluator := NewACLEvaluator()
	request := AccessRequest{UserID: 7, Action: ActionPlaybackPlay, ResourceType: ResourceLibrary, ResourceID: "10"}
	rules := []ACLRule{
		{ID: 1, SubjectType: SubjectGroup, SubjectID: "viewer", Action: ActionPlaybackPlay, ResourceType: ResourceLibrary, ResourceID: "10", Effect: EffectAllow, Priority: 10, Name: "viewer playback"},
	}

	explanation := evaluator.Explain(request, rules, EffectivePolicy{MaxStreams: 2}, true)
	if !explanation.Decision.Allowed {
		t.Fatalf("expected allowed decision: %#v", explanation.Decision)
	}
	if len(explanation.Decision.MatchedRules) != 1 {
		t.Fatalf("matched rules = %#v, want one rule", explanation.Decision.MatchedRules)
	}
	if explanation.Decision.EffectivePolicy.MaxStreams != 2 {
		t.Fatalf("effective max streams = %d, want 2", explanation.Decision.EffectivePolicy.MaxStreams)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
GOWORK=off GOCACHE=${SILO_GO_BUILD_CACHE:-.cache/go-build} GOMODCACHE=${SILO_GO_MOD_CACHE:-.cache/go-mod} go test ./internal/auth -run 'TestACLEvaluator'
```

Expected: FAIL with undefined `NewACLEvaluator`.

- [ ] **Step 3: Implement evaluator**

Create `internal/auth/acl_evaluator.go`:

```go
package auth

import (
	"fmt"
	"sort"
)

type ACLEvaluator struct{}

func NewACLEvaluator() *ACLEvaluator {
	return &ACLEvaluator{}
}

func (e *ACLEvaluator) Authorize(request AccessRequest, rules []ACLRule, basePolicy EffectivePolicy, userEnabled bool) AccessDecision {
	if !userEnabled {
		return AccessDecision{Allowed: false, ReasonCode: "user_disabled", EffectivePolicy: basePolicy}
	}

	matched := matchingRules(request, rules)
	sortMatchedRules(matched)
	if len(matched) == 0 {
		return AccessDecision{Allowed: false, ReasonCode: "default_deny", MatchedRules: matched, EffectivePolicy: basePolicy}
	}

	winning := matched[0]
	allowed := winning.Effect == EffectAllow
	reason := "rule_allow"
	if !allowed {
		reason = "rule_deny"
	}
	return AccessDecision{
		Allowed:         allowed,
		ReasonCode:      reason,
		WinningRule:     &winning,
		MatchedRules:    matched,
		EffectivePolicy: basePolicy,
	}
}

func (e *ACLEvaluator) Explain(request AccessRequest, rules []ACLRule, basePolicy EffectivePolicy, userEnabled bool) AccessExplanation {
	decision := e.Authorize(request, rules, basePolicy, userEnabled)
	return AccessExplanation{
		Request:        request,
		Decision:       decision,
		EvaluatedRules: append([]ACLRule(nil), rules...),
	}
}

func matchingRules(request AccessRequest, rules []ACLRule) []ACLRule {
	out := make([]ACLRule, 0, len(rules))
	for _, rule := range rules {
		if rule.Action != request.Action {
			continue
		}
		if !resourceMatches(request, rule) {
			continue
		}
		if !conditionsMatch(request, rule.Conditions) {
			continue
		}
		out = append(out, rule)
	}
	return out
}

func resourceMatches(request AccessRequest, rule ACLRule) bool {
	if rule.ResourceType != request.ResourceType && rule.ResourceType != ResourceServer {
		return false
	}
	if rule.ResourceID == "" || rule.ResourceID == "*" {
		return true
	}
	if request.ResourceID == "" {
		return false
	}
	return rule.ResourceID == request.ResourceID
}

func conditionsMatch(request AccessRequest, conditions ACLCondition) bool {
	if len(conditions.LibraryIDs) > 0 {
		ok := false
		for _, allowed := range conditions.LibraryIDs {
			for _, requested := range request.LibraryIDs {
				if allowed == requested {
					ok = true
					break
				}
			}
			if ok {
				break
			}
		}
		if !ok {
			return false
		}
	}

	if len(conditions.MediaTypes) > 0 {
		ok := false
		for _, mediaType := range conditions.MediaTypes {
			if mediaType == request.MediaType {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}

	if conditions.PrimaryProfileRequired != nil && *conditions.PrimaryProfileRequired != request.PrimaryProfile {
		return false
	}

	return true
}

func sortMatchedRules(rules []ACLRule) {
	sort.SliceStable(rules, func(i, j int) bool {
		left := ruleRank(rules[i])
		right := ruleRank(rules[j])
		if left != right {
			return left < right
		}
		if rules[i].Priority != rules[j].Priority {
			return rules[i].Priority > rules[j].Priority
		}
		return rules[i].ID < rules[j].ID
	})
}

func ruleRank(rule ACLRule) int {
	switch {
	case rule.SubjectType == SubjectBuiltInRole && rule.SubjectID == string(GroupOwner) && rule.Effect == EffectAllow:
		return 0
	case rule.SubjectType == SubjectUser && rule.Effect == EffectDeny:
		return 1
	case rule.SubjectType == SubjectUser && rule.Effect == EffectAllow:
		return 2
	case rule.SubjectType == SubjectGroup && rule.Effect == EffectDeny:
		return 3
	case rule.SubjectType == SubjectGroup && rule.Effect == EffectAllow:
		return 4
	case rule.Effect == EffectDeny:
		return 5
	case rule.Effect == EffectAllow:
		return 6
	default:
		panic(fmt.Sprintf("unknown ACL effect %q", rule.Effect))
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:

```bash
GOWORK=off GOCACHE=${SILO_GO_BUILD_CACHE:-.cache/go-build} GOMODCACHE=${SILO_GO_MOD_CACHE:-.cache/go-mod} go test ./internal/auth -run 'TestACLEvaluator'
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/auth/acl_evaluator.go internal/auth/acl_evaluator_test.go
git commit -m "Add ACL evaluator"
```

---

### Task 4: ACL Database Foundation

**Files:**
- Create: `migrations/sql/20260629193000_acl_foundation.sql`
- Create: `internal/auth/acl_repository.go`
- Test: `internal/auth/acl_repository_test.go`

**Interfaces:**
- Consumes: `ACLRule`, constants from Task 1
- Produces: `type ACLRepository struct`
- Produces: `func NewACLRepository(db *pgxpool.Pool) *ACLRepository`
- Produces: `func (r *ACLRepository) ListRulesForUser(ctx context.Context, userID int) ([]ACLRule, error)`
- Produces: `func (r *ACLRepository) CurrentPolicyRevision(ctx context.Context) (int64, error)`

- [ ] **Step 1: Add repository unit test for SQL row scanning**

Create `internal/auth/acl_repository_test.go`:

```go
package auth

import "testing"

func TestScanACLRuleFields(t *testing.T) {
	row := aclRuleRow{
		ID:           42,
		SubjectType:  "group",
		SubjectID:    "viewer",
		Action:       "playback.play",
		ResourceType: "library",
		ResourceID:   "10",
		Effect:       "allow",
		Priority:     5,
		Name:         "viewer playback",
		Description:  "allows library playback",
	}

	rule := row.toRule()
	if rule.ID != 42 {
		t.Fatalf("id = %d, want 42", rule.ID)
	}
	if rule.SubjectType != SubjectGroup {
		t.Fatalf("subject type = %q, want %q", rule.SubjectType, SubjectGroup)
	}
	if rule.Action != ActionPlaybackPlay {
		t.Fatalf("action = %q, want %q", rule.Action, ActionPlaybackPlay)
	}
	if rule.ResourceType != ResourceLibrary {
		t.Fatalf("resource type = %q, want %q", rule.ResourceType, ResourceLibrary)
	}
	if rule.Effect != EffectAllow {
		t.Fatalf("effect = %q, want %q", rule.Effect, EffectAllow)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
GOWORK=off GOCACHE=${SILO_GO_BUILD_CACHE:-.cache/go-build} GOMODCACHE=${SILO_GO_MOD_CACHE:-.cache/go-mod} go test ./internal/auth -run 'TestScanACLRuleFields'
```

Expected: FAIL with undefined `aclRuleRow`.

- [ ] **Step 3: Add ACL migration**

Create `migrations/sql/20260629193000_acl_foundation.sql`:

```sql
-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS public.acl_groups (
    id bigserial PRIMARY KEY,
    slug text NOT NULL UNIQUE,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    built_in boolean NOT NULL DEFAULT false,
    protected boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS public.acl_group_members (
    group_id bigint NOT NULL REFERENCES public.acl_groups(id) ON DELETE CASCADE,
    user_id integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, user_id)
);

CREATE TABLE IF NOT EXISTS public.acl_rules (
    id bigserial PRIMARY KEY,
    subject_type text NOT NULL,
    subject_id text NOT NULL,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL DEFAULT '*',
    effect text NOT NULL,
    conditions jsonb NOT NULL DEFAULT '{}'::jsonb,
    priority integer NOT NULL DEFAULT 0,
    name text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT acl_rules_effect_check CHECK (effect IN ('allow', 'deny')),
    CONSTRAINT acl_rules_subject_type_check CHECK (subject_type IN ('user', 'group', 'builtin_role', 'everyone'))
);

CREATE INDEX IF NOT EXISTS acl_group_members_user_idx ON public.acl_group_members(user_id);
CREATE INDEX IF NOT EXISTS acl_rules_subject_idx ON public.acl_rules(subject_type, subject_id);
CREATE INDEX IF NOT EXISTS acl_rules_action_resource_idx ON public.acl_rules(action, resource_type, resource_id);

CREATE TABLE IF NOT EXISTS public.acl_policy_revisions (
    id boolean PRIMARY KEY DEFAULT true,
    revision bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT acl_policy_revisions_singleton CHECK (id)
);

INSERT INTO public.acl_policy_revisions (id, revision)
VALUES (true, 1)
ON CONFLICT (id) DO NOTHING;

INSERT INTO public.acl_groups (slug, name, description, built_in, protected)
VALUES
    ('owner', 'Owner', 'Full server ownership and security control.', true, true),
    ('admin', 'Admin', 'Broad operational administration.', true, true),
    ('library_manager', 'Library Manager', 'Library and scan management.', true, false),
    ('metadata_curator', 'Metadata Curator', 'Metadata, poster, marker, and provider curation.', true, false),
    ('viewer', 'Viewer', 'Normal media playback access.', true, false),
    ('restricted_viewer', 'Restricted Viewer', 'Playback access with tighter limits.', true, false)
ON CONFLICT (slug) DO UPDATE
SET name = EXCLUDED.name,
    description = EXCLUDED.description,
    built_in = EXCLUDED.built_in,
    protected = EXCLUDED.protected,
    updated_at = now();

WITH oldest_admin AS (
    SELECT id
    FROM public.users
    WHERE enabled = true AND role = 'admin'
    ORDER BY created_at ASC, id ASC
    LIMIT 1
),
owner_group AS (
    SELECT id FROM public.acl_groups WHERE slug = 'owner'
),
admin_group AS (
    SELECT id FROM public.acl_groups WHERE slug = 'admin'
),
viewer_group AS (
    SELECT id FROM public.acl_groups WHERE slug = 'viewer'
)
INSERT INTO public.acl_group_members (group_id, user_id)
SELECT owner_group.id, oldest_admin.id
FROM owner_group, oldest_admin
ON CONFLICT DO NOTHING;

WITH oldest_admin AS (
    SELECT id
    FROM public.users
    WHERE enabled = true AND role = 'admin'
    ORDER BY created_at ASC, id ASC
    LIMIT 1
),
admin_group AS (
    SELECT id FROM public.acl_groups WHERE slug = 'admin'
)
INSERT INTO public.acl_group_members (group_id, user_id)
SELECT admin_group.id, users.id
FROM public.users, admin_group
WHERE users.enabled = true
  AND users.role = 'admin'
  AND users.id NOT IN (SELECT id FROM oldest_admin)
ON CONFLICT DO NOTHING;

WITH viewer_group AS (
    SELECT id FROM public.acl_groups WHERE slug = 'viewer'
)
INSERT INTO public.acl_group_members (group_id, user_id)
SELECT viewer_group.id, users.id
FROM public.users, viewer_group
WHERE users.enabled = true
  AND COALESCE(users.role, 'user') <> 'admin'
ON CONFLICT DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.acl_policy_revisions;
DROP TABLE IF EXISTS public.acl_rules;
DROP TABLE IF EXISTS public.acl_group_members;
DROP TABLE IF EXISTS public.acl_groups;
-- +goose StatementEnd
```

- [ ] **Step 4: Add repository scanner and methods**

Create `internal/auth/acl_repository.go`:

```go
package auth

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"
)

type ACLRepository struct {
	db *pgxpool.Pool
}

func NewACLRepository(db *pgxpool.Pool) *ACLRepository {
	return &ACLRepository{db: db}
}

type aclRuleRow struct {
	ID           int64
	SubjectType  string
	SubjectID    string
	Action       string
	ResourceType string
	ResourceID   string
	Effect       string
	Conditions   []byte
	Priority     int
	Name         string
	Description  string
}

func (row aclRuleRow) toRule() ACLRule {
	var conditions ACLCondition
	if len(row.Conditions) > 0 {
		_ = json.Unmarshal(row.Conditions, &conditions)
	}
	return ACLRule{
		ID:           row.ID,
		SubjectType:  ACLSubjectType(row.SubjectType),
		SubjectID:    row.SubjectID,
		Action:       ACLAction(row.Action),
		ResourceType: ACLResourceType(row.ResourceType),
		ResourceID:   row.ResourceID,
		Effect:       ACLEffect(row.Effect),
		Conditions:   conditions,
		Priority:     row.Priority,
		Name:         row.Name,
		Description:  row.Description,
	}
}

func (r *ACLRepository) ListRulesForUser(ctx context.Context, userID int) ([]ACLRule, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, subject_type, subject_id, action, resource_type, resource_id, effect, conditions, priority, name, description
		FROM public.acl_rules
		WHERE (subject_type = 'user' AND subject_id = $1::text)
		   OR (subject_type = 'group' AND subject_id IN (
		       SELECT g.slug
		       FROM public.acl_groups g
		       JOIN public.acl_group_members gm ON gm.group_id = g.id
		       WHERE gm.user_id = $2
		   ))
		   OR subject_type = 'everyone'
		ORDER BY priority DESC, id ASC
	`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rules := []ACLRule{}
	for rows.Next() {
		var row aclRuleRow
		if err := rows.Scan(&row.ID, &row.SubjectType, &row.SubjectID, &row.Action, &row.ResourceType, &row.ResourceID, &row.Effect, &row.Conditions, &row.Priority, &row.Name, &row.Description); err != nil {
			return nil, err
		}
		rules = append(rules, row.toRule())
	}
	return rules, rows.Err()
}

func (r *ACLRepository) CurrentPolicyRevision(ctx context.Context) (int64, error) {
	var revision int64
	err := r.db.QueryRow(ctx, `SELECT revision FROM public.acl_policy_revisions WHERE id = true`).Scan(&revision)
	return revision, err
}
```

- [ ] **Step 5: Run repository tests**

Run:

```bash
GOWORK=off GOCACHE=${SILO_GO_BUILD_CACHE:-.cache/go-build} GOMODCACHE=${SILO_GO_MOD_CACHE:-.cache/go-mod} go test ./internal/auth -run 'TestScanACLRuleFields'
```

Expected: PASS.

- [ ] **Step 6: Run full auth tests**

Run:

```bash
GOWORK=off GOCACHE=${SILO_GO_BUILD_CACHE:-.cache/go-build} GOMODCACHE=${SILO_GO_MOD_CACHE:-.cache/go-mod} go test ./internal/auth
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add migrations/sql/20260629193000_acl_foundation.sql internal/auth/acl_repository.go internal/auth/acl_repository_test.go
git commit -m "Add ACL persistence foundation"
```

---

### Task 5: Authorizer Facade for Future Middleware

**Files:**
- Create: `internal/auth/acl_authorizer.go`
- Test: `internal/auth/acl_authorizer_test.go`

**Interfaces:**
- Consumes: `ACLRepository`
- Consumes: `ACLEvaluator`
- Consumes: `CompatibilityRulesForUser`
- Consumes: `CompatibilityEffectivePolicyForUser`
- Produces: `type Authorizer interface`
- Produces: `func NewACLAuthorizer(rules ACLRuleLoader, users UserLoaderForACL) *ACLAuthorizer`
- Produces: `func (a *ACLAuthorizer) Authorize(ctx context.Context, request AccessRequest) (AccessDecision, error)`
- Produces: `func (a *ACLAuthorizer) Explain(ctx context.Context, request AccessRequest) (AccessExplanation, error)`

- [ ] **Step 1: Write failing facade test with fakes**

Create `internal/auth/acl_authorizer_test.go`:

```go
package auth

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeACLUserLoader struct {
	user *models.User
}

func (f fakeACLUserLoader) GetByID(ctx context.Context, id int) (*models.User, error) {
	return f.user, nil
}

type fakeACLRuleLoader struct {
	rules []ACLRule
}

func (f fakeACLRuleLoader) ListRulesForUser(ctx context.Context, userID int) ([]ACLRule, error) {
	return f.rules, nil
}

func TestACLAuthorizerCombinesRepositoryAndCompatibilityRules(t *testing.T) {
	user := &models.User{ID: 7, Role: "user", Enabled: true, Permissions: []string{"marker_edit"}, MaxStreams: 2}
	ruleLoader := fakeACLRuleLoader{
		rules: []ACLRule{
			{ID: 99, SubjectType: SubjectGroup, SubjectID: "viewer", Action: ActionPlaybackPlay, ResourceType: ResourceLibrary, ResourceID: "10", Effect: EffectAllow, Priority: 1, Name: "viewer playback"},
		},
	}

	authorizer := NewACLAuthorizer(ruleLoader, fakeACLUserLoader{user: user})
	decision, err := authorizer.Authorize(context.Background(), AccessRequest{UserID: 7, Action: ActionMarkersEdit, ResourceType: ResourceServer, ResourceID: "*"})
	if err != nil {
		t.Fatalf("authorize error: %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("legacy marker_edit should allow markers.edit: %#v", decision)
	}
	if decision.EffectivePolicy.MaxStreams != 2 {
		t.Fatalf("effective max streams = %d, want 2", decision.EffectivePolicy.MaxStreams)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
GOWORK=off GOCACHE=${SILO_GO_BUILD_CACHE:-.cache/go-build} GOMODCACHE=${SILO_GO_MOD_CACHE:-.cache/go-mod} go test ./internal/auth -run 'TestACLAuthorizer'
```

Expected: FAIL with undefined `NewACLAuthorizer`.

- [ ] **Step 3: Implement authorizer facade**

Create `internal/auth/acl_authorizer.go`:

```go
package auth

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/models"
)

type Authorizer interface {
	Authorize(ctx context.Context, request AccessRequest) (AccessDecision, error)
	Explain(ctx context.Context, request AccessRequest) (AccessExplanation, error)
}

type UserLoaderForACL interface {
	GetByID(ctx context.Context, id int) (*models.User, error)
}

type ACLRuleLoader interface {
	ListRulesForUser(ctx context.Context, userID int) ([]ACLRule, error)
}

type ACLAuthorizer struct {
	rules     ACLRuleLoader
	users     UserLoaderForACL
	evaluator *ACLEvaluator
}

func NewACLAuthorizer(rules ACLRuleLoader, users UserLoaderForACL) *ACLAuthorizer {
	return &ACLAuthorizer{
		rules:     rules,
		users:     users,
		evaluator: NewACLEvaluator(),
	}
}

func (a *ACLAuthorizer) Authorize(ctx context.Context, request AccessRequest) (AccessDecision, error) {
	user, rules, policy, err := a.loadInputs(ctx, request.UserID)
	if err != nil {
		return AccessDecision{}, err
	}
	return a.evaluator.Authorize(request, rules, policy, user != nil && user.Enabled), nil
}

func (a *ACLAuthorizer) Explain(ctx context.Context, request AccessRequest) (AccessExplanation, error) {
	user, rules, policy, err := a.loadInputs(ctx, request.UserID)
	if err != nil {
		return AccessExplanation{}, err
	}
	return a.evaluator.Explain(request, rules, policy, user != nil && user.Enabled), nil
}

func (a *ACLAuthorizer) loadInputs(ctx context.Context, userID int) (*models.User, []ACLRule, EffectivePolicy, error) {
	user, err := a.users.GetByID(ctx, userID)
	if err != nil {
		return nil, nil, EffectivePolicy{}, err
	}

	rules := []ACLRule{}
	if a.rules != nil {
		repositoryRules, err := a.rules.ListRulesForUser(ctx, userID)
		if err != nil {
			return nil, nil, EffectivePolicy{}, err
		}
		rules = append(rules, repositoryRules...)
	}
	rules = append(rules, CompatibilityRulesForUser(user)...)

	return user, rules, CompatibilityEffectivePolicyForUser(user), nil
}
```

- [ ] **Step 4: Run facade tests**

Run:

```bash
GOWORK=off GOCACHE=${SILO_GO_BUILD_CACHE:-.cache/go-build} GOMODCACHE=${SILO_GO_MOD_CACHE:-.cache/go-mod} go test ./internal/auth -run 'TestACLAuthorizer'
```

Expected: PASS.

- [ ] **Step 5: Run full auth tests**

Run:

```bash
GOWORK=off GOCACHE=${SILO_GO_BUILD_CACHE:-.cache/go-build} GOMODCACHE=${SILO_GO_MOD_CACHE:-.cache/go-mod} go test ./internal/auth
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/auth/acl_authorizer.go internal/auth/acl_authorizer_test.go
git commit -m "Add ACL authorizer facade"
```

---

### Task 6: Foundation Verification and Documentation Update

**Files:**
- Modify: `docs/superpowers/specs/2026-06-29-security-acl-design.md`
- Test: full auth and middleware package tests

**Interfaces:**
- Consumes: all previous tasks
- Produces: verified foundation branch ready for route-conversion planning

- [ ] **Step 1: Add implementation note to spec**

Append this section to `docs/superpowers/specs/2026-06-29-security-acl-design.md`:

```markdown

## Foundation Implementation Notes

The first implementation pass adds the ACL data model, constants, evaluator, compatibility mapping, repository, and authorizer facade without converting existing route middleware. Current behavior remains controlled by the legacy role and permission paths until route-specific ACL checks are introduced in later PRs.
```

- [ ] **Step 2: Run auth package tests**

Run:

```bash
GOWORK=off GOCACHE=${SILO_GO_BUILD_CACHE:-.cache/go-build} GOMODCACHE=${SILO_GO_MOD_CACHE:-.cache/go-mod} go test ./internal/auth
```

Expected: PASS.

- [ ] **Step 3: Run middleware package tests**

Run:

```bash
GOWORK=off GOCACHE=${SILO_GO_BUILD_CACHE:-.cache/go-build} GOMODCACHE=${SILO_GO_MOD_CACHE:-.cache/go-mod} go test ./internal/api/middleware
```

Expected: PASS.

- [ ] **Step 4: Run migration-aware package compile check**

Run:

```bash
GOWORK=off GOCACHE=${SILO_GO_BUILD_CACHE:-.cache/go-build} GOMODCACHE=${SILO_GO_MOD_CACHE:-.cache/go-mod} go test ./internal/auth ./internal/api/middleware ./internal/api/handlers
```

Expected: PASS.

- [ ] **Step 5: Commit verification note**

```bash
git add docs/superpowers/specs/2026-06-29-security-acl-design.md
git commit -m "Document ACL foundation rollout boundary"
```

---

## Self-Review

Spec coverage:

- Full ACL internally: covered by Tasks 1, 3, 4, and 5.
- Group-oriented compatibility: covered by Tasks 2 and 4.
- Behavior-preserving rollout: covered by Tasks 2, 5, and 6.
- Explain access: covered by Task 3 and Task 5.
- Data model: covered by Task 4.
- Session/cache invalidation table foundation: covered by Task 4 through `acl_policy_revisions`.
- UI: intentionally deferred because the foundation plan has no route behavior or frontend behavior changes.
- Route conversion: intentionally deferred to the next implementation plan after this foundation is merged.

Completion scan:

- The plan gives exact file paths, test names, commands, expected outputs, and code for each implementation step.

Type consistency:

- `ACLAction`, `ACLResourceType`, `ACLEffect`, `ACLSubjectType`, `BuiltInGroupSlug`, `AccessRequest`, `ACLRule`, `ACLCondition`, `AccessDecision`, `AccessExplanation`, and `EffectivePolicy` are introduced in Task 1 and reused consistently by Tasks 2 through 6.
