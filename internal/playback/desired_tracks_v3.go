package playback

import "github.com/Silo-Server/silo-server/internal/models"

// DesiredTracksV3 is the audio and subtitle the viewer asked for during one
// playback attempt. It survives a version change that had to fall back: a
// 1080p version without the chosen subtitle plays without it, and returning to
// the 2160p version brings it back. A plan's SelectedTracks only says what
// that plan could deliver.
//
// Each track is a snapshot of the chosen track's traits, not an ordinal: an
// ordinal means nothing on another file, and a file's own combined subtitle
// ordinals shift when its sidecar or downloaded inventory changes.
type DesiredTracksV3 struct {
	// PlanID is the plan this intent was last reconciled with. A replan
	// committed by a server that predates this field advances the attempt's
	// current plan without touching it, so a mismatch marks the intent stale.
	PlanID string `json:"plan_id"`
	// Audio is nil when the file the attempt started on had no audio.
	Audio *DesiredAudioTrackV3 `json:"audio,omitempty"`
	// Subtitle is nil when the viewer wants no subtitle.
	Subtitle *DesiredSubtitleTrackV3 `json:"subtitle,omitempty"`
}

// DesiredAudioTrackV3 is a chosen audio track and the file it was chosen on.
type DesiredAudioTrackV3 struct {
	FileID int               `json:"file_id"`
	Index  int               `json:"index"`
	Track  models.AudioTrack `json:"track"`
}

// DesiredSubtitleTrackV3 is a chosen subtitle: its source identity on the file
// it was chosen on, and the traits that find its equivalent elsewhere.
type DesiredSubtitleTrackV3 struct {
	FileID int `json:"file_id"`
	// Source is one of the SubtitleSource* values; it says which identity
	// field below names the track.
	Source               string `json:"source"`
	ExternalPath         string `json:"external_path,omitempty"`
	EmbeddedStreamIndex  int    `json:"embedded_stream_index,omitempty"`
	DownloadedSubtitleID int    `json:"downloaded_subtitle_id,omitempty"`
	Language             string `json:"language,omitempty"`
	// Title is the title the inventory shows the viewer.
	Title           string `json:"title,omitempty"`
	Format          string `json:"format,omitempty"`
	ReleaseName     string `json:"release_name,omitempty"`
	Forced          bool   `json:"forced,omitempty"`
	HearingImpaired bool   `json:"hearing_impaired,omitempty"`
}

// DesiredAudioFromFile snapshots file's audio track at index, or nil when
// there is none.
func DesiredAudioFromFile(file *models.MediaFile, index int) *DesiredAudioTrackV3 {
	if file == nil || index < 0 || index >= len(file.AudioTracks) {
		return nil
	}
	return &DesiredAudioTrackV3{FileID: file.ID, Index: index, Track: file.AudioTracks[index]}
}

// ResolveOn returns the index of the desired audio on file: the same track
// when file is the one it was chosen on and still holds it there, else the
// closest equivalent (MatchAudioTrack).
func (d DesiredAudioTrackV3) ResolveOn(file *models.MediaFile) int {
	if file == nil || len(file.AudioTracks) == 0 {
		return 0
	}
	if file.ID == d.FileID && d.Index >= 0 && d.Index < len(file.AudioTracks) &&
		file.AudioTracks[d.Index] == d.Track {
		return d.Index
	}
	return MatchAudioTrack(file.AudioTracks, d.Track, d.Index)
}
