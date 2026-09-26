package jellycompat

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/lang"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Jellyfin SubtitlePlaybackMode values.
const (
	compatSubtitleDefault    = "Default"
	compatSubtitleSmart      = "Smart"
	compatSubtitleAlways     = "Always"
	compatSubtitleOnlyForced = "OnlyForced"
	compatSubtitleNone       = "None"
)

// Canonical playback.subtitle_mode values.
const (
	profileSubtitleAuto   = "auto"
	profileSubtitleAlways = "always"
	profileSubtitleOff    = "off"
)

// compatNativeSubtitleSettings maps a Jellyfin SubtitlePlaybackMode onto
// Silo's canonical playback.subtitle_mode and playback.show_forced_subtitles.
// Silo's "auto" is Jellyfin's Smart, and "off" is OnlyForced or None depending
// on whether forced subtitles show; Default keeps "auto" and is recovered from
// the saved Jellyfin configuration. ok is false for an unknown mode.
func compatNativeSubtitleSettings(mode string) (nativeMode string, showForced bool, ok bool) {
	switch mode {
	case compatSubtitleDefault, compatSubtitleSmart:
		return profileSubtitleAuto, true, true
	case compatSubtitleAlways:
		return profileSubtitleAlways, true, true
	case compatSubtitleOnlyForced:
		return profileSubtitleOff, true, true
	case compatSubtitleNone:
		return profileSubtitleOff, false, true
	default:
		return "", false, false
	}
}

// compatJellyfinSubtitleMode is the inverse of compatNativeSubtitleSettings.
// modeSet is false when no scope stores a subtitle mode; such a profile gets
// Jellyfin's default user mode. savedMode is the mode the Jellyfin client last
// saved, which distinguishes Default from Smart for "auto".
func compatJellyfinSubtitleMode(nativeMode string, modeSet, showForced bool, savedMode string) string {
	if !modeSet {
		return compatSubtitleDefault
	}
	switch nativeMode {
	case profileSubtitleAlways:
		return compatSubtitleAlways
	case profileSubtitleOff:
		if showForced {
			return compatSubtitleOnlyForced
		}
		return compatSubtitleNone
	default:
		if savedMode == compatSubtitleDefault {
			return compatSubtitleDefault
		}
		return compatSubtitleSmart
	}
}

// compatSubtitleCandidate is one subtitle stream offered for default
// selection.
type compatSubtitleCandidate struct {
	Index    int
	Language string
	External bool
	Default  bool
	Forced   bool
}

// compatSubtitleCandidates lists a version's embedded and external subtitle
// tracks, then its downloaded subtitles (served as external streams), with
// the stream indexes PlaybackInfo advertises.
func compatSubtitleCandidates(version catalog.FileVersion, downloaded []subtitles.DownloadedSubtitle) []compatSubtitleCandidate {
	candidates := make([]compatSubtitleCandidate, 0, len(version.SubtitleTracks)+len(downloaded))
	for index, track := range version.SubtitleTracks {
		candidates = append(candidates, compatSubtitleCandidate{
			Index:    subtitleTrackIndex(version, track, index),
			Language: track.Language,
			External: track.External,
			Default:  track.Default,
			Forced:   track.Forced,
		})
	}
	base := nextDownloadedSubtitleIndex(version)
	for index, dl := range downloaded {
		candidates = append(candidates, compatSubtitleCandidate{Index: base + index, Language: dl.Language, External: true})
	}
	return candidates
}

// compatWithoutForcedSubtitles drops forced tracks for a viewer whose native
// settings hide them. Jellyfin has no such toggle; without this, its Smart and
// Default modes would start a forced track the viewer turned off.
func compatWithoutForcedSubtitles(candidates []compatSubtitleCandidate) []compatSubtitleCandidate {
	return slices.DeleteFunc(slices.Clone(candidates), func(c compatSubtitleCandidate) bool { return c.Forced })
}

