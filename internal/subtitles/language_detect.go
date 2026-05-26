package subtitles

import (
	"bufio"
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/abadojack/whatlanggo"
	"golang.org/x/text/language"

	"github.com/Silo-Server/silo-server/internal/lang"
)

// LanguageDetectionSource describes where an upload language was resolved from.
type LanguageDetectionSource string

const (
	LanguageSourceFilename LanguageDetectionSource = "filename"
	LanguageSourceMetadata LanguageDetectionSource = "metadata"
	LanguageSourceContent  LanguageDetectionSource = "content"
	LanguageSourceManual   LanguageDetectionSource = "manual"
)

// LanguageDetection holds a resolved subtitle language and its origin.
type LanguageDetection struct {
	Language string                  `json:"language"`
	Source   LanguageDetectionSource `json:"source"`
}

var (
	subtitleTimestampLine = regexp.MustCompile(`^\d{1,2}:\d{2}:\d{2}[,.]\d{3}\s*-->\s*\d{1,2}:\d{2}:\d{2}[,.]\d{3}`)
	assDialoguePrefix     = regexp.MustCompile(`(?i)^dialogue:\s*\d`)
	metadataLanguageLine  = regexp.MustCompile(`(?i)^(?:language|lang)\s*:\s*(.+)$`)
	vttLanguageLine       = regexp.MustCompile(`(?i)language\s*:\s*([^;]+)`)
)

var filenameLanguageSkipTokens = map[string]struct{}{
	"forced": {}, "sdh": {}, "hi": {}, "cc": {}, "sub": {}, "subs": {},
	"subtitle": {}, "subtitles": {}, "caption": {}, "captions": {},
	"the": {}, "and": {}, "for": {}, "with": {},
}

var filenameLanguageReleaseTokens = map[string]struct{}{
	"webrip": {}, "webdl": {}, "web": {}, "bluray": {}, "bdrip": {}, "dvdrip": {},
	"hdtv": {}, "hdrip": {}, "remux": {}, "proper": {}, "repack": {}, "extended": {},
	"unrated": {}, "dts": {}, "aac": {}, "ac3": {}, "eac3": {}, "truehd": {},
	"atmos": {}, "x264": {}, "x265": {}, "h264": {}, "h265": {}, "hevc": {}, "avc": {},
	"720p": {}, "1080p": {}, "2160p": {}, "4k": {}, "8k": {}, "hdr": {}, "sdr": {},
}

// filenameLanguageAliases maps common subtitle release abbreviations to ISO codes.
var filenameLanguageAliases = map[string]string{
	"chs": "zh", "cht": "zh", "chi": "zh", "zho": "zh", "cn": "zh",
	"eng": "en", "jpn": "ja", "ger": "de", "deu": "de", "fre": "fr", "fra": "fr",
	"spa": "es", "esp": "es", "ita": "it", "por": "pt", "pob": "pt", "br": "pt",
	"rus": "ru", "pol": "pl", "cze": "cs", "ces": "cs", "dan": "da", "dut": "nl",
	"nld": "nl", "swe": "sv", "nor": "no", "fin": "fi", "gre": "el", "ell": "el",
	"rum": "ro", "ron": "ro", "hrv": "hr", "srp": "sr", "bul": "bg", "ukr": "uk",
	"vie": "vi", "ind": "id", "msa": "ms", "may": "ms", "heb": "he", "hin": "hi",
	"kor": "ko", "ara": "ar", "tha": "th", "tur": "tr", "hun": "hu", "slo": "sk",
	"slk": "sk", "slv": "sl", "est": "et", "lav": "lv", "lit": "lt", "ice": "is",
	"isl": "is", "wel": "cy", "cym": "cy", "cat": "ca", "eus": "eu", "baq": "eu",
}

