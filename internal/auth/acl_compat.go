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
