package smartcoll

import (
	"context"
	"hash/fnv"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Item is the audiobook-domain projection the evaluator walks. The
// handler builds these from silo's *models.MediaItem + people/series
// lookups (see siloItemToSmartcollItem in smart_collections_handler.go).
type Item struct {
	ID              string
	Title           string
	Authors         []string
	Narrators       []string
	Series          []string
	Genres          []string
	Year            int
	Rating          float64
	Language        string
	Publisher       string
	AddedAt         time.Time
	DurationSeconds int
}

// Candidate is the input shape to Evaluate — one Item plus the optional
// per-user state needed by personalized rules.
type Candidate struct {
	Item           Item
	IsFinished     bool
	ProgressPct    float32
	CurrentSeconds int
	LastPlayedAt   time.Time
	BookmarkCount  int
	PlayCount      int
}

// EvaluateOptions controls non-rule aspects of evaluation.
type EvaluateOptions struct {
	AllowPersonalized bool
	UserSeed          string
	Now               time.Time
	AbandonedAfter    time.Duration
}

// Evaluate filters the candidate list by qd's rule tree and sorts the
// survivors by qd.Sort. Pure function — no side effects, no I/O.
func Evaluate(ctx context.Context, qd QueryDefinition, candidates []Candidate, opts EvaluateOptions) []Candidate {
	_ = ctx
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.AbandonedAfter == 0 {
		opts.AbandonedAfter = 60 * 24 * time.Hour
	}
	qd = qd.Normalize()
	out := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		if matchDefinition(qd, c, opts) {
			out = append(out, c)
		}
	}
	sortCandidates(out, qd.Sort, opts)
	if qd.Limit != nil && *qd.Limit > 0 && *qd.Limit < len(out) {
		out = out[:*qd.Limit]
	}
	return out
}

func matchDefinition(qd QueryDefinition, c Candidate, opts EvaluateOptions) bool {
	if len(qd.Groups) == 0 {
		return true
	}
	if qd.Match == "any" {
		for _, g := range qd.Groups {
			if matchGroup(g, c, opts) {
				return true
			}
		}
		return false
	}
	for _, g := range qd.Groups {
		if !matchGroup(g, c, opts) {
			return false
		}
	}
	return true
}

func matchGroup(g QueryGroup, c Candidate, opts EvaluateOptions) bool {
	if len(g.Rules) == 0 {
		return true
	}
	if g.Match == "any" {
		for _, r := range g.Rules {
			if matchRule(r, c, opts) {
				return true
			}
		}
		return false
	}
	for _, r := range g.Rules {
		if !matchRule(r, c, opts) {
			return false
		}
	}
	return true
}

func matchRule(r QueryRule, c Candidate, opts EvaluateOptions) bool {
	def, ok := queryFieldDefs[r.Field]
	if !ok {
		return false
	}
	if def.personalized && !opts.AllowPersonalized {
		return false
	}
	switch r.Field {
	case "title":
		return matchString(c.Item.Title, r.Op, r.Value)
	case "author":
		return matchStringSlice(c.Item.Authors, r.Op, r.Value)
	case "narrator":
		return matchStringSlice(c.Item.Narrators, r.Op, r.Value)
	case "series":
		return matchStringSlice(c.Item.Series, r.Op, r.Value)
	case "genre":
		return matchStringSlice(c.Item.Genres, r.Op, r.Value)
	case "year":
		return matchInt(c.Item.Year, r.Op, r.Value)
	case "rating":
		return matchFloat(c.Item.Rating, r.Op, r.Value)
	case "language":
		return matchString(c.Item.Language, r.Op, r.Value)
	case "publisher":
		return matchString(c.Item.Publisher, r.Op, r.Value)
	case "added_at":
		return matchTime(c.Item.AddedAt, r.Op, r.Value, opts.Now)
	case "duration_seconds":
		return matchInt(c.Item.DurationSeconds, r.Op, r.Value)
	case "finished":
		return matchBool(c.IsFinished, r.Op, r.Value)
	case "in_progress":
		inProg := !c.IsFinished && c.CurrentSeconds > 0
		return matchBool(inProg, r.Op, r.Value)
	case "last_played":
		return matchTime(c.LastPlayedAt, r.Op, r.Value, opts.Now)
	case "abandoned":
		inProg := !c.IsFinished && c.CurrentSeconds > 0
		ab := inProg && !c.LastPlayedAt.IsZero() && opts.Now.Sub(c.LastPlayedAt) > opts.AbandonedAfter
		return matchBool(ab, r.Op, r.Value)
	case "bookmark_count":
		return matchInt(c.BookmarkCount, r.Op, r.Value)
	}
	return false
}

func matchString(field string, op string, val any) bool {
	s, ok := stringValue(val)
	if !ok {
		return false
	}
	a, b := strings.ToLower(field), strings.ToLower(s)
	switch op {
	case "is":
		return a == b
	case "is_not":
		return a != b
	case "contains":
		return strings.Contains(a, b)
	}
	return false
}

func matchStringSlice(field []string, op string, val any) bool {
	s, ok := stringValue(val)
	if !ok {
		return false
	}
	target := strings.ToLower(s)
	switch op {
	case "is":
		for _, v := range field {
			if strings.ToLower(v) == target {
				return true
			}
		}
		return false
	case "is_not":
		for _, v := range field {
			if strings.ToLower(v) == target {
				return false
			}
		}
		return true
	case "contains":
		for _, v := range field {
			if strings.Contains(strings.ToLower(v), target) {
				return true
			}
		}
		return false
	}
	return false
}

