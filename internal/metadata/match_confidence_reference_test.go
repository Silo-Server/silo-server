package metadata

// These reference functions preserve the pre-optimization scoring and selection
// algorithms. Shared normalization, volume parsing, and tie-break helpers are
// intentionally unchanged by this patch.
import "strings"

func originalTitleScore(want, candidate string) float64 {
	w, c := normaliseTitle(want), normaliseTitle(candidate)
	if w == "" || c == "" {
		return 0
	}
	if w == c {
		return 1
	}

	if wv, wok := titleVolume(want); wok {
		if cv, cok := titleVolume(candidate); cok && wv != cv {
			return 0
		}
	}

	score := diceCoefficient(w, c)

	// One title fully containing the other is strong evidence: providers
	// routinely return "Title (Series Book 2)" for "Title", and our scanner
	// routinely has "Series 2 - Title" for "Title".
	// The floor applies to the *contained* title, not the containing one: a
	// long candidate does not make a short query specific.
	if shorter := min(len(w), len(c)); shorter >= minContainmentLen {
		if strings.Contains(w, c) || strings.Contains(c, w) {
			if containment := 0.9; containment > score {
				score = containment
			}
		}
	}
	return score
}

func originalSelectBestMatchYear(want string, wantYear int, results []SearchResult) (bestMatchSelection, bool) {
	best := bestMatchSelection{}
	found := false

	for _, r := range results {
		name := r.Name
		if strings.TrimSpace(name) == "" {
			name = r.OriginalTitle
		}

		// A volume stated on the primary title that contradicts the wanted
		// volume disqualifies the whole result, aliases included. Aliases are
		// often the volume-less series name, and letting one rescue a
		// wrong-volume primary would persist IDs for a different book --
		// "Dungeon In My Closet, Book 5" must not be accepted for volume 2 via
		// its generic "Dungeon In My Closet" alias.
		if wv, wok := titleVolume(want); wok {
			if cv, cok := titleVolume(name); cok && cv != wv {
				continue
			}
		}

		score := originalTitleScore(want, name)
		matchedTitle := name

		// Aliases are provider-confirmed titles for the same work, so a
		// translated or regional spelling should not be penalized.
		for _, alias := range r.TitleAliases {
			if s := originalTitleScore(want, alias.Title); s > score {
				score = s
				matchedTitle = alias.Title
			}
		}

		switch {
		case score > best.score+scoreTieEpsilon:
			best = bestMatchSelection{result: r, score: score, matchedTitle: matchedTitle}
			found = true
		case found && score > best.score-scoreTieEpsilon:
			// Effectively tied on title. Prefer the nearer year when both are
			// known; otherwise keep the incumbent.
			if yearIsCloser(wantYear, r.Year, best.result.Year) {
				best = bestMatchSelection{result: r, score: score, matchedTitle: matchedTitle}
			}
		}
	}

	// Strictly greater, not >=. A two-word title sharing exactly one word with
	// a two-word candidate scores precisely 0.5 -- "Malcolm X" against
	// "Malcolm 10" -- and that is the weakest possible evidence, not a match.
	// Nothing correct is lost: in the calibration sample the worst true match
	// scores 0.86.
	if !found || best.score <= matchThreshold() {
		return bestMatchSelection{}, false
	}
	return best, true
}
