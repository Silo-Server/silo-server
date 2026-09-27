package naming

import (
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	plexEditionTagRe      = regexp.MustCompile(`(?i)\{edition-([^}]+)\}`)
	presentationPartRe    = regexp.MustCompile(`(?i)(?:^|[.\-_\s])(cd|disc|part|pt)(?:\s*|[._-]?)(\d{1,2})(?:$|[.\-_\s])`)
	variantReleaseGroupRe = regexp.MustCompile(`(?i)(?:^|[.\s_-])(?:remux|web[ ._-]?dl|webrip|bluray|bdrip|brrip|hdr|dv|2160p|1080p|720p|x264|x265|h\.?264|h\.?265|hevc|av1|aac|ac3|eac3|dts|truehd|atmos).*-([a-z0-9][a-z0-9-]{1,31})$`)
	variantCleanupSepRe   = regexp.MustCompile(`[.\-_]+`)
	variantWhitespaceRe   = regexp.MustCompile(`\s+`)
	variantQualityTokenRe = regexp.MustCompile(`(?i)\b(?:hdr|dv|sdr|3d|remux|web[ ._-]?dl|webrip|bluray|bdrip|brrip|2160p|1080p|720p|480p|x264|x265|h\.?264|h\.?265|hevc|av1|aac|ac3|eac3|dts|truehd|atmos|proper|repack|multi|dual|vff|german|internal|criterion|custom|hybrid)\b`)
)

type editionRule struct {
	key                  string
	display              string
	re                   *regexp.Regexp
	tokens               []string
	comparisonSafeSuffix bool
}

var editionRules = []editionRule{
	{key: "extended_director_cut_fan_edit", display: "Extended Directors Cut Fan Edit", re: regexp.MustCompile(`(?i)\bextended\s+directors?\s+cut\s+fan\s+edit\b`), tokens: []string{"extended", "director_cut", "fan_edit"}},
	{key: "extended_director_cut", display: "Extended Directors Cut", re: regexp.MustCompile(`(?i)\bextended\s+directors?\s+cut\b`), tokens: []string{"extended", "director_cut"}},
	{key: "director_cut", display: "Director's Cut", re: regexp.MustCompile(`(?i)\bdirector'?s?\s+cut\b`), tokens: []string{"director_cut"}},
	{key: "editor_cut", display: "Editor's Cut", re: regexp.MustCompile(`(?i)\beditor'?s?\s+cut\b`), tokens: []string{"editor_cut"}},
	{key: "final_cut", display: "Final Cut", re: regexp.MustCompile(`(?i)\bfinal\s+cut\b`), tokens: []string{"final_cut"}},
	{key: "assembly_cut", display: "Assembly Cut", re: regexp.MustCompile(`(?i)\bassembly\s+cut\b`), tokens: []string{"assembly_cut"}},
	{key: "extended", display: "Extended", re: regexp.MustCompile(`(?i)\bextended(?:\s+(?:edition|cut))?\b`), tokens: []string{"extended"}},
	{key: "theatrical", display: "Theatrical", re: regexp.MustCompile(`(?i)\btheatrical(?:\s+release)?\b`), tokens: []string{"theatrical"}},
	{key: "unrated", display: "Unrated", re: regexp.MustCompile(`(?i)\bunrated\b`), tokens: []string{"unrated"}},
	{key: "uncut", display: "Uncut", re: regexp.MustCompile(`(?i)\buncut\b`), tokens: []string{"uncut"}},
	{key: "uncensored", display: "Uncensored", re: regexp.MustCompile(`(?i)\buncensored\b`), tokens: []string{"uncensored"}},
	{key: "imax_enhanced", display: "IMAX Enhanced", re: regexp.MustCompile(`(?i)\bimax\s+enhanced\b`), tokens: []string{"imax_enhanced"}},
	{key: "imax", display: "IMAX", re: regexp.MustCompile(`(?i)\bimax\b`), tokens: []string{"imax"}},
	{key: "open_matte", display: "Open Matte", re: regexp.MustCompile(`(?i)\bopen\s+matte\b`), tokens: []string{"open_matte"}},
	{key: "despecialized", display: "Despecialized", re: regexp.MustCompile(`(?i)\bdespeciali[sz]ed\b`), tokens: []string{"despecialized"}},
	{key: "fan_edit", display: "Fan Edit", re: regexp.MustCompile(`(?i)\bfan\s+edit\b`), tokens: []string{"fan_edit"}},
	{key: "special_edition", display: "Special Edition", re: regexp.MustCompile(`(?i)\bspecial\s+edition\b`), tokens: []string{"special_edition"}},
	{key: "collectors_edition", display: "Collector's Edition", re: regexp.MustCompile(`(?i)\bcollector'?s\s+edition\b`), tokens: []string{"collectors_edition"}},
	{key: "ultimate_edition", display: "Ultimate Edition", re: regexp.MustCompile(`(?i)\bultimate\s+edition\b`), tokens: []string{"ultimate_edition"}},
	{key: "remastered", display: "Remastered", re: regexp.MustCompile(`(?i)\bremastered\b`), tokens: []string{"remastered"}},
	{key: "restored", display: "Restored", re: regexp.MustCompile(`(?i)\brestored\b`), tokens: []string{"restored"}},
	{key: "workprint", display: "Workprint", re: regexp.MustCompile(`(?i)\bworkprint\b`), tokens: []string{"workprint"}},
	{key: "hybrid", display: "Hybrid", re: regexp.MustCompile(`(?i)\bhybrid\b`), tokens: []string{"hybrid"}},
	{key: "black_and_white", display: "Black and White", re: regexp.MustCompile(`(?i)\bblack\s+(?:and\s+)?white\b`), tokens: []string{"black_and_white"}},
	{key: "black_chrome", display: "Black Chrome", re: regexp.MustCompile(`(?i)\bblack\s+chrome\b`), tokens: []string{"black_chrome"}},
	{key: "noir", display: "Noir", re: regexp.MustCompile(`(?i)\bnoir\b`), tokens: []string{"noir"}},
	{key: "justice_is_gray", display: "Justice Is Gray", re: regexp.MustCompile(`(?i)\bjustice\s+is\s+gr(?:a|e)y\b`), tokens: []string{"justice_is_gray"}, comparisonSafeSuffix: true},
	{key: "anniversary_edition", display: "Anniversary Edition", re: regexp.MustCompile(`(?i)\b\d{1,3}(?:st|nd|rd|th)\s+anniversary\s+edition\b`), tokens: []string{"anniversary_edition"}},
}

