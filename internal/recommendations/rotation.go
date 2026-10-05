package recommendations

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"time"
)

// A profile's taste vector barely moves while it is idle, so without
// rotation its personal rows serve the same titles every day. Reads rotate
// the main row and the cluster rows once a day: the best-matched head stays,
// and the rest of the served window is drawn from the next-ranked titles,
// favoring the higher ranks. The draw is a pure function of the profile, the
// row, the server's local date and the items, so every node serves the same
// rotation without shared state, as long as the nodes share a time zone.
const (
	// rotationMaxPinned is the most leading items a rotation leaves in
	// place.
	rotationMaxPinned = 10
	// rotationRankOffset softens how strongly the draw favors the higher
	// ranks: the item at tail index i is drawn with weight 1/(i+offset).
	rotationRankOffset = 5
)

// rotationSeedKey is the seed rotateTail draws a personal row with: the
// profile and the row's cache key, so a title shared by two rows is drawn
// independently in each.
func rotationSeedKey(userID int, profileID, rowKey string) string {
	return fmt.Sprintf("%d|%s|%s", userID, profileID, rowKey)
}

// rotateTail returns items with the window a read serves, its first limit
// items, rotated for date's day. It keeps the first pin = min(10, limit/2)
// items in place and draws the other take = limit-pin from the next 2×take
// items, without replacement, the item at tail index i with weight
// 1/(i+rotationRankOffset). The draws follow the leading items in rank order,
// and the items not drawn follow them in rank order, so no item is lost and
// a caller that trims to limit serves the rotated window. Items no longer
// than limit, or a limit of at most rotationMaxPinned, come back unchanged.
// items is never modified.
//
// The draw ranks each tail item by ln(u)·(i+rotationRankOffset), the log of
// the weighted-sampling key u^(1/w), with u taken from
// sha256(seedKey|date|itemID); the log keeps tiny values of u apart.
func rotateTail(items []ScoredItem, limit int, seedKey string, date time.Time) []ScoredItem {
	if len(items) <= limit || limit <= rotationMaxPinned {
		return items
	}
	pin := min(rotationMaxPinned, limit/2)
	take := limit - pin
	end := min(rotationReach(limit), len(items))
	day := date.Format(time.DateOnly)

	keys := make([]float64, end-pin)
	order := make([]int, end-pin)
	for i := range order {
		order[i] = i
		keys[i] = math.Log(rotationUniform(seedKey, day, items[pin+i].MediaItemID)) * float64(i+rotationRankOffset)
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(keys[b], keys[a]) })
	drawn := make([]bool, end-pin)
	for _, i := range order[:take] {
		drawn[i] = true
	}

	out := make([]ScoredItem, 0, len(items))
	out = append(out, items[:pin]...)
	for _, keep := range []bool{true, false} {
		for i, item := range items[pin:end] {
			if drawn[i] == keep {
				out = append(out, item)
			}
		}
	}
	return append(out, items[end:]...)
}

// rotationReach is how far into a row rotateTail draws a window of limit
// items from: every item it can serve is among the row's first
// rotationReach(limit).
func rotationReach(limit int) int {
	if limit <= rotationMaxPinned {
		return limit
	}
	pin := min(rotationMaxPinned, limit/2)
	return pin + 2*(limit-pin)
}

// rotationUniform maps the seed, day and item to a number in (0, 1), from
// the first 8 bytes of their SHA-256.
func rotationUniform(seedKey, day, itemID string) float64 {
	sum := sha256.Sum256([]byte(seedKey + "|" + day + "|" + itemID))
	bits := binary.BigEndian.Uint64(sum[:8]) >> 11
	return (float64(bits) + 0.5) / (1 << 53)
}

// rotatePersonalRows rotates the read's personal rows, the main row and the
// cluster rows, for a window of served items and the server's current day.
// Every other row keeps its order.
func (rr *rowRead) rotatePersonalRows(rows []ForYouRow, served int) []ForYouRow {
	date := rr.reader.today()
	for i := range rows {
		if key := rows[i].personalKey; key != "" {
			rows[i].Items = rotateTail(rows[i].Items, served, rotationSeedKey(rr.userID, rr.profileID, key), date)
		}
	}
	return rows
}
