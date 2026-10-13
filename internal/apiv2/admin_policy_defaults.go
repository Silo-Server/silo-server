package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/access"
)

// PolicyDefaults is the policy an ungrouped account resolves a field to when
// it has no override of its own.
type PolicyDefaults struct {
	LibraryIDs                 []ID   `json:"library_ids" nullable:"true" doc:"Libraries the account may see; null means every library, empty means none" example:"[\"1\",\"2\"]"`
	MaxPlaybackQuality         string `json:"max_playback_quality" doc:"Playback ceiling; empty means none" example:""`
	MaxStreams                 int    `json:"max_streams" doc:"Concurrent stream limit; 0 means unlimited" example:"0"`
	MaxTranscodes              int    `json:"max_transcodes" doc:"Concurrent transcode limit; 0 means unlimited" example:"0"`
	MaxRemoteStreamBitrateKbps int    `json:"max_remote_stream_bitrate_kbps" minimum:"0" doc:"Remote per-stream bitrate limit in kbps; 0 means unlimited" example:"0"`
	MaxLocalStreamBitrateKbps  int    `json:"max_local_stream_bitrate_kbps" minimum:"0" doc:"Local per-stream bitrate limit in kbps; 0 means unlimited" example:"0"`
	TranscodeAllowed           bool   `json:"transcode_allowed" example:"true"`
	AudioTranscodeAllowed      bool   `json:"audio_transcode_allowed" example:"true"`
	DownloadAllowed            bool   `json:"download_allowed" example:"true"`
	DownloadTranscodeAllowed   bool   `json:"download_transcode_allowed" example:"true"`
	RequestsAllowed            bool   `json:"requests_allowed" example:"true"`
}

type AdminUserPolicyDefaultsOutput struct {
	Body AdminUserPolicyDefaults
}

type AdminUserPolicyDefaults struct {
	Admin     PolicyDefaults `json:"admin" doc:"What an admin account's unset policy fields resolve to: full access. Admin accounts never belong to an access group"`
	Ungrouped PolicyDefaults `json:"ungrouped" doc:"What a regular account with no access group resolves its unset policy fields to"`
}

func policyDefaultsOf(p access.GroupPolicy) PolicyDefaults {
	return PolicyDefaults{
		LibraryIDs:                 idsOfInts(p.LibraryIDs),
		MaxPlaybackQuality:         p.MaxPlaybackQuality,
		MaxStreams:                 p.MaxStreams,
		MaxTranscodes:              p.MaxTranscodes,
		MaxRemoteStreamBitrateKbps: p.MaxRemoteStreamBitrateKbps,
		MaxLocalStreamBitrateKbps:  p.MaxLocalStreamBitrateKbps,
		TranscodeAllowed:           p.TranscodeAllowed,
		AudioTranscodeAllowed:      p.AudioTranscodeAllowed,
		DownloadAllowed:            p.DownloadAllowed,
		DownloadTranscodeAllowed:   p.DownloadTranscodeAllowed,
		RequestsAllowed:            p.RequestsAllowed,
	}
}

// registerAdminPolicyDefaults serves the server's built-in policy layers so
// clients show them instead of keeping their own copy. They come from the
// build, not from a service.
func registerAdminPolicyDefaults(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodGet, Prefix+"/admin/users/policy-defaults", "getAdminUserPolicyDefaults", "admin-users", "The policy values an admin account, or a regular account without an access group, uses for fields it does not override."), Class: ClassActingAdmin}
	Register(reg, op, func(context.Context, *struct{}) (*AdminUserPolicyDefaultsOutput, error) {
		return &AdminUserPolicyDefaultsOutput{Body: AdminUserPolicyDefaults{
			Admin:     policyDefaultsOf(access.AdminPolicy()),
			Ungrouped: policyDefaultsOf(access.NoGroupPolicy()),
		}}, nil
	})
}
