package metadata

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Silo-Server/silo-server/internal/naming"
)

const (
	// Search APIs often return many same-title remakes. Three candidates was too
	// shallow for real libraries (the correct "Memories" result was below it),
	// while eight keeps the worst-case provider work bounded.
	maxEpisodeValidationCandidates = 8
	maxEpisodeValidationProviders  = 2
	maxEpisodeValidationSamples    = 3
	maxEpisodeValidationSeasons    = 3
	episodeValidationTimeout       = 10 * time.Second
)

var (
	episodeCodePattern          = regexp.MustCompile(`(?i)s\d{1,4}e\d{1,3}(?:e\d{1,3})?`)
	episodeReleaseSuffixPattern = regexp.MustCompile(
		`(?i)(?:^|[ ._-])(?:2160p|1080p|720p|576p|480p|webrip|web[ ._-]?dl|bluray|blu[ ._-]?ray|bdrip|hdtv|dvdrip|remux|x26[45]|h26[45]|hevc|av1|aac|ac3|eac3|dts(?:[ ._-]?hd)?|flac|hdr10?|dolby[ ._-]?vision)(?:$|[ ._-])`,
	)
	episodeGenericTitlePattern   = regexp.MustCompile(`(?i)^(?:tba|tbd|pilot|episode|episode\s+\d+|ep\.?\s*\d+|part\s+\d+|chapter\s+\d+|\d+)$`)
	episodeTechnicalTitlePattern = regexp.MustCompile(`(?i)^(?:\d{3,4}p|web(?:rip|dl)?|bluray|hdtv|remux|x26[45]|h26[45]|hevc)(?:\b|[ ._-])`)
)

type localEpisodeMatchHint struct {
	SeasonNumber  int
	EpisodeNumber int
	Title         string
}

type episodeCandidateValidation struct {
	candidate       MatchCandidate
	provider        string
	coordinates     int
	compared        int
	matches         int
	exactMatches    int
	similarityTotal float64
	evaluated       bool
	err             error
}

type episodeValidationProvider struct {
	provider EpisodeProvider
	slug     string
}

// validateSeriesMatchByEpisodes uses a small amount of episode-title evidence
// to resolve otherwise ambiguous exact-title series searches. It is deliberately
// positive-only: unavailable, generic, or disagreeing episode data leaves the
// item unmatched instead of turning absence of evidence into a rejection.
func validateSeriesMatchByEpisodes(
	ctx context.Context,
	hints *MatchHints,
	candidates []MatchCandidate,
	providers []Provider,
	language string,
) (*MatchCandidate, []error) {
	if hints == nil || hints.Type != matchContentTypeSeries || trustedHintIDsPresent(hints) {
		return nil, nil
	}

	// Coordinate selection keeps useful episode titles when present, but its
	// primary purpose is to build a bounded series-shape fingerprint: the last
	// locally present episode from up to three spread-out seasons.
	localEpisodes := selectLocalEpisodeCoordinateHints(hints.AllGroupFilePaths)
	if len(localEpisodes) == 0 {
		return nil, nil
	}

	contenders, contendersExhaustive := episodeValidationContenders(hints, candidates)
	if len(contenders) == 0 {
		return nil, nil
	}

	validationCtx, cancel := context.WithTimeout(ctx, episodeValidationTimeout)
	defer cancel()

	validations := make([]episodeCandidateValidation, len(contenders))
	var wg sync.WaitGroup
	for i := range contenders {
		candidateProviders := selectEpisodeValidationProviders(contenders[i], providers)
		validations[i].candidate = contenders[i]
		if len(candidateProviders) == 0 {
			continue
		}

		wg.Add(1)
		go func(index int, episodeProviders []episodeValidationProvider) {
			defer wg.Done()
			validations[index] = validateEpisodeCandidateAcrossProviders(
				validationCtx,
				validations[index].candidate,
				episodeProviders,
				localEpisodes,
				language,
			)
		}(i, candidateProviders)
	}
	wg.Wait()

	errs := make([]error, 0, len(validations))
	for _, validation := range validations {
		if validation.err != nil {
			errs = append(errs, validation.err)
		}
		// Every contender must have been checked successfully. Otherwise a
		// temporarily unavailable rival could be incorrectly treated as a zero.
		if !validation.evaluated || validation.err != nil {
			return nil, errs
		}
	}

	titleValidations := append([]episodeCandidateValidation(nil), validations...)
	sort.SliceStable(titleValidations, func(i, j int) bool {
		if titleValidations[i].matches != titleValidations[j].matches {
			return titleValidations[i].matches > titleValidations[j].matches
		}
		if titleValidations[i].exactMatches != titleValidations[j].exactMatches {
			return titleValidations[i].exactMatches > titleValidations[j].exactMatches
		}
		return titleValidations[i].similarityTotal > titleValidations[j].similarityTotal
	})

	best := titleValidations[0]
	if episodeEvidenceIsSufficient(best, localEpisodes) &&
		(len(titleValidations) == 1 || episodeEvidenceClearlyWins(best, titleValidations[1])) {
		winner := best.candidate
		winner.MatchReasons = append(winner.MatchReasons,
			fmt.Sprintf("episode_title_corroboration:%d_of_%d", best.matches, len(localEpisodes)))
		return &winner, errs
	}

	coordinateValidations := append([]episodeCandidateValidation(nil), validations...)
	sort.SliceStable(coordinateValidations, func(i, j int) bool {
		if coordinateValidations[i].coordinates != coordinateValidations[j].coordinates {
			return coordinateValidations[i].coordinates > coordinateValidations[j].coordinates
		}
		return episodeValidationEvidenceBetter(coordinateValidations[i], coordinateValidations[j])
	})
	best = coordinateValidations[0]
	var next episodeCandidateValidation
	if len(coordinateValidations) > 1 {
		next = coordinateValidations[1]
	}
	if !episodeCoordinateEvidenceIsSufficient(
		best, next, localEpisodes, len(coordinateValidations), contendersExhaustive,
	) {
		return nil, errs
	}

	winner := best.candidate
	winner.MatchReasons = append(winner.MatchReasons,
		fmt.Sprintf("episode_coordinate_corroboration:%d_of_%d", best.coordinates, len(localEpisodes)))
	return &winner, errs
}

