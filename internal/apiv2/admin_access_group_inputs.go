package apiv2

import (
	"bytes"
	"encoding/json"

	"github.com/Silo-Server/silo-server/internal/access"
)

const groupLibraryIDsField = "library_ids"

func adminGroupID(id ID) (int64, *Problem) {
	n, p := id.positive64("path.id")
	if p != nil {
		return 0, NewProblem(TypeValidationFailed, "Invalid access group ID.")
	}
	return n, nil
}
func groupInput(body AdminAccessGroupBody, raw []byte) (access.UpdateGroupInput, *Problem) {
	if p := rejectNonNullableNulls(raw, map[string]bool{groupLibraryIDsField: true, "allowed_permissions": true}); p != nil {
		return access.UpdateGroupInput{}, p
	}
	in := access.UpdateGroupInput{Name: body.Name, Description: body.Description, MaxPlaybackQuality: body.MaxPlaybackQuality, DownloadAllowed: body.DownloadAllowed, DownloadTranscodeAllowed: body.DownloadTranscodeAllowed, TranscodeAllowed: body.TranscodeAllowed, AudioTranscodeAllowed: body.AudioTranscodeAllowed, MaxStreams: body.MaxStreams, MaxTranscodes: body.MaxTranscodes, MaxRemoteStreamBitrateKbps: body.MaxRemoteStreamBitrateKbps, MaxLocalStreamBitrateKbps: body.MaxLocalStreamBitrateKbps, AllowedPermissions: body.AllowedPermissions, RequestsAllowed: body.RequestsAllowed, IsDefault: body.IsDefault}
	if body.LibraryIDs != nil {
		ids := make([]int, 0, len(*body.LibraryIDs))
		for _, id := range *body.LibraryIDs {
			n, p := id.positive("body.library_ids")
			if p != nil {
				return in, NewProblem(TypeValidationFailed, "Invalid library ID.")
			}
			ids = append(ids, n)
		}
		in.LibraryIDs = &ids
	}
	// Pointer fields distinguish [] from omission, while raw presence preserves
	// the legacy explicit-null policy reset rather than dropping it as absent.
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return in, NewProblem(TypeMalformedRequest, "Invalid JSON body.")
	}
	if bytes.Equal(bytes.TrimSpace(members[groupLibraryIDsField]), []byte("null")) {
		in.LibraryIDs = new([]int(nil))
	}
	if bytes.Equal(bytes.TrimSpace(members["allowed_permissions"]), []byte("null")) {
		in.AllowedPermissions = new([]string(nil))
	}
	return in, nil
}
func applyGroupCreate(out *access.CreateGroupInput, in access.UpdateGroupInput) {
	if in.Description != nil {
		out.Description = *in.Description
	}
	if in.LibraryIDs != nil {
		out.LibraryIDs = *in.LibraryIDs
	}
	if in.MaxPlaybackQuality != nil {
		out.MaxPlaybackQuality = *in.MaxPlaybackQuality
	}
	if in.DownloadAllowed != nil {
		out.DownloadAllowed = *in.DownloadAllowed
	}
	if in.DownloadTranscodeAllowed != nil {
		out.DownloadTranscodeAllowed = *in.DownloadTranscodeAllowed
	}
	if in.TranscodeAllowed != nil {
		out.TranscodeAllowed = *in.TranscodeAllowed
	}
	if in.AudioTranscodeAllowed != nil {
		out.AudioTranscodeAllowed = *in.AudioTranscodeAllowed
	}
	if in.MaxStreams != nil {
		out.MaxStreams = *in.MaxStreams
	}
	if in.MaxTranscodes != nil {
		out.MaxTranscodes = *in.MaxTranscodes
	}
	if in.MaxRemoteStreamBitrateKbps != nil {
		out.MaxRemoteStreamBitrateKbps = *in.MaxRemoteStreamBitrateKbps
	}
	if in.MaxLocalStreamBitrateKbps != nil {
		out.MaxLocalStreamBitrateKbps = *in.MaxLocalStreamBitrateKbps
	}
	if in.AllowedPermissions != nil {
		out.AllowedPermissions = *in.AllowedPermissions
	}
	if in.RequestsAllowed != nil {
		out.RequestsAllowed = *in.RequestsAllowed
	}
	if in.IsDefault != nil {
		out.IsDefault = *in.IsDefault
	}
}
