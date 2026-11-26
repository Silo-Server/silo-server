package handlers

import (
	"errors"
	"fmt"
	"maps"
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
	// create, leftOff and hidden are item indexes, in set order. A hidden
	// item names a profile the owner removed on this server: it is neither
	// created nor paired, and does not count against the limit.
	create  []int
	leftOff []int
	hidden  []int
	// removed is the rows the set removes, in ID order.
	removed []*userstore.ProfileSetRow
	// localOnly is the rows outside the set, in creation order.
	localOnly []*userstore.ProfileSetRow
	// refusal is why the set can't be applied to this household as it
	// stands (a name clash, the main profile taking a sync ID the owner
	// removed), nil when it can. It counts only for a set that will write:
	// the caller checks it after the version and edits checks.
	refusal error
}

// ErrProfileSetNoPrimary is a household with profiles and no primary, which
// the set cannot place its first item on: stored data a set can't apply to.
var ErrProfileSetNoPrimary = errors.New("the household has profiles but no primary profile")

// checkProfileSetItemNames refuses a set two of whose items have one name,
// judged on the names the set gives, over all its items: an item left off
// for the limit or hidden by the owner counts, and so does one whose
// profile keeps a name the owner gave it here, since clearing that name
// would bring the clash back with nobody sending anything. The later item is
// the one refused.
func checkProfileSetItemNames(doc *profileSetDoc) error {
	for i, item := range doc.Items {
		for _, earlier := range doc.Items[:i] {
			if sameProfileName(earlier.Name, item.Name) {
				return &ProfileSetFieldError{Location: fmt.Sprintf("set.items[%d].name", i), Code: "name_conflict",
					Message: "Two items of the set have this name; rename one."}
			}
		}
	}
	return nil
}

// planProfileSet maps the set onto rows, pure. The first item is always the
// primary profile; the rest match a row by sync ID, then by pairs, and are
// otherwise new. Rows holding a sync ID the set no longer has are removed,
// the primary never. New items are created in set order while the household
// is under limit (0 = none); the rest are left off, and nothing is deleted
// to fit. Names are judged on the household as the set leaves it, names
// the owner set here kept, and a clash goes in plan.refusal rather than
// failing the plan. Of the refusals, two items of the set named alike
// comes first, being about the set alone; then the main profile taking a
// sync ID the owner removed; then a clash with the household. An item after the first whose sync ID the owner
// removed here, and that no profile here holds, is hidden: its pair, if
// any, is ignored.
func planProfileSet(doc *profileSetDoc, rows []userstore.ProfileSetRow, limit int, edits profileSetEdits) (*profileSetPlan, error) {
	hidden := edits.hidden
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
	isHidden := func(i int) bool { return i > 0 && hidden[doc.Items[i].SyncID] && bySyncID[doc.Items[i].SyncID] == nil }
	// Two items of one name are refused first: that is about the set alone.
	plan.refusal = checkProfileSetItemNames(doc)
	// The main profile can't take a sync ID the owner removed here: the
	// removal would stop hiding it, and a later set could bring it back.
	if first := doc.Items[0].SyncID; plan.refusal == nil && hidden[first] && bySyncID[first] == nil {
		plan.refusal = profileSetFieldError("set.items[0].sync_id", "The owner of this server removed the profile with this sync_id here; the main profile can't take it.")
	}
	for i, item := range doc.Items {
		if isHidden(i) {
			plan.hidden = append(plan.hidden, i)
			continue
		}
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
		if _, mapped := plan.rowOf[i]; mapped || isHidden(i) {
			continue
		}
		if limit < 1 || occupied < limit || (i == 0 && primary == nil) {
			plan.create = append(plan.create, i)
			occupied++
		} else {
			plan.leftOff = append(plan.leftOff, i)
		}
	}
	if plan.refusal == nil {
		plan.refusal = checkProfileSetNames(doc, plan, edits.forItems(doc, plan).finalNames(doc, plan))
	}
	return plan, nil
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
// profiles, judged on final, the names the set's profiles will have (each
// item's name, or the name the owner gave its profile here): a profile of
// the set named like a profile outside it, or two profiles of the set named
// alike, which only an owner's rename can cause, since a set whose items'
// own names clash was refused before this (checkProfileSetItemNames). Of two items that clash, the one refused at its name is the one
// whose name comes from the set, since renaming the item the owner renamed
// changes nothing; the later one when both do. Duplicates among the profiles
// outside the set are not the set's to judge.
func checkProfileSetNames(doc *profileSetDoc, plan *profileSetPlan, final []string) error {
	held := slices.Concat(slices.Collect(maps.Keys(plan.rowOf)), plan.create)
	slices.Sort(held)
	clash := func(i int, message string) error {
		return &ProfileSetFieldError{Location: fmt.Sprintf("set.items[%d].name", i), Code: "name_conflict", Message: message}
	}
	fromSet := func(i int) bool { return sameProfileName(final[i], doc.Items[i].Name) }
	for n, i := range held {
		for _, local := range plan.localOnly {
			if sameProfileName(local.Name, final[i]) {
				return clash(i, "A profile on this server that is not part of the set already has this name; pair it or rename one.")
			}
		}
		for _, j := range held[:n] {
			if sameProfileName(final[i], final[j]) {
				at := i
				if !fromSet(i) && fromSet(j) {
					at = j
				}
				return clash(at, "The owner of this server gave another profile in the set this name; rename this one.")
			}
		}
	}
	return nil
}