var editionTokenOrder = []string{
	"theatrical",
	"unrated",
	"uncut",
	"uncensored",
	"extended",
	"director_cut",
	"editor_cut",
	"final_cut",
	"assembly_cut",
	"special_edition",
	"collectors_edition",
	"ultimate_edition",
	"anniversary_edition",
	"remastered",
	"restored",
	"open_matte",
	"imax",
	"imax_enhanced",
	"fan_edit",
	"despecialized",
	"workprint",
	"black_and_white",
	"black_chrome",
	"noir",
	"justice_is_gray",
	"hybrid",
}

// EditionDisplayLabel converts a canonical edition key into a user-facing label.
func EditionDisplayLabel(key string) string {
	normalized := strings.TrimSpace(strings.ToLower(key))
	if normalized == "" {
		return ""
	}
	for _, rule := range editionRules {
		if rule.key == normalized {
			return rule.display
		}
	}

	parts := strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(normalized))
	for i, part := range parts {
		switch part {
		case "imax":
			parts[i] = "IMAX"
		default:
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, " ")
}

func ParseVariantHints(filePath string, libraryType string, libraryRoots ...string) *VariantHints {
	cleanPath := filepath.ToSlash(filepath.Clean(filePath))
	if cleanPath == "" {
		return &VariantHints{}
	}

	hints := &VariantHints{}
	parts := strings.Split(cleanPath, "/")
	baseName := path.Base(cleanPath)
	baseNoExt := strings.TrimSuffix(baseName, path.Ext(baseName))
	parentBase := ""
	if len(parts) > 1 {
		parentBase = parts[len(parts)-2]
	}

	for i := len(parts) - 2; i >= 0; i-- {
		if match := plexEditionTagRe.FindStringSubmatch(parts[i]); match != nil {
			hints.EditionRaw = strings.TrimSpace(match[1])
			hints.EditionKey = normalizeEditionKey(hints.EditionRaw)
			hints.EditionSource = "plex_tag_folder"
			hints.EditionConfidence = floatPtr(1.0)
			break
		}
	}
	if hints.EditionKey == "" {
		if match := plexEditionTagRe.FindStringSubmatch(baseNoExt); match != nil {
			hints.EditionRaw = strings.TrimSpace(match[1])
			hints.EditionKey = normalizeEditionKey(hints.EditionRaw)
			hints.EditionSource = "plex_tag_file"
			hints.EditionConfidence = floatPtr(1.0)
		}
	}
	if hints.EditionKey == "" {
		for i := len(parts) - 2; i >= 0; i-- {
			if raw, key := parseEditionHeuristic(parts[i], "", 0); key != "" {
				hints.EditionRaw = raw
				hints.EditionKey = key
				hints.EditionSource = "heuristic_folder"
				hints.EditionConfidence = floatPtr(0.7)
				break
			}
		}
	}
	if hints.EditionKey == "" {
		folderTitle, folderYear, _ := parseInferFolderTitleYear(parentBase)
		if raw, key := parseEditionHeuristic(baseNoExt, folderTitle, folderYear); key != "" {
			hints.EditionRaw = raw
			hints.EditionKey = key
			hints.EditionSource = "heuristic_file"
			hints.EditionConfidence = floatPtr(0.7)
		}
	}
	if hints.EditionKey == "" {
		folderTitle, folderYear, _ := parseInferFolderTitleYear(parentBase)
		if stem := parseInferMovieStem(baseNoExt, folderTitle, folderYear); stem.Title != "" {
			if _, raw, key, ok := comparisonSafeEditionSuffix(stem.Title); ok {
				hints.EditionRaw = raw
				hints.EditionKey = key
				hints.EditionSource = "heuristic_file"
				hints.EditionConfidence = floatPtr(0.7)
			}
		}
	}

	seriesLibrary := normalizeInferLibraryType(libraryType) == seriesContentType
	libraryRoot := deepestContainingLibraryRoot(cleanPath, libraryRoots)
	directories := directorySegmentsWithinRoot(cleanPath, libraryRoot)
	allowNumericSeason := seriesLibrary && (libraryRoot == "" || len(directories) > 1)
	if token, ok := parseEpisodeToken(baseNoExt, directories, allowNumericSeason, seriesLibrary); ok && token.episodeEnd > token.episode {
		hints.PresentationKind = "multi_episode"
		hints.PresentationGroupKey = normalizePresentationGroup(baseNoExt, baseNoExt[token.start:token.end])
		hints.MultiEpisodeStart = token.episode
		hints.MultiEpisodeEnd = token.episodeEnd
		return hints
	}

	if match := presentationPartRe.FindStringSubmatch(" " + baseNoExt + " "); match != nil {
		partIndex, _ := strconv.Atoi(match[2])
		ctx := ResolvePathContext(cleanPath, libraryType, libraryRoots...)
		hints.PresentationKind = "multipart_movie"
		if ctx != nil && ctx.Type == "series" {
			hints.PresentationKind = "split_episode"
		}
		hints.PresentationPartIndex = partIndex
		hints.PresentationGroupKey = normalizePresentationGroup(baseNoExt, strings.TrimSpace(match[0]))
	}

	return hints
}

