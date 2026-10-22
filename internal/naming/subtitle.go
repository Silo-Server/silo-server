package naming

import (
	"path/filepath"
	"strings"

	"github.com/Silo-Server/silo-server/internal/lang"
)

// SubtitleTags is what a sidecar subtitle's file name says about the track.
type SubtitleTags struct {
	Language        string
	Forced          bool
	HearingImpaired bool
}

// SubtitleSuffix returns the part of a sidecar subtitle's name between the
// video's name and the subtitle extension, both given without extensions:
// ".en.sdh" for "Movie.en.sdh" next to "Movie". ok is false when the sidecar
// does not belong to the video; its name must equal the video's, ignoring
// case, or continue with a dot, so "MovieExtra" does not match "Movie" while
// "movie.en" does.
func SubtitleSuffix(videoBase, subtitleBase string) (suffix string, ok bool) {
	// Case folding can change a name's byte length ("ß" and "ẞ"), so compare
	// the whole name before each dot rather than cutting at len(videoBase).
	for i := 0; i <= len(subtitleBase); i++ {
		if i < len(subtitleBase) && subtitleBase[i] != '.' {
			continue
		}
		if strings.EqualFold(subtitleBase[:i], videoBase) {
			return subtitleBase[i:], true
		}
	}
	return "", false
}

// SubtitleDescriptorTags reads the name of a subtitle that is not named for a
// video, such as "English" or "2_English" in a Subs/ folder: an optional track
// number and underscore, then dot-separated words that each name a language
// or a flag ParseSubtitleSuffix knows. ok is false when any word is something
// else, so "Other Movie.en" is not a descriptor.
func SubtitleDescriptorTags(subtitleBase string) (SubtitleTags, bool) {
	words := subtitleBase
	if digits := strings.IndexFunc(words, func(r rune) bool { return r < '0' || r > '9' }); digits > 0 && words[digits] == '_' {
		words = words[digits+1:]
	}
	if words == "" {
		return SubtitleTags{}, false
	}
	for _, word := range strings.Split(words, ".") {
		switch strings.ToLower(strings.TrimSpace(word)) {
		case "forced", "sdh", "cc", "hi":
		default:
			if lang.RegisteredTag(word) == "" {
				return SubtitleTags{}, false
			}
		}
	}
	return ParseSubtitleSuffix(words), true
}

// SidecarSubtitleTags reads the tags of the sidecar at subtitlePath as a
// subtitle for the video at videoPath. ok is false when the names do not
// match, as SubtitleSuffix decides.
func SidecarSubtitleTags(videoPath, subtitlePath string) (SubtitleTags, bool) {
	videoBase := filepath.Base(videoPath)
	subtitleBase := filepath.Base(subtitlePath)
	suffix, ok := SubtitleSuffix(
		strings.TrimSuffix(videoBase, filepath.Ext(videoBase)),
		strings.TrimSuffix(subtitleBase, filepath.Ext(subtitleBase)))
	if !ok {
		return SubtitleTags{}, false
	}
	return ParseSubtitleSuffix(suffix), true
}

// ParseSubtitleSuffix reads the dot-separated words of a sidecar suffix such
// as ".en.sdh". The words can come in any order:
//
//   - "forced" marks a forced track, and "sdh" or "cc" a hearing-impaired one.
//     "sdh" is also the ISO 639-3 code for Southern Kurdish; as in Jellyfin
//     and Plex, the flag wins.
//   - The language is the first word that names a registered language, so
//     ".ai.hr" and ".hr.ai" are both Croatian. A language with a two-letter
//     ISO 639-1 code wins over one that has only a three-letter code, because
//     many short words in file names ("sub", "dub", "mkv") are also ISO 639-3
//     codes: ".sub.en" is English, while ".yue" alone is still Cantonese.
//   - "hi" is Hindi when no other word names a two-letter-code language, and
//     otherwise marks the track hearing-impaired, the rule Jellyfin uses.
func ParseSubtitleSuffix(suffix string) SubtitleTags {
	var tags SubtitleTags
	var threeLetterOnly string
	hiWords := 0
	for _, word := range strings.Split(suffix, ".") {
		switch strings.ToLower(strings.TrimSpace(word)) {
		case "":
			continue
		case "forced":
			tags.Forced = true
		case "sdh", "cc":
			tags.HearingImpaired = true
		case "hi":
			hiWords++
		default:
			tag := lang.RegisteredTag(word)
			switch {
			case tag == "":
			case hasTwoLetterCode(word, tag):
				if tags.Language == "" {
					tags.Language = tag
				}
			case threeLetterOnly == "":
				threeLetterOnly = tag
			}
		}
	}
	if tags.Language == "" && hiWords > 0 {
		tags.Language = "hi"
		hiWords--
	}
	if tags.Language == "" {
		tags.Language = threeLetterOnly
	}
	if hiWords > 0 {
		tags.HearingImpaired = true
	}
	return tags
}

// hasTwoLetterCode reports whether word, read as tag, names a language with an
// ISO 639-1 code. The word's own spelling counts too: the deprecated "tl" is a
// two-letter code even though it canonicalizes to "fil".
func hasTwoLetterCode(word, tag string) bool {
	primary, _, _ := strings.Cut(tag, "-")
	written, _, _ := strings.Cut(strings.ReplaceAll(strings.TrimSpace(word), "_", "-"), "-")
	return len(primary) == 2 || len(written) == 2
}
