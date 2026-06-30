package auth

import (
	"fmt"
	"strings"
)

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
	GroupOwner           BuiltInGroupSlug = "owner"
	GroupAdmin           BuiltInGroupSlug = "admin"
	GroupLibraryManager  BuiltInGroupSlug = "library_manager"
	GroupMetadataCurator BuiltInGroupSlug = "metadata_curator"
	GroupStandardUser    BuiltInGroupSlug = "standard_user"
	GroupRestrictedUser  BuiltInGroupSlug = "restricted_user"
)

const (
	ActionServerView            ACLAction = "server.view"
	ActionServerConfigure       ACLAction = "server.configure"
	ActionSecurityManage        ACLAction = "security.manage"
	ActionUsersView             ACLAction = "users.view"
	ActionUsersManage           ACLAction = "users.manage"
	ActionUsersImpersonate      ACLAction = "users.impersonate"
	ActionLibrariesView         ACLAction = "libraries.view"
	ActionLibrariesManage       ACLAction = "libraries.manage"
	ActionTasksView             ACLAction = "tasks.view"
	ActionTasksRun              ACLAction = "tasks.run"
	ActionLogsView              ACLAction = "logs.view"
	ActionPluginsView           ACLAction = "plugins.view"
	ActionPluginsManage         ACLAction = "plugins.manage"
	ActionNodesView             ACLAction = "nodes.view"
	ActionNodesManage           ACLAction = "nodes.manage"
	ActionRecommendationsView   ACLAction = "recommendations.view"
	ActionRecommendationsManage ACLAction = "recommendations.manage"
	ActionMetadataCurate        ACLAction = "metadata.curate"
	ActionMarkersEdit           ACLAction = "markers.edit"
	ActionPlaybackPlay          ACLAction = "playback.play"
	ActionPlaybackTranscode     ACLAction = "playback.transcode"
	ActionDownloadsDirect       ACLAction = "downloads.direct"
	ActionDownloadsTranscode    ACLAction = "downloads.transcode"
	ActionProfilesManage        ACLAction = "profiles.manage"
	ActionPersonalListsManage   ACLAction = "personal_lists.manage"
	ActionRequestsCreate        ACLAction = "requests.create"
	ActionRequestsApprove       ACLAction = "requests.approve"
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

func DefaultACLGroupSlugsForRole(role string) []string {
	if strings.TrimSpace(strings.ToLower(role)) == "admin" {
		return []string{string(GroupAdmin)}
	}
	return []string{string(GroupStandardUser)}
}

func NormalizeACLGroupSlug(raw string) (string, error) {
	slug := strings.TrimSpace(strings.ToLower(raw))
	if !validACLGroupSlug(slug) {
		return "", fmt.Errorf("invalid ACL group slug %q", raw)
	}
	return slug, nil
}

func NormalizeACLGroupSlugs(slugs []string) ([]string, error) {
	out := make([]string, 0, len(slugs))
	seen := map[string]struct{}{}
	for _, raw := range slugs {
		slug := strings.TrimSpace(strings.ToLower(raw))
		if slug == "" {
			continue
		}
		if !validACLGroupSlug(slug) {
			return nil, fmt.Errorf("invalid ACL group slug %q", slug)
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		out = append(out, slug)
	}
	return out, nil
}

func validACLGroupSlug(slug string) bool {
	for i, r := range slug {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		case (r == '_' || r == '-') && i > 0:
		default:
			return false
		}
	}
	return slug != ""
}

func NormalizeACLRuleInput(input ACLRuleInput) (ACLRuleInput, error) {
	out := input
	out.Action = ACLAction(strings.TrimSpace(strings.ToLower(string(input.Action))))
	out.ResourceType = ACLResourceType(strings.TrimSpace(strings.ToLower(string(input.ResourceType))))
	out.ResourceID = strings.TrimSpace(input.ResourceID)
	out.Effect = ACLEffect(strings.TrimSpace(strings.ToLower(string(input.Effect))))
	out.Name = strings.TrimSpace(input.Name)
	out.Description = strings.TrimSpace(input.Description)
	out.Conditions = normalizeACLCondition(input.Conditions)

	if !ValidACLAction(out.Action) {
		return ACLRuleInput{}, fmt.Errorf("invalid ACL action %q", input.Action)
	}
	if !ValidACLResourceType(out.ResourceType) {
		return ACLRuleInput{}, fmt.Errorf("invalid ACL resource type %q", input.ResourceType)
	}
	if out.ResourceID == "" {
		out.ResourceID = "*"
	}
	if out.Effect == "" {
		out.Effect = EffectAllow
	}
	if out.Effect != EffectAllow && out.Effect != EffectDeny {
		return ACLRuleInput{}, fmt.Errorf("invalid ACL effect %q", input.Effect)
	}
	if out.Conditions.MaxPlaybackQuality != "" {
		if _, ok := playbackQualityRank(out.Conditions.MaxPlaybackQuality); !ok {
			return ACLRuleInput{}, fmt.Errorf("invalid ACL max playback quality %q", out.Conditions.MaxPlaybackQuality)
		}
	}
	if out.Conditions.MaxContentRating != "" {
		if _, ok := contentRatingRank(out.Conditions.MaxContentRating); !ok {
			return ACLRuleInput{}, fmt.Errorf("invalid ACL max content rating %q", out.Conditions.MaxContentRating)
		}
	}
	if out.Conditions.MaxStreams != nil && *out.Conditions.MaxStreams < 0 {
		return ACLRuleInput{}, fmt.Errorf("invalid ACL max streams %d", *out.Conditions.MaxStreams)
	}
	if out.Conditions.MaxTranscodes != nil && *out.Conditions.MaxTranscodes < 0 {
		return ACLRuleInput{}, fmt.Errorf("invalid ACL max transcodes %d", *out.Conditions.MaxTranscodes)
	}
	return out, nil
}

func NormalizeACLRuleInputs(inputs []ACLRuleInput) ([]ACLRuleInput, error) {
	out := make([]ACLRuleInput, 0, len(inputs))
	for _, input := range inputs {
		normalized, err := NormalizeACLRuleInput(input)
		if err != nil {
			return nil, err
		}
		out = append(out, normalized)
	}
	return out, nil
}

func NormalizeACLPolicy(input ACLPolicy) (ACLPolicy, error) {
	out := input
	if out.LibraryIDs != nil {
		libraryIDs := make([]int, 0, len(out.LibraryIDs))
		seen := map[int]struct{}{}
		for _, id := range out.LibraryIDs {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			libraryIDs = append(libraryIDs, id)
		}
		out.LibraryIDs = libraryIDs
	}
	if out.MediaTypes != nil {
		mediaTypes := make([]string, 0, len(out.MediaTypes))
		seen := map[string]struct{}{}
		for _, raw := range out.MediaTypes {
			mediaType := strings.TrimSpace(strings.ToLower(raw))
			if mediaType == "" {
				continue
			}
			if _, ok := seen[mediaType]; ok {
				continue
			}
			seen[mediaType] = struct{}{}
			mediaTypes = append(mediaTypes, mediaType)
		}
		out.MediaTypes = mediaTypes
	}
	out.MaxPlaybackQuality = strings.ToUpper(strings.TrimSpace(out.MaxPlaybackQuality))
	if out.MaxPlaybackQuality != "" {
		if _, ok := playbackQualityRank(out.MaxPlaybackQuality); !ok {
			return ACLPolicy{}, fmt.Errorf("invalid ACL max playback quality %q", out.MaxPlaybackQuality)
		}
	}
	if out.MaxStreams != nil && *out.MaxStreams < 0 {
		return ACLPolicy{}, fmt.Errorf("invalid ACL max streams %d", *out.MaxStreams)
	}
	if out.MaxTranscodes != nil && *out.MaxTranscodes < 0 {
		return ACLPolicy{}, fmt.Errorf("invalid ACL max transcodes %d", *out.MaxTranscodes)
	}
	if out.MaxProfiles != nil && *out.MaxProfiles < 1 {
		return ACLPolicy{}, fmt.Errorf("invalid ACL max profiles %d", *out.MaxProfiles)
	}
	return out, nil
}

func ValidACLAction(action ACLAction) bool {
	_, ok := validACLActions[action]
	return ok
}

func ValidACLResourceType(resourceType ACLResourceType) bool {
	_, ok := validACLResourceTypes[resourceType]
	return ok
}

func normalizeACLCondition(condition ACLCondition) ACLCondition {
	out := condition
	if out.MediaTypes != nil {
		mediaTypes := make([]string, 0, len(out.MediaTypes))
		seen := map[string]struct{}{}
		for _, raw := range out.MediaTypes {
			mediaType := strings.TrimSpace(strings.ToLower(raw))
			if mediaType == "" {
				continue
			}
			if _, ok := seen[mediaType]; ok {
				continue
			}
			seen[mediaType] = struct{}{}
			mediaTypes = append(mediaTypes, mediaType)
		}
		out.MediaTypes = mediaTypes
	}
	out.MaxPlaybackQuality = strings.ToUpper(strings.TrimSpace(out.MaxPlaybackQuality))
	out.MaxContentRating = strings.ToUpper(strings.TrimSpace(out.MaxContentRating))
	return out
}

var validACLActions = map[ACLAction]struct{}{
	ActionServerView:            {},
	ActionServerConfigure:       {},
	ActionSecurityManage:        {},
	ActionUsersView:             {},
	ActionUsersManage:           {},
	ActionUsersImpersonate:      {},
	ActionLibrariesView:         {},
	ActionLibrariesManage:       {},
	ActionTasksView:             {},
	ActionTasksRun:              {},
	ActionLogsView:              {},
	ActionPluginsView:           {},
	ActionPluginsManage:         {},
	ActionNodesView:             {},
	ActionNodesManage:           {},
	ActionRecommendationsView:   {},
	ActionRecommendationsManage: {},
	ActionMetadataCurate:        {},
	ActionMarkersEdit:           {},
	ActionPlaybackPlay:          {},
	ActionPlaybackTranscode:     {},
	ActionDownloadsDirect:       {},
	ActionDownloadsTranscode:    {},
	ActionProfilesManage:        {},
	ActionPersonalListsManage:   {},
	ActionRequestsCreate:        {},
	ActionRequestsApprove:       {},
}

var validACLResourceTypes = map[ACLResourceType]struct{}{
	ResourceServer:           {},
	ResourceSecuritySettings: {},
	ResourceUser:             {},
	ResourceGroup:            {},
	ResourceLibrary:          {},
	ResourceMediaItem:        {},
	ResourceMediaType:        {},
	ResourceTask:             {},
	ResourceLog:              {},
	ResourcePlugin:           {},
	ResourceRemoteNode:       {},
	ResourceProfile:          {},
	ResourceRequest:          {},
}

func AdminCapabilityActions() []ACLAction {
	return []ACLAction{
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
		ActionRecommendationsView,
		ActionRecommendationsManage,
		ActionProfilesManage,
		ActionRequestsApprove,
	}
}

func ExplainableCapabilityActions() []ACLAction {
	return []ACLAction{
		ActionServerView,
		ActionServerConfigure,
		ActionSecurityManage,
		ActionLogsView,
		ActionUsersView,
		ActionUsersManage,
		ActionUsersImpersonate,
		ActionProfilesManage,
		ActionPersonalListsManage,
		ActionRequestsCreate,
		ActionRequestsApprove,
		ActionLibrariesView,
		ActionLibrariesManage,
		ActionMetadataCurate,
		ActionMarkersEdit,
		ActionRecommendationsView,
		ActionRecommendationsManage,
		ActionTasksView,
		ActionTasksRun,
		ActionPluginsView,
		ActionPluginsManage,
		ActionNodesView,
		ActionNodesManage,
		ActionPlaybackPlay,
		ActionPlaybackTranscode,
		ActionDownloadsDirect,
		ActionDownloadsTranscode,
	}
}

func UserFacingCapabilityActions() []ACLAction {
	return []ACLAction{
		ActionPlaybackPlay,
		ActionPlaybackTranscode,
		ActionDownloadsDirect,
		ActionDownloadsTranscode,
		ActionProfilesManage,
		ActionPersonalListsManage,
		ActionRequestsCreate,
	}
}