func editionHeuristicSurface(baseNoExt string, folderTitle string, folderYear int) string {
	surface := stripFolderTags(baseNoExt)
	surface = stripVariantReleaseGroup(surface)
	surface = plexEditionTagRe.ReplaceAllString(surface, " ")
	if stem := parseInferMovieStem(surface, folderTitle, folderYear); stem.Title != "" && stem.Year != 0 {
		surface = stem.Remainder
	} else if m := inferTitleYearRe.FindStringSubmatch(surface); m != nil {
		surface = strings.TrimSpace(strings.TrimPrefix(surface, m[0]))
	}
	surface = variantQualityTokenRe.ReplaceAllString(surface, " ")
	return collapseWhitespace(strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(surface))
}

func parseEditionHeuristic(surface string, folderTitle string, folderYear int) (string, string) {
	candidate := editionHeuristicSurface(surface, folderTitle, folderYear)
	if candidate == "" {
		return "", ""
	}
	lower := strings.ToLower(candidate)
	switch lower {
	case "holiday special", "christmas edition":
		return "", ""
	}
	type match struct {
		start int
		end   int
		raw   string
		rule  editionRule
	}
	matches := make([]match, 0, len(editionRules))
	tokenSet := make(map[string]struct{})
	for _, rule := range editionRules {
		indices := rule.re.FindAllStringIndex(candidate, -1)
		for _, idx := range indices {
			raw := collapseWhitespace(candidate[idx[0]:idx[1]])
			if raw == "" {
				continue
			}
			matches = append(matches, match{
				start: idx[0],
				end:   idx[1],
				raw:   raw,
				rule:  rule,
			})
			for _, token := range rule.tokens {
				tokenSet[token] = struct{}{}
			}
		}
	}
	if len(matches) == 0 {
		return "", ""
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].start == matches[j].start {
			return (matches[i].end - matches[i].start) > (matches[j].end - matches[j].start)
		}
		return matches[i].start < matches[j].start
	})

	rawParts := make([]string, 0, len(matches))
	lastEnd := -1
	for _, match := range matches {
		if match.start < lastEnd {
			continue
		}
		rawParts = append(rawParts, match.raw)
		lastEnd = match.end
	}

	orderedTokens := make([]string, 0, len(tokenSet))
	for _, token := range editionTokenOrder {
		if _, ok := tokenSet[token]; ok {
			orderedTokens = append(orderedTokens, token)
		}
	}
	if len(orderedTokens) == 0 {
		return "", ""
	}
	return collapseWhitespace(strings.Join(rawParts, " ")), strings.Join(orderedTokens, "_")
}

