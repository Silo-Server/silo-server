package ai

import "strings"

// languageNames maps common ISO 639-1 codes to English names. The model handles
// bare codes acceptably, but full names noticeably improve translation quality,
// so we resolve the common cases and fall back to the raw code otherwise.
var languageNames = map[string]string{
	"ar": "Arabic", "bg": "Bulgarian", "bn": "Bengali", "cs": "Czech",
	"da": "Danish", "de": "German", "el": "Greek", "en": "English",
	"es": "Spanish", "et": "Estonian", "fa": "Persian", "fi": "Finnish",
	"fr": "French", "he": "Hebrew", "hi": "Hindi", "hr": "Croatian",
	"hu": "Hungarian", "id": "Indonesian", "it": "Italian", "ja": "Japanese",
	"ko": "Korean", "lt": "Lithuanian", "lv": "Latvian", "ms": "Malay",
	"nl": "Dutch", "no": "Norwegian", "pl": "Polish", "pt": "Portuguese",
	"ro": "Romanian", "ru": "Russian", "sk": "Slovak", "sl": "Slovenian",
	"sr": "Serbian", "sv": "Swedish", "ta": "Tamil", "th": "Thai",
	"tr": "Turkish", "uk": "Ukrainian", "vi": "Vietnamese", "zh": "Chinese",
}

// languageDisplayName returns a human-readable language name for a code, or the
// trimmed code itself when unknown. An empty code yields an empty string.
func languageDisplayName(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	base := strings.ToLower(code)
	if i := strings.IndexAny(base, "-_"); i >= 0 {
		base = base[:i]
	}
	if name, ok := languageNames[base]; ok {
		return name
	}
	return code
}
