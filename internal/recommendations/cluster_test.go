package recommendations

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// interestTitles returns n titles of interest, all carrying genre: unit
// vectors that share a common direction, their interest's own direction and
// per-title noise, so titles of one interest score a cosine near 0.75 and
// titles of different interests near 0.45, the way real title embeddings sit
// closer within a genre than across genres.
func interestTitles(rng *rand.Rand, interest, n int, genre string) []clusterItem {
	const interests, noiseDims = 8, 32
	items := make([]clusterItem, n)
	for i := range items {
		vec := make([]float32, 1+interests+noiseDims)
		vec[0] = 1
		vec[1+interest] = 0.8
		var noiseNorm float64
		noise := make([]float64, noiseDims)
		for d := range noise {
			noise[d] = rng.NormFloat64()
			noiseNorm += noise[d] * noise[d]
		}
		for d := range noise {
			vec[1+interests+d] = float32(0.7 * noise[d] / math.Sqrt(noiseNorm))
		}
		var norm float64
		for _, v := range vec {
			norm += float64(v) * float64(v)
		}
		for d := range vec {
			vec[d] /= float32(math.Sqrt(norm))
		}
		items[i] = clusterTestItem(fmt.Sprintf("%s-%02d", genre, i), vec, 0.8, genre)
	}
	return items
}

// clusterGenres returns, per cluster of assignments, the sorted genres of its
// members, one entry per member.
func clusterGenres(items []clusterItem, assignments []int) [][]string {
	var out [][]string
	for i, c := range assignments {
		for len(out) <= c {
			out = append(out, nil)
		}
		out[c] = append(out[c], items[i].genres[0])
	}
	for _, g := range out {
		slices.Sort(g)
	}
	slices.SortFunc(out, func(a, b []string) int { return strings.Compare(strings.Join(a, ","), strings.Join(b, ",")) })
	return out
}

// Profiles of two or three interests of 3 to 9 titles, with 10 to 19 titles
// in all, get exactly their interests as clusters, whichever seeding k-means
// starts from.
func TestClusterAssignmentsRecoversInterests(t *testing.T) {
	rng := rand.New(rand.NewPCG(20261004, 1))
	checked := 0
	for range 200 {
		var items []clusterItem
		var want [][]string
		for interest := range 2 + rng.IntN(2) {
			genre := fmt.Sprintf("g%d", interest)
			titles := interestTitles(rng, interest, 3+rng.IntN(7), genre)
			items = append(items, titles...)
			group := make([]string, len(titles))
			for i := range group {
				group[i] = genre
			}
			want = append(want, group)
		}
		if n := len(items); n < 10 || n >= 20 {
			continue
		}
		checked++
		slices.SortFunc(want, func(a, b []string) int { return strings.Compare(strings.Join(a, ","), strings.Join(b, ",")) })
		got := clusterGenres(items, clusterAssignments(items))
		if !slices.EqualFunc(got, want, slices.Equal) {
			t.Fatalf("profile of %d titles: clusters %v, want %v", len(items), got, want)
		}
	}
	if checked < 50 {
		t.Fatalf("checked %d profiles, want at least 50", checked)
	}
}
