package recommendations

import (
	"math"
	"math/rand"
	"slices"
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

// kmeansMinClusterSize is the fewest titles a taste cluster holds; a smaller
// one is merged into a neighbor.
const kmeansMinClusterSize = 3

// kmeansMaxClusters is the most taste clusters a profile gets.
const kmeansMaxClusters = 5

// A single k-means++ seeding can settle on a poor partition: two seeds in one
// interest leave a sliver cluster that merging then folds away, and with it
// every other interest. Each cluster count is tried with up to kmeansRestarts
// seedings, as many as kmeansRestartBudget titles allow, so a profile of 200
// titles costs about what one of 1,500 always did, and one of 800 or more
// runs once.
const (
	kmeansRestarts      = 8
	kmeansRestartBudget = 800
)

// kmeansMinSilhouette is the silhouette a partition into more clusters than
// determinClusterCount gives must reach to be chosen. Distinct interests in
// real profiles score 0.13 to 0.22; one interest cut into parts scores under
// 0.08, where the best count changes with any new title.
const kmeansMinSilhouette = 0.1

// kmeansCluster partitions items into k groups using k-means with k-means++
// initialization, seeded by kmeansSeed. Returns a slice of length len(items)
// mapping each item index to its assigned cluster index.
func kmeansCluster(items []clusterItem, k int) []int {
	assignments, _ := kmeansRun(items, k, kmeansSeed(items, k))
	return assignments
}

// kmeansRestartSeed is the seed of restart r of k-means over items; restart 0
// uses kmeansSeed itself.
func kmeansRestartSeed(items []clusterItem, k, r int) int64 {
	return kmeansSeed(items, k) ^ int64(r)*0x5851f42d4c957f2d
}

// kmeansRun partitions items into k groups using k-means with k-means++
// initialization from seed. Centroids are computed as weighted averages using
// each item's signal weight and are L2-normalized after each update. Returns a
// slice of length len(items) mapping each item index to its assigned cluster
// index, and the partition's inertia: the sum of the items' squared distances
// to their centroids. It leaves weights out, so a choice between partitions
// by inertia does not change as the weights decay.
func kmeansRun(items []clusterItem, k int, seed int64) ([]int, float64) {
	n := len(items)
	if n == 0 || k <= 0 {
		return nil, 0
	}
	if k > n {
		k = n
	}

	dims := len(items[0].embedding)
	rng := rand.New(rand.NewSource(seed))

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
	inertia := 0.0
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
		inertia += bestDist
	}

	return assignments, inertia
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

// clusterAssignments partitions items into taste clusters. A profile under 10
// titles has one. A larger one gets determinClusterCount clusters, or more,
// up to kmeansMaxClusters, when a partition into more scores a higher
// silhouette and at least kmeansMinSilhouette. The silhouette compares each
// title's distance to its own cluster with its distance to the nearest other,
// so it judges interests against the profile's own spread: a third interest
// of three titles gets a cluster of its own, while one interest is not cut up
// for the sake of a count. Each count's partition is its best seeding (see
// bestSeeding). When determinClusterCount's count has none, and no larger
// count qualifies, that count is run once and its small clusters merged.
func clusterAssignments(items []clusterItem) []int {
	n := len(items)
	minK := determinClusterCount(n)
	if minK <= 1 {
		return make([]int, n)
	}
	maxK := max(minK, min(kmeansMaxClusters, n/kmeansMinClusterSize))
	restarts := max(1, min(kmeansRestarts, kmeansRestartBudget/n))

	var best []int
	bestScore := math.Inf(-1)
	var distances [][]float64
	for k := minK; k <= maxK; k++ {
		assignments := bestSeeding(items, k, restarts)
		if assignments == nil {
			continue
		}
		if minK == maxK {
			return compactAssignments(assignments)
		}
		if distances == nil {
			distances = cosineDistances(items)
		}
		score := silhouette(distances, assignments)
		if k > minK && score < kmeansMinSilhouette {
			continue
		}
		if score > bestScore {
			best, bestScore = assignments, score
		}
	}
	if best == nil {
		return mergSmallClusters(items, kmeansCluster(items, minK), minK)
	}
	return compactAssignments(best)
}

// bestSeeding partitions items into k clusters with restarts k-means++
// seedings and returns the partition with the lowest inertia whose clusters
// all hold kmeansMinClusterSize items, or nil when no seeding gives one.
func bestSeeding(items []clusterItem, k, restarts int) []int {
	var best []int
	bestInertia := math.Inf(1)
	for r := range restarts {
		assignments, inertia := kmeansRun(items, k, kmeansRestartSeed(items, k, r))
		if inertia < bestInertia && clustersHoldMinimum(assignments, k) {
			best, bestInertia = assignments, inertia
		}
	}
	return best
}

// clustersHoldMinimum reports whether each of the k clusters of assignments
// holds at least kmeansMinClusterSize items.
func clustersHoldMinimum(assignments []int, k int) bool {
	sizes := make([]int, k)
	for _, c := range assignments {
		sizes[c]++
	}
	return !slices.ContainsFunc(sizes, func(size int) bool { return size < kmeansMinClusterSize })
}

// cosineDistances returns the cosine distance, 1 - cosine similarity, between
// every pair of items.
func cosineDistances(items []clusterItem) [][]float64 {
	d := make([][]float64, len(items))
	for i := range d {
		d[i] = make([]float64, len(items))
	}
	for i := range items {
		for j := i + 1; j < len(items); j++ {
			dist := 1 - cosineSimilarity(items[i].embedding, items[j].embedding)
			d[i][j], d[j][i] = dist, dist
		}
	}
	return d
}

// silhouette is the mean silhouette of a partition, from -1 to 1: for each
// item, how much nearer it is, on average, to its own cluster's other members
// than to the members of the nearest other cluster.
func silhouette(distances [][]float64, assignments []int) float64 {
	k := slices.Max(assignments) + 1
	sizes := make([]int, k)
	for _, c := range assignments {
		sizes[c]++
	}
	total := 0.0
	for i, own := range assignments {
		if sizes[own] < 2 {
			continue // a lone member scores 0
		}
		sums := make([]float64, k)
		for j, c := range assignments {
			sums[c] += distances[i][j]
		}
		within := sums[own] / float64(sizes[own]-1)
		nearest := math.Inf(1)
		for c := range k {
			if c != own && sizes[c] > 0 {
				nearest = min(nearest, sums[c]/float64(sizes[c]))
			}
		}
		if spread := max(within, nearest); spread > 0 {
			total += (nearest - within) / spread
		}
	}
	return total / float64(len(assignments))
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

	// Steps 1-3: Choose the cluster count and partition the items.
	assignments := clusterAssignments(valid)

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

	// The profile's own genre mix is the baseline a cluster's label is
	// measured against.
	profileGenres := make(map[string]int)
	for _, item := range valid {
		for _, g := range distinctGenres(item.genres) {
			profileGenres[g]++
		}
	}

	clusters := make([]TasteCluster, 0, len(clusterIndices))
	for _, ci := range clusterIndices {
		memberIndices := clusterMap[ci]

		// Gather embeddings and weights for the weighted average.
		vecs := make([][]float32, len(memberIndices))
		weights := make([]float64, len(memberIndices))
		genreCounts := make(map[string]int)
		members := make([]clusterItem, len(memberIndices))
		totalWeight := 0.0

		for j, idx := range memberIndices {
			members[j] = valid[idx]
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

		label := clusterLabel(members, profileGenres, len(valid))

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

// A cluster's label names what sets it apart from the profile's other
// interests, not merely what is common: Drama runs through most taste
// clusters, so the most frequent genres would name nearly every cluster the
// same. The thresholds were tuned on synthetic profiles.
const (
	// labelMinShare is the fewest of a cluster's members, as a share, that
	// must carry a distinctive genre.
	labelMinShare = 0.40
	// labelMinLift is how many times more often the cluster's members must
	// carry a distinctive genre than the profile's titles do.
	labelMinLift = 1.5
	// labelMaxGenres is the most genres a label names.
	labelMaxGenres = 2
	// clusterLabelSeparator joins a label's genres. It is not " & ", which
	// TMDB genres such as "Sci-Fi & Fantasy" already contain.
	clusterLabelSeparator = ", "
)

// clusterLabel labels the cluster of members against a profile whose
// profileTitles titles carry each genre profileGenres times. It names the
// cluster's distinctive genres, those at least labelMinShare of the members
// carry and carry at least labelMinLift times as often as the profile's
// titles do, the most distinctive first, and always the cluster's most common
// genre, which its row's titles mostly carry, in at most labelMaxGenres
// genres. A cluster with no distinctive genre, such as a profile's only
// cluster, is labeled by its most common genre alone, and one whose members
// carry no genres gets "".
func clusterLabel(members []clusterItem, profileGenres map[string]int, profileTitles int) string {
	counts := make(map[string]int)
	weights := make(map[string]float64)
	for _, m := range members {
		for _, g := range distinctGenres(m.genres) {
			counts[g]++
			weights[g] += m.weight
		}
	}
	if len(counts) == 0 {
		return ""
	}
	// The most common genre; a tie goes to the genre of the heavier titles,
	// which pull the cluster's centroid, and its row, toward them.
	moreCommon := func(g, than string) bool {
		switch {
		case counts[g] != counts[than]:
			return counts[g] > counts[than]
		case weights[g] != weights[than]:
			return weights[g] > weights[than]
		default:
			return g < than
		}
	}
	common := ""
	for g := range counts {
		if common == "" || moreCommon(g, common) {
			common = g
		}
	}

	type candidate struct {
		genre       string
		share, lift float64
	}
	var distinctive []candidate
	for g, n := range counts {
		share := float64(n) / float64(len(members))
		base := float64(profileGenres[g]) / float64(max(profileTitles, 1))
		if share < labelMinShare || base <= 0 {
			continue
		}
		if lift := share / base; lift >= labelMinLift {
			distinctive = append(distinctive, candidate{g, share, lift})
		}
	}
	sort.Slice(distinctive, func(i, j int) bool {
		a, b := distinctive[i], distinctive[j]
		if a.lift != b.lift {
			return a.lift > b.lift
		}
		if a.share != b.share {
			return a.share > b.share
		}
		return a.genre < b.genre
	})

	// The most common genre and the most distinctive others, in order of
	// distinction; the most common genre goes last when it is not
	// distinctive.
	genres := make([]string, 0, labelMaxGenres)
	others := 0
	for _, c := range distinctive {
		switch {
		case c.genre == common:
			genres = append(genres, c.genre)
		case others < labelMaxGenres-1:
			genres = append(genres, c.genre)
			others++
		}
	}
	if !slices.Contains(genres, common) {
		genres = append(genres, common)
	}
	return strings.Join(genres, clusterLabelSeparator)
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
