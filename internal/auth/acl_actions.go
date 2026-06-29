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
