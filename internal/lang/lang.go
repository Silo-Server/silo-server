// Package lang canonicalizes ISO 639 language codes and ISO 3166-1 country
// codes at ingest write sites so equivalent values ("en"/"eng"/"ENG")
// collapse to a single stored form.
package lang

import (
	"strings"

	"golang.org/x/text/language"
)

const (
	legacyEnglishName           = "english"
	canonicalPortugueseBrazil   = "pt-BR"
	canonicalChineseTraditional = "zh-Hant"
)

// CanonicalTag validates a BCP 47 tag, canonicalizing ISO aliases and casing.
// Explicit scripts, regions, variants and extensions are preserved. It accepts
// underscores but no display names. Empty and malformed values return "".
func CanonicalTag(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "_", "-")
	if !languageTagPattern.MatchString(value) {
		return grandfatheredTag(value)
	}
	if hasDuplicateSubtags(value) {
		return ""
	}
	parts := strings.Split(value, "-")
	if len(parts) > 1 && len(parts[1]) == 3 && isAlpha(parts[1]) {
		return canonicalTagCase(value)
	}
	if tag, err := language.Parse(value); err == nil {
		return tag.String()
	}
	// The settings contract is open to well-formed, unregistered subtags.
	return canonicalTagCase(value)
}

// grandfatheredTag resolves the irregular BCP 47 forms the grammar prefilter
// cannot express ("i-klingon", "en-GB-oed", "sgn-BE-FR") to their registered
// replacements. The parser rejects everything else the prefilter rejects, so
// display names and free text still return "".
func grandfatheredTag(value string) string {
	if !strings.Contains(value, "-") {
		return ""
	}
	tag, err := language.Parse(value)
	if err != nil || tag == language.Und {
		return ""
	}
	return tag.String()
}

// hasDuplicateSubtags rejects structurally invalid repeated variants and
// extension singletons while retaining unregistered but well-formed subtags.
func hasDuplicateSubtags(value string) bool {
	parts := strings.Split(strings.ToLower(value), "-")
	variants := make(map[string]struct{})
	singletons := make(map[string]struct{})
	privateUse := parts[0] == "x"
	extensionStarted := false
	for i := 1; i < len(parts); i++ {
		part := parts[i]
		if privateUse {
			continue
		}
		if len(part) == 1 {
			if _, exists := singletons[part]; exists {
				return true
			}
			singletons[part] = struct{}{}
			extensionStarted = true
			if part == "x" {
				privateUse = true
			}
			continue
		}
		if !extensionStarted && (len(part) >= 5 || (len(part) == 4 && part[0] >= '0' && part[0] <= '9')) {
			if _, exists := variants[part]; exists {
				return true
			}
			variants[part] = struct{}{}
		}
	}
	return false
}

// CompatibleTag also recognizes known English display names from legacy
// preferences, subtitle providers and transcription services.
func CompatibleTag(value string) string {
	value = strings.TrimSpace(value)
	if mapped, ok := languageNames[strings.ToLower(value)]; ok {
		value = mapped
	}
	return CanonicalTag(value)
}

// PrimaryLanguage intentionally drops script and region for language matching.
// It never infers a language from an undefined or private-use tag.
func PrimaryLanguage(value string) string {
	canonical := CompatibleTag(value)
	base, _, _ := strings.Cut(canonical, "-")
	if base == "und" || base == "x" {
		return ""
	}
	return base
}

var languageNames = map[string]string{
	legacyEnglishName: "en", "spanish": "es", "french": "fr", "german": "de",
	"italian": "it", "portuguese": "pt", "japanese": "ja", "korean": "ko",
	"chinese": "zh", "russian": "ru", "arabic": "ar", "dutch": "nl",
	"polish": "pl", "swedish": "sv", "norwegian": "no", "danish": "da",
	"finnish": "fi", "greek": "el", "turkish": "tr", "hungarian": "hu",
	"czech": "cs", "romanian": "ro", "hebrew": "he", "hindi": "hi",
	"thai": "th", "vietnamese": "vi", "indonesian": "id", "ukrainian": "uk",
	"bengali": "bn", "bangla": "bn", "bulgarian": "bg", "croatian": "hr",
	"persian": "fa", "farsi": "fa", "malay": "ms", "serbian": "sr",
	"slovak": "sk", "slovenian": "sl", "tamil": "ta", "telugu": "te",
	"estonian": "et", "latvian": "lv", "lithuanian": "lt", "icelandic": "is",
	"brazilian portuguese": canonicalPortugueseBrazil, "brazillian portuguese": canonicalPortugueseBrazil,
	"portuguese (brazil)": canonicalPortugueseBrazil, "portuguese (portugal)": "pt-PT",
	"european portuguese": "pt-PT", "chinese (traditional)": canonicalChineseTraditional,
	"traditional chinese": canonicalChineseTraditional, "chinese (simplified)": "zh-Hans",
	"simplified chinese": "zh-Hans",
}

// Canonical returns the ISO 639-1 lowercase 2-letter form of value, or the
// 3-letter form for languages without a 2-letter equivalent (e.g. "fil").
// Unparseable inputs are returned trimmed and lowercased verbatim so we
// never silently drop data.
func Canonical(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if trimmed == "" {
		return ""
	}
	tag, err := language.Parse(trimmed)
	if err != nil {
		return trimmed
	}
	base, conf := tag.Base()
	if conf == language.No || tag == language.Und {
		return trimmed
	}
	return strings.ToLower(base.String())
}

// CanonicalCountry returns the ISO 3166-1 alpha-2 uppercase form. Unparseable
// inputs are returned trimmed and uppercased verbatim.
func CanonicalCountry(value string) string {
	trimmed := strings.ToUpper(strings.TrimSpace(value))
	if trimmed == "" {
		return ""
	}
	region, err := language.ParseRegion(trimmed)
	if err != nil {
		return trimmed
	}
	return region.String()
}

// CanonicalCountries returns a copy of values with each entry canonicalized
// and empties dropped. Preserves nil so callers can keep the SQL NULL
// distinction from an empty array.
func CanonicalCountries(values []string) []string {
	if values == nil {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		c := CanonicalCountry(v)
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}