func matchInt(field int, op string, val any) bool {
	switch op {
	case "between":
		low, high, ok := pairValue(val)
		if !ok {
			return false
		}
		l, lok := numericValue(low)
		h, hok := numericValue(high)
		if !lok || !hok {
			return false
		}
		f := float64(field)
		return f >= l && f <= h
	}
	n, ok := numericValue(val)
	if !ok {
		return false
	}
	f := float64(field)
	switch op {
	case "is":
		return f == n
	case "is_not":
		return f != n
	case "gt":
		return f > n
	case "gte":
		return f >= n
	case "lt":
		return f < n
	case "lte":
		return f <= n
	}
	return false
}

func matchFloat(field float64, op string, val any) bool {
	switch op {
	case "between":
		low, high, ok := pairValue(val)
		if !ok {
			return false
		}
		l, lok := numericValue(low)
		h, hok := numericValue(high)
		if !lok || !hok {
			return false
		}
		return field >= l && field <= h
	}
	n, ok := numericValue(val)
	if !ok {
		return false
	}
	switch op {
	case "gt":
		return field > n
	case "gte":
		return field >= n
	case "lt":
		return field < n
	case "lte":
		return field <= n
	}
	return false
}

func matchBool(field bool, op string, val any) bool {
	if op != "is" {
		return false
	}
	b, ok := val.(bool)
	if !ok {
		return false
	}
	return field == b
}

func matchTime(field time.Time, op string, val any, now time.Time) bool {
	switch op {
	case "in_last":
		s, ok := stringValue(val)
		if !ok {
			return false
		}
		d, err := parseDurationLoose(s)
		if err != nil {
			return false
		}
		if field.IsZero() {
			return false
		}
		return now.Sub(field) <= d
	case "between":
		low, high, ok := pairValue(val)
		if !ok {
			return false
		}
		lt, lok := timeValue(low)
		ht, hok := timeValue(high)
		if !lok || !hok || field.IsZero() {
			return false
		}
		return !field.Before(lt) && !field.After(ht)
	}
	t, ok := timeValue(val)
	if !ok || field.IsZero() {
		return false
	}
	switch op {
	case "gt":
		return field.After(t)
	case "gte":
		return field.After(t) || field.Equal(t)
	case "lt":
		return field.Before(t)
	case "lte":
		return field.Before(t) || field.Equal(t)
	}
	return false
}

func sortCandidates(out []Candidate, s QuerySort, opts EvaluateOptions) {
	field := s.Field
	if field == "" {
		field = defaultSortField
	}
	desc := s.Order == "desc"
	if field == "random" {
		seed := uint64(0)
		if opts.UserSeed != "" {
			h := fnv.New64a()
			_, _ = h.Write([]byte(opts.UserSeed))
			seed = h.Sum64()
		}
		rng := rand.New(rand.NewSource(int64(seed))) //nolint:gosec
		rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
		return
	}
	sort.SliceStable(out, func(i, j int) bool {
		less := compareCandidates(out[i], out[j], field)
		if desc {
			return !less && !equalCandidates(out[i], out[j], field)
		}
		return less
	})
}

func compareCandidates(a, b Candidate, field string) bool {
	switch field {
	case "title":
		return strings.ToLower(a.Item.Title) < strings.ToLower(b.Item.Title)
	case "added_at":
		return a.Item.AddedAt.Before(b.Item.AddedAt)
	case "year":
		return a.Item.Year < b.Item.Year
	case "duration_seconds":
		return a.Item.DurationSeconds < b.Item.DurationSeconds
	case "rating":
		return a.Item.Rating < b.Item.Rating
	case "progress":
		return a.ProgressPct < b.ProgressPct
	case "last_played":
		return a.LastPlayedAt.Before(b.LastPlayedAt)
	case "plays":
		return a.PlayCount < b.PlayCount
	}
	return false
}

func equalCandidates(a, b Candidate, field string) bool {
	switch field {
	case "title":
		return strings.EqualFold(a.Item.Title, b.Item.Title)
	case "added_at":
		return a.Item.AddedAt.Equal(b.Item.AddedAt)
	case "year":
		return a.Item.Year == b.Item.Year
	case "duration_seconds":
		return a.Item.DurationSeconds == b.Item.DurationSeconds
	case "rating":
		return a.Item.Rating == b.Item.Rating
	case "progress":
		return a.ProgressPct == b.ProgressPct
	case "last_played":
		return a.LastPlayedAt.Equal(b.LastPlayedAt)
	case "plays":
		return a.PlayCount == b.PlayCount
	}
	return false
}

func stringValue(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case int:
		return strconv.Itoa(x), true
	case int64:
		return strconv.FormatInt(x, 10), true
	}
	return "", false
}

func numericValue(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err == nil {
			return f, true
		}
	}
	return 0, false
}

func timeValue(v any) (time.Time, bool) {
	s, ok := stringValue(v)
	if !ok {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0), true
	}
	return time.Time{}, false
}

func pairValue(v any) (any, any, bool) {
	switch x := v.(type) {
	case []any:
		if len(x) == 2 {
			return x[0], x[1], true
		}
	case [2]any:
		return x[0], x[1], true
	}
	return nil, nil, false
}

func parseDurationLoose(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if len(s) >= 2 {
		suffix := s[len(s)-1]
		body := s[:len(s)-1]
		if suffix == 'd' {
			n, err := strconv.Atoi(body)
			if err == nil {
				return time.Duration(n) * 24 * time.Hour, nil
			}
		}
		if suffix == 'w' {
			n, err := strconv.Atoi(body)
			if err == nil {
				return time.Duration(n) * 7 * 24 * time.Hour, nil
			}
		}
	}
	return time.ParseDuration(s)
}