// compatDefaultSubtitleStreamIndex ports Jellyfin 12.1's
// MediaStreamSelector.GetDefaultSubtitleStreamIndex. preferred is the user's
// subtitle language preference (empty matches any language, as upstream);
// audioLanguage is the language of the audio the client starts with.
func compatDefaultSubtitleStreamIndex(candidates []compatSubtitleCandidate, preferred []string, mode, audioLanguage string) *int {
	if mode == compatSubtitleNone {
		return nil
	}
	matches := func(language string) bool { return compatMatchesPreferredLanguage(language, preferred) }
	// Sort: external > default > preferred full > preferred forced >
	// undefined forced > forced. The sort is stable, as LINQ's OrderBy is.
	sorted := slices.Clone(candidates)
	slices.SortStableFunc(sorted, func(a, b compatSubtitleCandidate) int {
		for _, key := range []func(compatSubtitleCandidate) bool{
			func(c compatSubtitleCandidate) bool { return c.External },
			func(c compatSubtitleCandidate) bool { return c.Default },
			func(c compatSubtitleCandidate) bool { return !c.Forced && matches(c.Language) },
			func(c compatSubtitleCandidate) bool { return c.Forced && matches(c.Language) },
			func(c compatSubtitleCandidate) bool { return c.Forced && compatLanguageUndefined(c.Language) },
			func(c compatSubtitleCandidate) bool { return c.Forced },
		} {
			if ka, kb := key(a), key(b); ka != kb {
				if ka {
					return -1
				}
				return 1
			}
		}
		return 0
	})
	first := func(keep func(compatSubtitleCandidate) bool) *int {
		for _, candidate := range sorted {
			if keep(candidate) {
				return intPtr(candidate.Index)
			}
		}
		return nil
	}
	onlyForced := func() *int {
		forced := make([]compatSubtitleCandidate, 0, len(sorted))
		for _, candidate := range sorted {
			if candidate.Forced && (matches(candidate.Language) || compatLanguageUndefined(candidate.Language)) {
				forced = append(forced, candidate)
			}
		}
		slices.SortStableFunc(forced, func(a, b compatSubtitleCandidate) int {
			if ma, mb := matches(a.Language), matches(b.Language); ma != mb {
				if ma {
					return -1
				}
				return 1
			}
			if ua, ub := compatLanguageUndefined(a.Language), compatLanguageUndefined(b.Language); ua != ub {
				if ua {
					return -1
				}
				return 1
			}
			return 0
		})
		if len(forced) == 0 {
			return nil
		}
		return intPtr(forced[0].Index)
	}

	switch mode {
	case compatSubtitleDefault:
		return first(func(c compatSubtitleCandidate) bool { return c.External || c.Default || c.Forced })
	case compatSubtitleSmart:
		// Subtitles only when the audio is not in a preferred subtitle
		// language; otherwise behave like OnlyForced.
		if !compatContainsLanguage(preferred, audioLanguage) {
			return first(func(c compatSubtitleCandidate) bool { return matches(c.Language) })
		}
		return onlyForced()
	case compatSubtitleAlways:
		if index := first(func(c compatSubtitleCandidate) bool { return !c.Forced && matches(c.Language) }); index != nil {
			return index
		}
		return onlyForced()
	case compatSubtitleOnlyForced:
		return onlyForced()
	default:
		return nil
	}
}

// compatMatchesPreferredLanguage mirrors Jellyfin's MatchesPreferredLanguage:
// an empty preference matches any language.
func compatMatchesPreferredLanguage(language string, preferred []string) bool {
	return len(preferred) == 0 || compatContainsLanguage(preferred, language)
}

// compatContainsLanguage reports whether language is one of preferred.
// Jellyfin expands a bare language to every ISO 639 code for it and keeps a
// regional preference exact; Silo compares primary languages for a bare
// preference and full tags for a regional one.
func compatContainsLanguage(preferred []string, language string) bool {
	candidate := lang.CompatibleTag(language)
	if candidate == "" {
		return false
	}
	for _, want := range preferred {
		wantTag := lang.CompatibleTag(want)
		if wantTag == "" {
			continue
		}
		if strings.Contains(wantTag, "-") {
			if strings.EqualFold(wantTag, candidate) {
				return true
			}
			continue
		}
		if primary := lang.PrimaryLanguage(candidate); primary != "" && primary == lang.PrimaryLanguage(wantTag) {
			return true
		}
	}
	return false
}

// compatLanguageUndefined mirrors Jellyfin's IsLanguageUndefined.
func compatLanguageUndefined(language string) bool {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "", "und", "unknown", "undetermined", "mul", "zxx": //nolint:goconst // Jellyfin's undefined-language codes, listed verbatim
		return true
	default:
		return false
	}
}

// compatDetailSubtitleStreamIndex applies the same viewer preferences before
// playback and after the playback audio choice has been negotiated.
func compatDetailSubtitleStreamIndex(detail *upstreamItemDetail, version catalog.FileVersion, downloaded []subtitles.DownloadedSubtitle, savedMode string, audioIndex *int) *int {
	mode := compatJellyfinSubtitleMode(detail.SubtitleMode, detail.SubtitleModeSet, detail.ShowForcedSubtitles, savedMode)
	var preferred []string
	if language := strings.TrimSpace(detail.SubtitleLanguage); language != "" {
		preferred = []string{language}
	}
	candidates := compatSubtitleCandidates(version, downloaded)
	if !detail.ShowForcedSubtitles {
		candidates = compatWithoutForcedSubtitles(candidates)
	}
	return compatDefaultSubtitleStreamIndex(candidates, preferred, mode, compatAudioTrack(version, audioIndex).Language)
}

// savedCompatSubtitleMode returns the SubtitleMode the viewer's Jellyfin
// client last saved, or "" when none is stored or it cannot be read. It only
// tells Jellyfin's Default apart from Smart, which share Silo's "auto".
func savedCompatSubtitleMode(ctx context.Context, provider userstore.UserStoreProvider, session *Session) string {
	if provider == nil || session == nil || session.ProfileID == "" {
		return ""
	}
	store, err := provider.ForUser(ctx, session.StreamAppUserID)
	if err != nil || store == nil {
		return ""
	}
	raw, err := store.GetSetting(ctx, configurationKey(session.ProfileID))
	if err != nil || raw == "" {
		return ""
	}
	var saved struct{ SubtitleMode string }
	if json.Unmarshal([]byte(raw), &saved) != nil {
		return ""
	}
	return saved.SubtitleMode
}
