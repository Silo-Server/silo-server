package autoscan

import "strings"

// RewriteSuggestions is the result of matching arr root folders to Silo folders.
type RewriteSuggestions struct {
	Proposed  []ProposedRewrite `json:"proposed"`
	Unmatched []string          `json:"unmatched"`
	Ambiguous []AmbiguousRoot   `json:"ambiguous"`
	Covered   []string          `json:"covered"`
}

// ProposedRewrite is a suggested rewrite plus its confidence (shared trailing segments).
type ProposedRewrite struct {
	From       string `json:"from"`
	To         string `json:"to"`
	MatchDepth int    `json:"match_depth"`
}

// AmbiguousRoot is an arr root that tied across multiple Silo folders.
type AmbiguousRoot struct {
	Root       string   `json:"root"`
	Candidates []string `json:"candidates"`
}

// normalizePath makes a path comparable: backslashes -> '/', collapse duplicate
// slashes, strip a trailing slash (but keep a bare "/").
func normalizePath(p string) string {
	p = normalizeSeparators(strings.TrimSpace(p))
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return p
}

func segments(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// commonSuffixLen counts equal trailing segments (full-segment equality).
func commonSuffixLen(a, b []string) int {
	i, j, n := len(a)-1, len(b)-1, 0
	for i >= 0 && j >= 0 && a[i] == b[j] {
		n++
		i--
		j--
	}
	return n
}

// coveredBy reports whether an existing rewrite already matches root (same
// boundary rule as applyRewrites).
func coveredBy(root string, existing []PathRewrite) bool {
	for _, rw := range existing {
		from := strings.TrimRight(strings.TrimSpace(rw.From), "/")
		if from == "" {
			continue
		}
		if root == from || strings.HasPrefix(root, from+"/") {
			return true
		}
	}
	return false
}

// suggestRewrites matches each arr root to the Silo folder sharing the most
// trailing path segments (unique winner). Pure: no I/O, no deployment constants.
func suggestRewrites(arrRoots, siloFolderPaths []string, existing []PathRewrite) RewriteSuggestions {
	siloNorm := make([]string, 0, len(siloFolderPaths))
	siloSegs := make([][]string, 0, len(siloFolderPaths))
	for _, p := range siloFolderPaths {
		n := normalizePath(p)
		if n == "" {
			continue
		}
		siloNorm = append(siloNorm, n)
		siloSegs = append(siloSegs, segments(n))
	}

	var out RewriteSuggestions
	for _, raw := range arrRoots {
		root := normalizePath(raw)
		if root == "" {
			continue
		}
		if coveredBy(root, existing) {
			out.Covered = append(out.Covered, root)
			continue
		}
		rootSegs := segments(root)
		best := 0
		var winners []string
		for i, segs := range siloSegs {
			n := commonSuffixLen(rootSegs, segs)
			if n == 0 {
				continue
			}
			if n > best {
				best, winners = n, []string{siloNorm[i]}
			} else if n == best {
				winners = append(winners, siloNorm[i])
			}
		}
		switch {
		case best == 0:
			out.Unmatched = append(out.Unmatched, root)
		case len(winners) == 1:
			out.Proposed = append(out.Proposed, ProposedRewrite{From: root, To: winners[0], MatchDepth: best})
		default:
			out.Ambiguous = append(out.Ambiguous, AmbiguousRoot{Root: root, Candidates: winners})
		}
	}
	return out
}