var metadataLanguageNames = map[string]string{
	"english": "en", "spanish": "es", "french": "fr", "german": "de", "italian": "it",
	"portuguese": "pt", "japanese": "ja", "korean": "ko", "chinese": "zh", "russian": "ru",
	"arabic": "ar", "dutch": "nl", "polish": "pl", "swedish": "sv", "norwegian": "no",
	"danish": "da", "finnish": "fi", "greek": "el", "turkish": "tr", "hungarian": "hu",
	"czech": "cs", "romanian": "ro", "hebrew": "he", "hindi": "hi", "thai": "th",
	"vietnamese": "vi", "indonesian": "id", "ukrainian": "uk",
}

// DetectSubtitleLanguage resolves a subtitle language from filename, embedded
// metadata, or dialogue text.
func DetectSubtitleLanguage(filename string, format SubtitleFormat, data []byte) LanguageDetection {
	if language, ok := languageFromFilename(filename); ok {
		return LanguageDetection{Language: language, Source: LanguageSourceFilename}
	}
	if language, ok := languageFromMetadata(format, data); ok {
		return LanguageDetection{Language: language, Source: LanguageSourceMetadata}
	}
	if language, ok := languageFromContent(data, format); ok {
		return LanguageDetection{Language: language, Source: LanguageSourceContent}
	}
	return LanguageDetection{}
}

// ResolveUploadLanguage prefers auto-detected language and falls back to the
// user-provided hint when detection fails. When preferUserLanguage is true, the
// user-provided language is used as an explicit override.
func ResolveUploadLanguage(filename string, format SubtitleFormat, data []byte, userLanguage string, preferUserLanguage bool) (LanguageDetection, error) {
	if preferUserLanguage {
		if manual := canonicalLanguageToken(userLanguage); manual != "" {
			return LanguageDetection{Language: manual, Source: LanguageSourceManual}, nil
		}
		return LanguageDetection{}, fmt.Errorf("invalid subtitle language")
	}

	if detected := DetectSubtitleLanguage(filename, format, data); detected.Language != "" {
		return detected, nil
	}
	if manual := canonicalLanguageToken(userLanguage); manual != "" {
		return LanguageDetection{Language: manual, Source: LanguageSourceManual}, nil
	}
	return LanguageDetection{}, fmt.Errorf("could not detect subtitle language")
}

func languageFromFilename(filename string) (string, bool) {
	base := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	tokens := splitFilenameTokens(base)
	for i := len(tokens) - 1; i >= 0; i-- {
		token := strings.Trim(tokens[i], "[](){}")
		if token == "" {
			continue
		}
		lower := strings.ToLower(token)
		if _, skip := filenameLanguageSkipTokens[lower]; skip {
			continue
		}
		if _, skip := filenameLanguageReleaseTokens[lower]; skip {
			continue
		}
		if containsDigit(token) {
			continue
		}
		if language, ok := filenameLanguageToken(token); ok {
			return language, true
		}
	}
	return "", false
}

func filenameLanguageToken(token string) (string, bool) {
	trimmed := strings.TrimSpace(token)
	if len(trimmed) < 2 || len(trimmed) > 3 {
		return "", false
	}
	for _, r := range trimmed {
		if r < 'A' || r > 'Z' {
			if r < 'a' || r > 'z' {
				return "", false
			}
		}
	}
	lower := strings.ToLower(trimmed)
	if mapped, ok := filenameLanguageAliases[lower]; ok {
		return mapped, true
	}
	language := canonicalLanguageToken(trimmed)
	if language == "" {
		return "", false
	}
	return language, true
}

func containsDigit(value string) bool {
	for _, r := range value {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

func languageFromMetadata(format SubtitleFormat, data []byte) (string, bool) {
	switch format {
	case FormatASS, FormatSSA:
		return languageFromASSMetadata(data)
	case FormatVTT:
		return languageFromVTTMetadata(data)
	default:
		return "", false
	}
}

func languageFromASSMetadata(data []byte) (string, bool) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "[") {
			continue
		}
		if matches := metadataLanguageLine.FindStringSubmatch(line); len(matches) == 2 {
			if language, ok := languageFromMetadataValue(matches[1]); ok {
				return language, true
			}
		}
	}
	return "", false
}

