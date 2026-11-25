package handlers

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// profileSetPlan is how a set lands on a household: which profile each item
// becomes, which items are created and which are left off for the limit,
// and which profiles the set removes or leaves alone.
type profileSetPlan struct {
	// rowOf maps an item's index to the row it updates; an item not in it
	// is created or left off.
	rowOf map[int]*userstore.ProfileSetRow
	// create and leftOff are item indexes, in set order.
	create  []int
	leftOff []int
	// removed is the rows the set removes, in ID order.
	removed []*userstore.ProfileSetRow
	// localOnly is the rows outside the set, in creation order.
	localOnly []*userstore.ProfileSetRow
}

// ErrProfileSetNoPrimary is a household with profiles and no primary, which
// the set cannot place its first item on: stored data a set can't apply to.
var ErrProfileSetNoPrimary = errors.New("the household has profiles but no primary profile")

// planProfileSet maps the set onto rows, pure. The first item is always the
// primary profile; the rest match a row by sync ID, then by pairs, and are
// otherwise new. Rows holding a sync ID the set no longer has are removed,
// the primary never. New items are created in set order while the household
// is under limit (0 = none); the rest are left off, and nothing is deleted
// to fit. Names are checked on the household as the set leaves it.
func planProfileSet(doc *profileSetDoc, rows []userstore.ProfileSetRow, limit int) (*profileSetPlan, error) {
	plan := &profileSetPlan{rowOf: map[int]*userstore.ProfileSetRow{}}
	bySyncID := map[string]*userstore.ProfileSetRow{}
	byID := map[string]*userstore.ProfileSetRow{}
	var primary *userstore.ProfileSetRow
	for i := range rows {
		row := &rows[i]
		byID[row.ID] = row
		if row.SyncID != "" {
			bySyncID[row.SyncID] = row
		}
		if row.IsPrimary {
			primary = row
		}
	}
	if primary == nil && len(rows) > 0 {
		return nil, ErrProfileSetNoPrimary
	}
	pairOf := map[string]int{}
	for j, pair := range doc.Pairs {
		pairOf[pair.SyncID] = j
	}
	inSet := map[string]bool{}
	for _, item := range doc.Items {
		inSet[item.SyncID] = true
	}
	claimed := map[string]bool{}
	for i, item := range doc.Items {
		row, err := planProfileSetRow(doc, i, item, primary, bySyncID, byID, pairOf)
		if err != nil {
			return nil, err
		}
		if row != nil {
			plan.rowOf[i] = row
			claimed[row.ID] = true
		}
	}
	for i := range rows {
		row := &rows[i]
		switch {
		case claimed[row.ID]:
		case row.SyncID != "" && !inSet[row.SyncID] && !row.IsPrimary:
			plan.removed = append(plan.removed, row)
		default:
			plan.localOnly = append(plan.localOnly, row)
		}
	}
	slices.SortFunc(plan.removed, func(a, b *userstore.ProfileSetRow) int { return strings.Compare(a.ID, b.ID) })
	occupied := len(plan.localOnly) + len(plan.rowOf)
	for i := range doc.Items {
		if _, mapped := plan.rowOf[i]; mapped {
			continue
		}
		if limit < 1 || occupied < limit || (i == 0 && primary == nil) {
			plan.create = append(plan.create, i)
			occupied++
		} else {
			plan.leftOff = append(plan.leftOff, i)
		}
	}
	return plan, checkProfileSetNames(doc, plan)
}

// planProfileSetRow finds the row item i updates, nil for a new item.
func planProfileSetRow(
	doc *profileSetDoc, i int, item profileSetItem, primary *userstore.ProfileSetRow,
	bySyncID, byID map[string]*userstore.ProfileSetRow, pairOf map[string]int,
) (*userstore.ProfileSetRow, error) {
	syncAt := fmt.Sprintf("set.items[%d].sync_id", i)
	holder := bySyncID[item.SyncID]
	j, paired := pairOf[item.SyncID]
	pairAt := fmt.Sprintf("pairs[%d].local_id", j)
	if i == 0 {
		if holder != nil && !holder.IsPrimary {
			return nil, profileSetFieldError(syncAt, "The first item is the main profile, and another profile here holds its sync_id.")
		}
		if paired && (primary == nil || doc.Pairs[j].LocalID != primary.ID) {
			return nil, profileSetFieldError(pairAt, "The first item always pairs with the main profile.")
		}
		return primary, nil
	}
	if holder != nil && holder.IsPrimary {
		return nil, profileSetFieldError(syncAt, "This sync_id is the main profile's; the main profile is the first item.")
	}
	if paired {
		local := byID[doc.Pairs[j].LocalID]
		switch {
		case local == nil:
			return nil, profileSetFieldError(pairAt, "No profile on this account has this ID.")
		case local.IsPrimary:
			return nil, profileSetFieldError(pairAt, "The main profile pairs only with the first item.")
		case holder != nil && holder != local:
			return nil, profileSetFieldError(pairAt, "Another profile here already holds this sync_id.")
		case holder == nil && local.SyncID != "":
			return nil, profileSetFieldError(pairAt, "This profile is already part of the set.")
		}
		return local, nil
	}
	return holder, nil
}

// checkProfileSetNames refuses a set that would leave one name on two
// profiles: an item named like a profile outside the set. Names inside the
// set are unique already, so renames among set profiles, a chain included,
// never collide. Duplicates among the profiles outside the set are not the
// set's to judge.
func checkProfileSetNames(doc *profileSetDoc, plan *profileSetPlan) error {
	held := make([]int, 0, len(plan.rowOf)+len(plan.create))
	for i := range plan.rowOf {
		held = append(held, i)
	}
	held = append(held, plan.create...)
	slices.Sort(held)
	for _, i := range held {
		for _, local := range plan.localOnly {
			if sameProfileName(local.Name, doc.Items[i].Name) {
				return &ProfileSetFieldError{
					Location: fmt.Sprintf("set.items[%d].name", i),
					Code:     "name_conflict",
					Message:  "A profile on this server that is not part of the set already has this name; pair it or rename one.",
				}
			}
		}
	}
	return nil
}
