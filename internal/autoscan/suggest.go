package autoscan

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrNoConnection is returned when the rewrite suggester is asked for a source
// that has no bound connection. The admin API surfaces it as a 400.
var ErrNoConnection = errors.New("autoscan: source has no bound connection")

// SuggestRewrites resolves the source's bound connection, lists the arr root
// folders and the Silo media-folder paths, and suffix-matches arr roots to Silo
// folders to propose path rewrites. The source's existing rewrites are passed so
// already-covered roots are reported separately. Returns ErrNoConnection when
// the source has no bound connection (the handler maps it to 400).
func (s *Service) SuggestRewrites(ctx context.Context, sourceID string) (RewriteSuggestions, error) {
	if s.rootFolders == nil || s.folders == nil {
		return RewriteSuggestions{}, fmt.Errorf("autoscan: rewrite suggester not configured")
	}
	src, err := s.store.GetSource(ctx, sourceID)
	if err != nil {
		return RewriteSuggestions{}, err
	}
	if src.ConnectionID == nil {
		return RewriteSuggestions{}, ErrNoConnection
	}
	conn, err := s.store.GetConnection(ctx, *src.ConnectionID)
	if err != nil {
		return RewriteSuggestions{}, err
	}
	resolved, err := s.connres.Resolve(ctx, conn)
	if err != nil {
		return RewriteSuggestions{}, fmt.Errorf("autoscan: resolve connection: %w", err)
	}
	arrRoots, err := s.rootFolders.RootFolders(ctx, resolved.BaseURL, resolved.APIKey)
	if err != nil {
		return RewriteSuggestions{}, fmt.Errorf("autoscan: list arr root folders: %w", err)
	}
	siloFolders, err := s.folders.ListFolderPaths(ctx)
	if err != nil {
		return RewriteSuggestions{}, fmt.Errorf("autoscan: list silo folders: %w", err)
	}
	return suggestRewrites(arrRoots, siloFolders, src.PathRewrites), nil
}

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
// boundary rule as applyRewrites). The existing From is normalized too, so a
// stored Windows/dup-slash rewrite still covers a normalized root.
func coveredBy(root string, existing []PathRewrite) bool {
	for _, rw := range existing {
		from := normalizePath(rw.From)
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
	siloSeen := make(map[string]struct{})
	for _, p := range siloFolderPaths {
		n := normalizePath(p)
		if n == "" {
			continue
		}
		if _, dup := siloSeen[n]; dup { // dedup Silo paths so candidates are distinct
			continue
		}
		siloSeen[n] = struct{}{}
		siloNorm = append(siloNorm, n)
		siloSegs = append(siloSegs, segments(n))
	}

	// Initialize to non-nil so the JSON response always has arrays ([]) rather
	// than null — the frontend maps over these directly.
	out := RewriteSuggestions{
		Proposed:  []ProposedRewrite{},
		Unmatched: []string{},
		Ambiguous: []AmbiguousRoot{},
		Covered:   []string{},
	}
	rootSeen := make(map[string]struct{})
	for _, raw := range arrRoots {
		root := normalizePath(raw)
		if root == "" {
			continue
		}
		if _, dup := rootSeen[root]; dup { // dedup arr roots that normalize alike
			continue
		}
		rootSeen[root] = struct{}{}
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
			if winners[0] == root {
				continue // arr path already equals the Silo path — no rewrite needed
			}
			out.Proposed = append(out.Proposed, ProposedRewrite{From: root, To: winners[0], MatchDepth: best})
		default:
			out.Ambiguous = append(out.Ambiguous, AmbiguousRoot{Root: root, Candidates: winners})
		}
	}
	return out
}
