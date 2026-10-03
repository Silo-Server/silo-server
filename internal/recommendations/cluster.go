package recommendations

import (
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"
)

// clusterItem represents a single item to be clustered, carrying its embedding,
// signal weight, and genre metadata.
type clusterItem struct {
	itemID    string
	embedding []float32
	weight    float64
	genres    []string
}

// kmeansMaxIterations is the maximum number of k-means refinement iterations.
const kmeansMaxIterations = 50

// kmeansConvergenceThreshold is the minimum centroid movement to continue iterating.
const kmeansConvergenceThreshold = 1e-6

// kmeansMinClusterSize is the minimum number of members a cluster must have
// before it gets merged into a neighbor.
const kmeansMinClusterSize = 3

// kmeansCluster partitions items into k groups using k-means with k-means++
// initialization. Centroids are computed as weighted averages using each item's
// signal weight and are L2-normalized after each update. Returns a slice of
// length len(items) mapping each item index to its assigned cluster index.
func kmeansCluster(items []clusterItem, k int) []int {
	n := len(items)
	if n == 0 || k <= 0 {
		return nil
	}
	if k > n {
		k = n
	}

	dims := len(items[0].embedding)
	rng := rand.New(rand.NewSource(kmeansSeed(items, k)))

	// --- k-means++ initialization ---
	// Select the first centroid uniformly at random.
	centroids := make([][]float64, 0, k)
	firstIdx := rng.Intn(n)
	centroids = append(centroids, float32ToFloat64(items[firstIdx].embedding))

	// Select remaining centroids with probability proportional to D(x)^2.
	distSq := make([]float64, n)
	for c := 1; c < k; c++ {
		totalDist := 0.0
		for i := range items {
			minD := math.MaxFloat64
			for _, cent := range centroids {
				d := l2DistSq(items[i].embedding, cent)
				if d < minD {
					minD = d
				}
			}
			distSq[i] = minD
			totalDist += minD
		}

		// Weighted random selection.
		threshold := rng.Float64() * totalDist
		cumulative := 0.0
		chosen := 0
		for i, d := range distSq {
			cumulative += d
			if cumulative >= threshold {
				chosen = i
				break
			}
		}
		centroids = append(centroids, float32ToFloat64(items[chosen].embedding))
	}

	assignments := make([]int, n)

	// --- Main k-means loop ---
	for iter := 0; iter < kmeansMaxIterations; iter++ {
		// Assignment step: assign each item to the nearest centroid.
		for i := range items {
			bestCluster := 0
			bestDist := math.MaxFloat64
			for c := range centroids {
				d := l2DistSq(items[i].embedding, centroids[c])
				if d < bestDist {
					bestDist = d
					bestCluster = c
				}
			}
			assignments[i] = bestCluster
		}

		// Update step: recompute centroids as weighted averages, then L2-normalize.
		newCentroids := make([][]float64, k)
		for c := 0; c < k; c++ {
			newCentroids[c] = make([]float64, dims)
		}

		for i, ci := range assignments {
			w := items[i].weight
			if w <= 0 {
				w = 0.01 // small floor so every item contributes
			}
			for d := 0; d < dims; d++ {
				newCentroids[ci][d] += float64(items[i].embedding[d]) * w
			}
		}

		// L2-normalize each centroid.
		for c := 0; c < k; c++ {
			l2NormalizeFloat64(newCentroids[c])
		}

		// Check convergence: if all centroids moved less than the threshold, stop.
		maxMovement := 0.0
		for c := 0; c < k; c++ {
			movement := 0.0
			for d := 0; d < dims; d++ {
				diff := newCentroids[c][d] - centroids[c][d]
				movement += diff * diff
			}
			movement = math.Sqrt(movement)
			if movement > maxMovement {
				maxMovement = movement
			}
		}

		centroids = newCentroids

		if maxMovement < kmeansConvergenceThreshold {
			break
		}
	}

	// Final assignment pass to ensure consistency with final centroids.
	for i := range items {
		bestCluster := 0
		bestDist := math.MaxFloat64
		for c := range centroids {
			d := l2DistSq(items[i].embedding, centroids[c])
			if d < bestDist {
				bestDist = d
				bestCluster = c
			}
		}
		assignments[i] = bestCluster
	}

	return assignments
}