func comparisonSafeEditionSuffix(surface string) (prefix string, raw string, key string, ok bool) {
	cleaned := collapseWhitespace(strings.NewReplacer(".", " ", "_", " ").Replace(strings.TrimSpace(surface)))
	if cleaned == "" {
		return "", "", "", false
	}

	bestPrefix := ""
	bestRaw := ""
	bestKey := ""
	bestStart := -1
	for _, rule := range editionRules {
		if !rule.comparisonSafeSuffix {
			continue
		}
		indices := rule.re.FindAllStringIndex(cleaned, -1)
		for _, idx := range indices {
			if idx[1] != len(cleaned) {
				continue
			}
			candidatePrefix := collapseWhitespace(strings.TrimSpace(cleaned[:idx[0]]))
			if candidatePrefix == "" {
				continue
			}
			if idx[0] > bestStart {
				bestStart = idx[0]
				bestPrefix = candidatePrefix
				bestRaw = collapseWhitespace(cleaned[idx[0]:idx[1]])
				bestKey = rule.key
			}
		}
	}
	if bestStart < 0 {
		return "", "", "", false
	}
	return bestPrefix, bestRaw, bestKey, true
}

func StripComparisonSafeEditionSuffix(surface string) string {
	if prefix, _, _, ok := comparisonSafeEditionSuffix(surface); ok {
		return prefix
	}
	return surface
}

func normalizeEditionKey(raw string) string {
	cleaned := strings.TrimSpace(strings.ToLower(raw))
	cleaned = strings.NewReplacer("'", "", "\"", "", "&", " and ").Replace(cleaned)
	cleaned = variantCleanupSepRe.ReplaceAllString(cleaned, " ")
	cleaned = variantWhitespaceRe.ReplaceAllString(cleaned, " ")
	cleaned = strings.TrimSpace(cleaned)
	return strings.ReplaceAll(cleaned, " ", "_")
}

func stripVariantReleaseGroup(surface string) string {
	match := variantReleaseGroupRe.FindStringSubmatchIndex(surface)
	if len(match) < 4 {
		return surface
	}
	return strings.TrimSpace(surface[:match[2]-1])
}

func inferEditionTokenKey(token string) string {
	switch strings.ToLower(strings.TrimSpace(token)) {
	case "theatrical", "release":
		return "theatrical"
	case "unrated":
		return "unrated"
	case "uncut":
		return "uncut"
	case "uncensored":
		return "uncensored"
	case "extended", "cut", "edition":
		return "extended"
	case "director", "directors":
		return "director_cut"
	case "editor", "editors":
		return "editor_cut"
	case "final":
		return "final_cut"
	case "assembly":
		return "assembly_cut"
	case "imax":
		return "imax"
	case "open", "matte":
		return "open_matte"
	case "special":
		return "special_edition"
	case "collector", "collectors":
		return "collectors_edition"
	case "ultimate":
		return "ultimate_edition"
	case "anniversary":
		return "anniversary_edition"
	case "fan":
		return "fan_edit"
	case "despecialized", "despecialised":
		return "despecialized"
	case "remastered":
		return "remastered"
	case "restored":
		return "restored"
	case "workprint":
		return "workprint"
	case "hybrid":
		return "hybrid"
	case "black", "white":
		return "black_and_white"
	case "chrome":
		return "black_chrome"
	case "noir":
		return "noir"
	default:
		return ""
	}
}

func normalizePresentationGroup(baseNoExt string, matched string) string {
	group := strings.Replace(baseNoExt, matched, " ", 1)
	group = strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(group)
	group = variantWhitespaceRe.ReplaceAllString(group, " ")
	return strings.TrimSpace(group)
}

func floatPtr(value float64) *float64 {
	return &value
}