func episodeValidationContenders(hints *MatchHints, candidates []MatchCandidate) ([]MatchCandidate, bool) {
	scored := make([]scoredMatchCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		annotateCandidateMatch(&candidate, hints)
		titleSimilarity, _ := bestCandidateTitleSimilarity(hints.Title, candidate, 0)
		exactTitleCandidate := candidate.MatchScore >= automaticMatchAcceptanceFloor && titleSimilarity == 1
		crossProviderYearCandidate := hints.Year != 0 && candidate.Year == hints.Year &&
			candidateCorroboratingSourceCount(candidate) >= 2
		if (!exactTitleCandidate && !crossProviderYearCandidate) ||
			!candidateTypeMatchesHint(hints.Type, candidate.ContentType) || providerIDRichness(candidate.ProviderIDs) == 0 {
			continue
		}
		scored = append(scored, scoredMatchCandidate{candidate: candidate, score: candidate.MatchScore})
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	exhaustive := len(scored) <= maxEpisodeValidationCandidates
	if len(scored) > maxEpisodeValidationCandidates {
		scored = scored[:maxEpisodeValidationCandidates]
	}
	out := make([]MatchCandidate, 0, len(scored))
	for _, candidate := range scored {
		out = append(out, candidate.candidate)
	}
	return out, exhaustive
}

func selectEpisodeValidationProviders(candidate MatchCandidate, providers []Provider) []episodeValidationProvider {
	// TMDB fetches exactly one requested season, making it the cheapest
	// validator when both first-party IDs are present. The configured chain
	// order remains the fallback for TVDB-only and third-party candidates.
	ordered := make([]Provider, 0, len(providers))
	for _, provider := range providers {
		if strings.EqualFold(provider.Slug(), "tmdb") {
			ordered = append(ordered, provider)
		}
	}
	for _, provider := range providers {
		if !strings.EqualFold(provider.Slug(), "tmdb") {
			ordered = append(ordered, provider)
		}
	}

	selected := make([]episodeValidationProvider, 0, maxEpisodeValidationProviders)
	for _, provider := range ordered {
		if _, local := provider.(IdentityHintProvider); local {
			continue
		}
		episodeProvider, ok := provider.(EpisodeProvider)
		if !ok {
			continue
		}
		providerSlug := strings.ToLower(strings.TrimSpace(provider.Slug()))
		if strings.TrimSpace(candidate.ProviderIDs[providerSlug]) == "" {
			continue
		}
		selected = append(selected, episodeValidationProvider{provider: episodeProvider, slug: providerSlug})
		if len(selected) == maxEpisodeValidationProviders {
			break
		}
	}
	return selected
}

func validateEpisodeCandidateAcrossProviders(
	ctx context.Context,
	candidate MatchCandidate,
	providers []episodeValidationProvider,
	localEpisodes []localEpisodeMatchHint,
	language string,
) episodeCandidateValidation {
	best := episodeCandidateValidation{candidate: candidate}
	var lastErr error
	for _, provider := range providers {
		validation := validateEpisodeCandidate(
			ctx, candidate, provider.slug, provider.provider, localEpisodes, language,
		)
		if validation.err != nil {
			lastErr = validation.err
			continue
		}
		if !best.evaluated || episodeValidationEvidenceBetter(validation, best) {
			best = validation
		}
		if episodeEvidenceIsSufficient(validation, localEpisodes) {
			return validation
		}
	}
	if best.evaluated {
		return best
	}
	best.err = lastErr
	return best
}

func episodeValidationEvidenceBetter(left, right episodeCandidateValidation) bool {
	if left.matches != right.matches {
		return left.matches > right.matches
	}
	if left.exactMatches != right.exactMatches {
		return left.exactMatches > right.exactMatches
	}
	if left.similarityTotal != right.similarityTotal {
		return left.similarityTotal > right.similarityTotal
	}
	if left.compared != right.compared {
		return left.compared > right.compared
	}
	return left.coordinates > right.coordinates
}

func validateEpisodeCandidate(
	ctx context.Context,
	candidate MatchCandidate,
	providerSlug string,
	provider EpisodeProvider,
	localEpisodes []localEpisodeMatchHint,
	language string,
) episodeCandidateValidation {
	validation := episodeCandidateValidation{candidate: candidate, provider: providerSlug}
	validation.evaluated = true

	localBySeason := make(map[int][]localEpisodeMatchHint)
	seasonNumbers := make([]int, 0, maxEpisodeValidationSeasons)
	for _, episode := range localEpisodes {
		if _, exists := localBySeason[episode.SeasonNumber]; !exists {
			seasonNumbers = append(seasonNumbers, episode.SeasonNumber)
		}
		localBySeason[episode.SeasonNumber] = append(localBySeason[episode.SeasonNumber], episode)
	}
	sort.Ints(seasonNumbers)

	for _, seasonNumber := range seasonNumbers {
		episodes, err := provider.GetEpisodes(ctx, EpisodesRequest{
			ProviderIDs:  copyMap(candidate.ProviderIDs),
			SeasonNumber: seasonNumber,
			Language:     language,
		})
		if err != nil {
			if isProvider404(err) {
				// A missing season is valid negative evidence for this candidate.
				continue
			}
			validation.err = fmt.Errorf("episode validation via %s for %s season %d: %w", providerSlug, candidate.Title, seasonNumber, err)
			return validation
		}

		providerEpisodes := make(map[int]EpisodeResult, len(episodes))
		for _, episode := range episodes {
			if episode.SeasonNumber == seasonNumber {
				providerEpisodes[episode.EpisodeNumber] = episode
			}
		}
		for _, localEpisode := range localBySeason[seasonNumber] {
			providerEpisode, ok := providerEpisodes[localEpisode.EpisodeNumber]
			if !ok {
				continue
			}
			validation.coordinates++
			if isGenericEpisodeMatchTitle(localEpisode.Title) || isGenericEpisodeMatchTitle(providerEpisode.Title) {
				continue
			}
			validation.compared++
			similarity := episodeTitleSimilarity(localEpisode.Title, providerEpisode.Title)
			if similarity < 0.8 {
				continue
			}
			validation.matches++
			validation.similarityTotal += similarity
			if similarity == 1 {
				validation.exactMatches++
			}
		}
	}
	return validation
}

func episodeEvidenceIsSufficient(validation episodeCandidateValidation, localEpisodes []localEpisodeMatchHint) bool {
	if len(localEpisodes) >= 2 {
		return validation.matches >= 2
	}
	return len(localEpisodes) == 1 && validation.matches == 1 && validation.exactMatches == 1 &&
		isDistinctiveSingleEpisodeTitle(localEpisodes[0].Title)
}

func episodeEvidenceClearlyWins(best, next episodeCandidateValidation) bool {
	if best.matches != next.matches {
		return best.matches > next.matches
	}
	if best.exactMatches != next.exactMatches {
		return best.exactMatches > next.exactMatches
	}
	return best.similarityTotal-next.similarityTotal >= 0.2
}

func episodeCoordinateEvidenceIsSufficient(
	validation episodeCandidateValidation,
	next episodeCandidateValidation,
	localEpisodes []localEpisodeMatchHint,
	contenderCount int,
	contendersExhaustive bool,
) bool {
	if len(localEpisodes) < 2 || validation.coordinates != len(localEpisodes) {
		return false
	}
	// Two coordinates spread across two seasons carry meaningful series-shape
	// evidence. Within one season, require all three spread-out samples; merely
	// finding two early episodes is common across unrelated same-title series.
	if distinctEpisodeSeasons(localEpisodes) < 2 && len(localEpisodes) < maxEpisodeValidationSamples {
		return false
	}
	// Coordinates cannot prove that a differently named candidate is an alias.
	// Cross-provider exact-year title variants are eligible for validation, but
	// they must win through matching episode titles. Coordinate-only promotion
	// remains limited to an exact normalized series title.
	if !slices.Contains(validation.candidate.MatchReasons, "exact_title") {
		return false
	}
	if contenderCount > 1 {
		// Shape evidence can only choose between multiple exact-title candidates
		// when the bounded candidate set was exhaustive and exactly one candidate
		// contains every sampled coordinate. A truncated search result remains
		// unmatched because an unchecked rival may have the same shape.
		if !contendersExhaustive || next.coordinates == len(localEpisodes) {
			return false
		}
		if distinctEpisodeSeasons(localEpisodes) < 2 {
			return false
		}
	}
	discriminating := episodeCoordinatesAreDiscriminating(localEpisodes)
	return discriminating || candidateCorroboratingSourceCount(validation.candidate) >= 2
}

func distinctEpisodeSeasons(episodes []localEpisodeMatchHint) int {
	seasons := make(map[int]struct{})
	for _, episode := range episodes {
		seasons[episode.SeasonNumber] = struct{}{}
	}
	return len(seasons)
}

func episodeCoordinatesAreDiscriminating(episodes []localEpisodeMatchHint) bool {
	for _, episode := range episodes {
		if episode.SeasonNumber > 1 || episode.EpisodeNumber >= 6 {
			return true
		}
	}
	return false
}

func candidateCorroboratingSourceCount(candidate MatchCandidate) int {
	seen := make(map[string]struct{}, len(candidate.Sources))
	for _, source := range candidate.Sources {
		source = strings.ToLower(strings.TrimSpace(source))
		if source == "" || nonCorroboratingSources[source] {
			continue
		}
		seen[source] = struct{}{}
	}
	return len(seen)
}

func selectLocalEpisodeCoordinateHints(paths []string) []localEpisodeMatchHint {
	bySeason := make(map[int]map[int]localEpisodeMatchHint)
	for _, path := range paths {
		parsed := naming.ParseFilename(path, "series")
		if parsed == nil || parsed.EpisodeNum <= 0 {
			continue
		}
		if bySeason[parsed.SeasonNum] == nil {
			bySeason[parsed.SeasonNum] = make(map[int]localEpisodeMatchHint)
		}
		title := extractEpisodeMatchTitle(path)
		if isGenericEpisodeMatchTitle(title) {
			title = ""
		}
		existing, exists := bySeason[parsed.SeasonNum][parsed.EpisodeNum]
		if exists && utf8.RuneCountInString(existing.Title) >= utf8.RuneCountInString(title) {
			continue
		}
		bySeason[parsed.SeasonNum][parsed.EpisodeNum] = localEpisodeMatchHint{
			SeasonNumber: parsed.SeasonNum, EpisodeNumber: parsed.EpisodeNum, Title: title,
		}
	}

	seasonNumbers := make([]int, 0, len(bySeason))
	for season, episodes := range bySeason {
		if len(episodes) > 0 {
			seasonNumbers = append(seasonNumbers, season)
		}
	}
	if len(seasonNumbers) == 0 {
		return nil
	}
	sort.Ints(seasonNumbers)

	// Specials are weak shape evidence. Prefer numbered seasons whenever they
	// are available, retaining season zero only for specials-only libraries.
	numberedSeasons := make([]int, 0, len(seasonNumbers))
	for _, season := range seasonNumbers {
		if season > 0 {
			numberedSeasons = append(numberedSeasons, season)
		}
	}
	if len(numberedSeasons) > 0 {
		seasonNumbers = numberedSeasons
	}

	if len(seasonNumbers) == 1 {
		selectedSeason := seasonNumbers[0]
		episodeNumbers := sortedEpisodeNumbers(bySeason[selectedSeason])
		if len(episodeNumbers) > maxEpisodeValidationSamples {
			episodeNumbers = []int{
				episodeNumbers[0],
				episodeNumbers[len(episodeNumbers)/2],
				episodeNumbers[len(episodeNumbers)-1],
			}
		}
		out := make([]localEpisodeMatchHint, 0, len(episodeNumbers))
		for _, episodeNumber := range episodeNumbers {
			out = append(out, bySeason[selectedSeason][episodeNumber])
		}
		return out
	}

	if len(seasonNumbers) > maxEpisodeValidationSeasons {
		seasonNumbers = []int{
			seasonNumbers[0],
			seasonNumbers[len(seasonNumbers)/2],
			seasonNumbers[len(seasonNumbers)-1],
		}
	}

	// The last locally present episode of several seasons is far more
	// discriminating than S01E01-style existence checks: same-name remakes
	// rarely share both season count and episode count per season.
	out := make([]localEpisodeMatchHint, 0, len(seasonNumbers))
	for _, seasonNumber := range seasonNumbers {
		episodeNumbers := sortedEpisodeNumbers(bySeason[seasonNumber])
		out = append(out, bySeason[seasonNumber][episodeNumbers[len(episodeNumbers)-1]])
	}
	return out
}

func sortedEpisodeNumbers(episodes map[int]localEpisodeMatchHint) []int {
	numbers := make([]int, 0, len(episodes))
	for episodeNumber := range episodes {
		numbers = append(numbers, episodeNumber)
	}
	sort.Ints(numbers)
	return numbers
}

func extractEpisodeMatchTitle(path string) string {
	base := filepath.Base(path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	location := episodeCodePattern.FindStringIndex(stem)
	if location == nil {
		return ""
	}
	title := strings.TrimLeft(stem[location[1]:], " ._-")
	if cut := strings.IndexAny(title, "[{"); cut >= 0 {
		title = title[:cut]
	}
	if location := episodeReleaseSuffixPattern.FindStringIndex(title); location != nil {
		title = title[:location[0]]
	}
	title = strings.NewReplacer(".", " ", "_", " ").Replace(title)
	return strings.Join(strings.Fields(strings.Trim(title, " -")), " ")
}

func isGenericEpisodeMatchTitle(title string) bool {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(title)), " ")
	return normalized == "" || episodeGenericTitlePattern.MatchString(normalized) || episodeTechnicalTitlePattern.MatchString(normalized)
}

func isDistinctiveSingleEpisodeTitle(title string) bool {
	normalized := normalizeTitleForScoring(title)
	return !isGenericEpisodeMatchTitle(normalized) && utf8.RuneCountInString(normalized) >= 10 && len(strings.Fields(normalized)) >= 2
}

func episodeTitleSimilarity(left, right string) float64 {
	leftComparable := normalizeTitleForScoring(left)
	rightComparable := normalizeTitleForScoring(right)
	if leftComparable == "" || rightComparable == "" {
		return 0
	}
	if leftComparable == rightComparable {
		return 1
	}
	if naming.InferTitlesCoherent(leftComparable, rightComparable) {
		return 0.8
	}
	return 0
}