func languageFromVTTMetadata(data []byte) (string, bool) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.EqualFold(line, "WEBVTT") {
			continue
		}
		if matches := vttLanguageLine.FindStringSubmatch(line); len(matches) == 2 {
			if language, ok := languageFromMetadataValue(matches[1]); ok {
				return language, true
			}
		}
		if strings.Contains(line, "-->") {
			break
		}
	}
	return "", false
}

func languageFromContent(data []byte, format SubtitleFormat) (string, bool) {
	text := extractSubtitleDialogue(data, format)
	if len([]rune(text)) < 40 {
		return "", false
	}

	info := whatlanggo.Detect(text)
	if !info.IsReliable() {
		return "", false
	}

	code := info.Lang.Iso6391()
	if code == "" {
		code = info.Lang.Iso6393()
	}
	if language := canonicalLanguageToken(code); language != "" {
		return language, true
	}
	return "", false
}

func extractSubtitleDialogue(data []byte, format SubtitleFormat) string {
	switch format {
	case FormatASS, FormatSSA:
		return extractASSDialogue(data)
	case FormatVTT:
		return extractVTTDialogue(data)
	default:
		return extractSRTDialogue(data)
	}
}

func extractSRTDialogue(data []byte) string {
	var b strings.Builder
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || subtitleTimestampLine.MatchString(line) {
			continue
		}
		if _, err := fmt.Sscanf(line, "%d", new(int)); err == nil {
			continue
		}
		appendDialogueLine(&b, line)
	}
	return b.String()
}

func extractVTTDialogue(data []byte) string {
	var b strings.Builder
	scanner := bufio.NewScanner(bytes.NewReader(data))
	inCue := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			inCue = false
			continue
		}
		if strings.EqualFold(line, "WEBVTT") || strings.HasPrefix(strings.ToUpper(line), "NOTE") {
			continue
		}
		if strings.Contains(line, "-->") {
			inCue = true
			continue
		}
		if inCue {
			appendDialogueLine(&b, line)
		}
	}
	return b.String()
}

func extractASSDialogue(data []byte) string {
	var b strings.Builder
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !assDialoguePrefix.MatchString(line) {
			continue
		}
		parts := strings.SplitN(line, ",", 10)
		if len(parts) < 10 {
			continue
		}
		appendDialogueLine(&b, strings.TrimSpace(parts[9]))
	}
	return b.String()
}

func appendDialogueLine(b *strings.Builder, line string) {
	cleaned := strings.TrimSpace(stripASSTags(line))
	if cleaned == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteByte(' ')
	}
	b.WriteString(cleaned)
}

func stripASSTags(line string) string {
	var b strings.Builder
	b.Grow(len(line))
	inTag := false
	for _, r := range line {
		switch {
		case r == '{':
			inTag = true
		case r == '}':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func splitFilenameTokens(base string) []string {
	replaced := strings.NewReplacer("_", ".", "-", ".", " ", ".").Replace(base)
	raw := strings.Split(replaced, ".")
	tokens := make([]string, 0, len(raw))
	for _, token := range raw {
		token = strings.TrimSpace(token)
		if token != "" {
			tokens = append(tokens, token)
		}
	}
	return tokens
}

func languageFromMetadataValue(value string) (string, bool) {
	if language := canonicalLanguageToken(value); language != "" {
		return language, true
	}
	if mapped, ok := metadataLanguageNames[strings.ToLower(strings.TrimSpace(value))]; ok {
		return mapped, true
	}
	return "", false
}

// NormalizeLanguageCode canonicalizes a subtitle language code to ISO 639-1 base form.
func NormalizeLanguageCode(value string) (string, error) {
	language := canonicalLanguageToken(value)
	if language == "" {
		return "", fmt.Errorf("invalid subtitle language")
	}
	return language, nil
}

func canonicalLanguageToken(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	candidate := lang.Canonical(trimmed)
	tag, err := language.Parse(candidate)
	if err != nil {
		return ""
	}
	base, conf := tag.Base()
	if conf == language.No {
		return ""
	}
	return strings.ToLower(base.String())
}