// kmeansSeed derives the k-means seed from k and the item IDs only. Weights
// decay with time, so seeding from them reshuffled the clusters on every
// rebuild even when the profile's titles had not changed.
func kmeansSeed(items []clusterItem, k int) int64 {
	seed := int64(1469598103934665603)
	seed = seed*1099511628211 + int64(k)
	for _, item := range items {
		for _, b := range []byte(item.itemID) {
			seed ^= int64(b)
			seed *= 1099511628211
		}
		// Separate IDs, so "ab","c" and "a","bc" seed differently.
		seed ^= 0xff
		seed *= 1099511628211
	}
	if seed == 0 {
		return 1
	}
	return seed
}

// determinClusterCount maps the number of items to a suitable cluster count.
func determinClusterCount(itemCount int) int {
	switch {
	case itemCount < 10:
		return 1
	case itemCount < 20:
		return 2
	case itemCount <= 30:
		return 3
	case itemCount < 60:
		return 3
	case itemCount <= 100:
		return 4
	case itemCount < 200:
		return 4
	default:
		return 5
	}
}

// mergSmallClusters reassigns items in clusters with fewer than kmeansMinClusterSize
// members to the nearest non-small cluster. Returns updated assignments with
// cluster indices compacted to be contiguous starting from 0.
func mergSmallClusters(items []clusterItem, assignments []int, k int) []int {
	if len(items) == 0 {
		return assignments
	}

	// Count members per cluster.
	clusterSize := make(map[int]int)
	for _, c := range assignments {
		clusterSize[c]++
	}

	// Compute centroid of each cluster (weighted).
	dims := len(items[0].embedding)
	centroids := make(map[int][]float64)
	for c := range clusterSize {
		centroids[c] = make([]float64, dims)
	}
	for i, c := range assignments {
		w := items[i].weight
		if w <= 0 {
			w = 0.01
		}
		for d := 0; d < dims; d++ {
			centroids[c][d] += float64(items[i].embedding[d]) * w
		}
	}
	for c := range centroids {
		l2NormalizeFloat64(centroids[c])
	}

	// Identify small clusters and valid (non-small) clusters.
	smallClusters := make(map[int]bool)
	for c, size := range clusterSize {
		if size < kmeansMinClusterSize {
			smallClusters[c] = true
		}
	}

	// If all clusters are small, skip merging to avoid losing all data.
	nonSmallCount := 0
	for c := range clusterSize {
		if !smallClusters[c] {
			nonSmallCount++
		}
	}
	if nonSmallCount == 0 {
		return compactAssignments(assignments)
	}

	// Reassign items from small clusters to the nearest non-small cluster.
	for i, c := range assignments {
		if !smallClusters[c] {
			continue
		}
		bestCluster := -1
		bestDist := math.MaxFloat64
		for target, cent := range centroids {
			if smallClusters[target] {
				continue
			}
			d := l2DistSq(items[i].embedding, cent)
			if d < bestDist {
				bestDist = d
				bestCluster = target
			}
		}
		if bestCluster >= 0 {
			assignments[i] = bestCluster
		}
	}

	return compactAssignments(assignments)
}

// compactAssignments remaps cluster indices to be contiguous starting from 0.
func compactAssignments(assignments []int) []int {
	seen := make(map[int]int)
	next := 0
	result := make([]int, len(assignments))
	for i, c := range assignments {
		if _, ok := seen[c]; !ok {
			seen[c] = next
			next++
		}
		result[i] = seen[c]
	}
	return result
}

