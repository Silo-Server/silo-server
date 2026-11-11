package catalog

import (
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// A file's identity is where it lives on the server and what it is called:
// its path, its file name, an external subtitle's file name, and a subtitle
// title that is only that name. Only a viewer who may curate metadata
// sees it. Everyone else still gets every technical detail of a version.
//
// Audiobooks and ebooks keep their file names: those beta features name,
// order and open files by them.

// HideFileIdentity strips file identity from versions sent to a viewer who
// may not see it. itemType is the type of the item that owns them.
func HideFileIdentity(versions []FileVersion, itemType string) {
	keepNames := itemType == detailTypeAudiobook || itemType == detailTypeEbook
	for i := range versions {
		versions[i].hideFileIdentity(keepNames)
	}
}

// HideFileIdentity strips file identity from the detail's versions, playback
// variant parts included, and drops its folder paths.
func (d *ItemDetail) HideFileIdentity() {
	if d == nil {
		return
	}
	sidecars := sidecarPositions(d.Versions)
	HideFileIdentity(d.Versions, d.Type)
	hideVariantFileIdentity(d.PlaybackVariants, d.Type)
	d.FolderPaths = nil
	d.EffectiveSubtitleTrackSignature = relabelSavedSidecarChoice(d.EffectiveSubtitleTrackSignature, d.Versions, sidecars)
}

// HideFileIdentity strips file identity from the watch detail's versions,
// playback variant parts included.
func (d *WatchDetail) HideFileIdentity() {
	if d == nil {
		return
	}
	sidecars := sidecarPositions(d.Versions)
	HideFileIdentity(d.Versions, d.Type)
	hideVariantFileIdentity(d.PlaybackVariants, d.Type)
	d.EffectiveSubtitleTrackSignature = relabelSavedSidecarChoice(d.EffectiveSubtitleTrackSignature, d.Versions, sidecars)
}

// subtitleSourceExternal is the source of a subtitle file next to the media.
const subtitleSourceExternal = "external"

// sidecarPosition is where an external subtitle track sits in a version list.
type sidecarPosition struct{ version, track int }

// sidecarPositions maps each external track's file name to its position.
func sidecarPositions(versions []FileVersion) map[string]sidecarPosition {
	positions := map[string]sidecarPosition{}
	for v := range versions {
		for t, track := range versions[v].SubtitleTracks {
			name := strings.TrimSpace(track.FileName)
			if !track.External || name == "" {
				continue
			}
			if _, seen := positions[name]; !seen {
				positions[name] = sidecarPosition{version: v, track: t}
			}
		}
	}
	return positions
}

// relabelSavedSidecarChoice translates a saved choice of an external subtitle
// whose label is the sidecar's file name, as such choices were saved before
// this viewer stopped seeing file names, to the label the viewer now gets for
// that sidecar, so the choice still selects it. A label the viewer cannot see
// on any current sidecar, such as the file name of one since removed, is
// dropped; the choice then matches by language, format and flags. versions
// are already hidden; positions were taken before.
func relabelSavedSidecarChoice(sig *userstore.SubtitleTrackSignature, versions []FileVersion, positions map[string]sidecarPosition) *userstore.SubtitleTrackSignature {
	if sig == nil || sig.Source != subtitleSourceExternal || strings.TrimSpace(sig.Label) == "" {
		return sig
	}
	relabeled := *sig
	if pos, ok := positions[strings.TrimSpace(sig.Label)]; ok {
		relabeled.Label = sidecarTrackLabel(versions[pos.version].SubtitleTracks[pos.track])
		return &relabeled
	}
	for _, version := range versions {
		for _, track := range version.SubtitleTracks {
			if track.External && sidecarTrackLabel(track) == sig.Label {
				return sig
			}
		}
	}
	relabeled.Label = ""
	return &relabeled
}

// sidecarTrackLabel is the label a client shows for an external track once its
// file name is hidden.
func sidecarTrackLabel(track VersionSubtitleTrack) string {
	return firstNonEmpty(track.Title, track.EmbeddedTitle, track.Language)
}

func hideVariantFileIdentity(variants []PlaybackVariant, itemType string) {
	for i := range variants {
		for j := range variants[i].Parts {
			HideFileIdentity(variants[i].Parts[j].Versions, itemType)
		}
	}
}

func (v *FileVersion) hideFileIdentity(keepNames bool) {
	v.FilePath = ""
	if keepNames {
		return
	}
	v.FileName = ""
	// A playback variant part holds a copy of the version that shares this
	// slice, so edit a copy of it.
	tracks := slices.Clone(v.SubtitleTracks)
	labels := models.SidecarLabels{}
	for i := range tracks {
		t := &tracks[i]
		// The catalog fills an untitled sidecar's title from its file name,
		// and the scanner stores the file name as its title. Without it the
		// track is known by its embedded title or, numbered as the playback
		// inventory numbers it, its language.
		if t.External && (t.TitleIsFallback || models.IsFileNameTitle(t.Title, t.FileName)) {
			t.Title, t.TitleIsFallback = "", false
			if strings.TrimSpace(t.EmbeddedTitle) == "" {
				if label := labels.Next(t.Language, t.Codec, t.Forced, t.HearingImpaired); label != t.Language {
					t.Title = label
				}
			}
		}
		t.FileName = ""
	}
	v.SubtitleTracks = tracks
}