// buildTasteClusters is the main entry point for computing taste sub-profiles.
// It determines the appropriate cluster count, runs weighted k-means clustering,
// merges undersized clusters, and returns labeled TasteCluster values with
// computed embeddings, dominant genres, and aggregate statistics.
func buildTasteClusters(items []clusterItem) []TasteCluster {
	if len(items) == 0 {
		return nil
	}

	// Filter out items without embeddings.
	valid := make([]clusterItem, 0, len(items))
	for _, item := range items {
		if len(item.embedding) > 0 {
			valid = append(valid, item)
		}
	}
	if len(valid) == 0 {
		return nil
	}

	// Step 1: Determine how many clusters to target.
	k := determinClusterCount(len(valid))

	// Step 2: Run k-means clustering.
	assignments := kmeansCluster(valid, k)

	// Step 3: Merge clusters that are too small.
	assignments = mergSmallClusters(valid, assignments, k)

	// Step 4: Build the output TasteCluster for each cluster.
	clusterMap := make(map[int][]int) // cluster index -> item indices
	for i, c := range assignments {
		clusterMap[c] = append(clusterMap[c], i)
	}

	// Collect cluster indices and sort for deterministic output order.
	clusterIndices := make([]int, 0, len(clusterMap))
	for c := range clusterMap {
		clusterIndices = append(clusterIndices, c)
	}
	sort.Ints(clusterIndices)

	clusters := make([]TasteCluster, 0, len(clusterIndices))
	for _, ci := range clusterIndices {
		memberIndices := clusterMap[ci]

		// Gather embeddings and weights for the weighted average.
		vecs := make([][]float32, len(memberIndices))
		weights := make([]float64, len(memberIndices))
		genreCounts := make(map[string]int)
		totalWeight := 0.0

		for j, idx := range memberIndices {
			vecs[j] = valid[idx].embedding
			weights[j] = valid[idx].weight
			totalWeight += valid[idx].weight
			for _, g := range valid[idx].genres {
				genreCounts[g]++
			}
		}

		// Compute L2-normalized weighted average embedding.
		embedding := weightedAverage(vecs, weights)

		// Determine dominant genres (top 3 by frequency).
		dominantGenres := topNGenres(genreCounts, 3)

		// Build a human-readable label from the dominant genres.
		label := buildClusterLabel(dominantGenres)

		clusters = append(clusters, TasteCluster{
			ClusterIdx:     ci,
			Embedding:      embedding,
			DominantGenres: dominantGenres,
			Label:          label,
			MemberCount:    len(memberIndices),
			TotalWeight:    totalWeight,
			UpdatedAt:      time.Now(),
		})
	}

	return clusters
}

// topNGenres returns up to n genre names sorted by descending frequency.
func topNGenres(counts map[string]int, n int) []string {
	type genreCount struct {
		genre string
		count int
	}

	pairs := make([]genreCount, 0, len(counts))
	for g, c := range counts {
		pairs = append(pairs, genreCount{g, c})
	}

	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count != pairs[j].count {
			return pairs[i].count > pairs[j].count
		}
		return pairs[i].genre < pairs[j].genre // stable tie-breaking
	})

	end := n
	if end > len(pairs) {
		end = len(pairs)
	}

	result := make([]string, end)
	for i := 0; i < end; i++ {
		result[i] = pairs[i].genre
	}
	return result
}

// buildClusterLabel creates a display label by joining genre names with " & ".
// Returns "Mixed" if no genres are available.
func buildClusterLabel(genres []string) string {
	if len(genres) == 0 {
		return "Mixed"
	}
	return strings.Join(genres, " & ")
}

// --- Vector math helpers ---

// float32ToFloat64 converts a float32 slice to float64.
func float32ToFloat64(v []float32) []float64 {
	out := make([]float64, len(v))
	for i, val := range v {
		out[i] = float64(val)
	}
	return out
}

// l2DistSq computes the squared L2 distance between a float32 vector and a
// float64 centroid. If either vector is nil, returns 0.
func l2DistSq(a []float32, b []float64) float64 {
	if a == nil || b == nil {
		return 0
	}
	sum := 0.0
	for i := range a {
		diff := float64(a[i]) - b[i]
		sum += diff * diff
	}
	return sum
}

// l2NormalizeFloat64 normalizes a float64 vector in-place to unit length.
// If the vector has zero magnitude, it is left unchanged.
func l2NormalizeFloat64(v []float64) {
	var norm float64
	for _, val := range v {
		norm += val * val
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		return
	}
	for i := range v {
		v[i] /= norm
	}
}
