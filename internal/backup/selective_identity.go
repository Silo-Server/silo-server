package backup

// Matching a backup's sections and collections one to one with this
// server's: a title is an identity only when no other entry of the backup
// shares it, and no two items of one restore write the same thing here.

import (
	"context"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// Kinds of thing a restore item claims here.
const (
	claimSection    = "section"
	claimCollection = "collection"
)

// claimedByOther reports whether another item of this restore already wrote
// the local section or collection id.
func (p *planner) claimedByOther(kind, localID, itemID string) bool {
	owner, ok := p.claimed[kind+":"+localID]
	return ok && owner != itemID
}

// claim records that itemID wrote the local id, so no other item of the
// restore matches it.
func (a *applier) claim(kind, localID, itemID string) {
	if a.claimed == nil {
		a.claimed = map[string]string{}
	}
	a.claimed[kind+":"+localID] = itemID
}

// planner returns a planner over q that knows what this restore claimed.
func (a *applier) planner(q queryer) *planner {
	p := a.s.newPlanner(a.rs, q)
	p.claimed = a.claimed
	return p
}

// mappedLibrary is a backup library id's library here, or nil.
func mappedLibrary(id *int, libMap map[int]int) *int {
	if id == nil {
		return nil
	}
	if local, ok := libMap[*id]; ok {
		return &local
	}
	return nil
}

func sameLibrary(a, b *int) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// sectionTwins counts the backup's sections that would land on the same page
// as e with its type and title, e included.
func (p *planner) sectionTwins(e HomeSectionEntry, library *int, libMap map[int]int) int {
	n := 0
	for _, other := range p.rs.HomeSections.Sections {
		if other.Scope == e.Scope && other.SectionType == e.SectionType && other.Title == e.Title &&
			sameLibrary(mappedLibrary(other.LibraryID, libMap), library) {
			n++
		}
	}
	return n
}

// isBackupSection reports whether id is the id of one of the backup's
// sections other than e: a section here with it is that entry's.
func (p *planner) isBackupSection(id string, e HomeSectionEntry) bool {
	for _, other := range p.rs.HomeSections.Sections {
		if other.ID == id && other.ID != e.ID {
			return true
		}
	}
	return false
}

// matchSection finds the section here. The section with the backup's id is
// it: on the server that wrote the backup any on the same page, on another
// one a restore created (same page and type). Otherwise it is the one
// section of the page with the type and title that is not another backup
// section's and that no other item of this restore wrote, provided no
// other section of the backup has that type and title on the page. ambiguous
// says why it cannot choose.
func (p *planner) matchSection(ctx context.Context, e HomeSectionEntry, library *int, libMap map[int]int, sameServer bool) (local *sections.PageSection, ambiguous string, err error) {
	byID, err := readSection(ctx, p.q, `id = $1 AND scope = $2 AND library_id IS NOT DISTINCT FROM $3`, e.ID, e.Scope, library)
	if err != nil {
		return nil, "", err
	}
	if len(byID) == 1 && (sameServer || string(byID[0].SectionType) == e.SectionType) {
		return byID[0], "", nil
	}
	named, err := readSection(ctx, p.q, `scope = $1 AND library_id IS NOT DISTINCT FROM $2 AND section_type = $3 AND title = $4`,
		e.Scope, library, e.SectionType, e.Title)
	if err != nil {
		return nil, "", err
	}
	var candidates []*sections.PageSection
	for _, s := range named {
		if !p.isBackupSection(s.ID, e) && !p.claimedByOther(claimSection, s.ID, e.itemID()) {
			candidates = append(candidates, s)
		}
	}
	twins := p.sectionTwins(e, library, libMap)
	switch {
	case len(candidates) == 0:
		return nil, "", nil
	case twins > 1:
		return nil, fmt.Sprintf("%d sections of the backup have this type and title on the page, and a section here does too.", twins), nil
	case len(candidates) > 1:
		return nil, fmt.Sprintf("%d sections of this type have this title on the page here.", len(candidates)), nil
	}
	return candidates[0], "", nil
}

// collectionTwins counts the backup's collections with c's title, in any
// case, whose library maps to library, c included.
func (p *planner) collectionTwins(c CollectionEntry, library int, libMap map[int]int) int {
	n := 0
	for _, other := range p.rs.Collections.Collections {
		if local, ok := libMap[other.LibraryID]; ok && local == library && strings.EqualFold(other.Title, c.Title) {
			n++
		}
	}
	return n
}

// isBackupCollection reports whether id is the id of one of the backup's
// collections other than c.
func (p *planner) isBackupCollection(id string, c CollectionEntry) bool {
	if id == c.ID {
		return false
	}
	for _, other := range p.rs.Collections.Collections {
		if other.ID == id {
			return true
		}
	}
	for _, other := range p.rs.Collections.TemplateCollections {
		if other.ID == id {
			return true
		}
	}
	return false
}

// matchCollection finds a collection here in its mapped library: the one
// with the backup's id (a restore keeps it when it is free), else the one
// with its name that is not another backup collection's and that no other
// item of this restore wrote, provided no other collection of the backup
// has that name there. ambiguous says why it cannot choose.
func (p *planner) matchCollection(ctx context.Context, c CollectionEntry, library int, libMap map[int]int) (local, ambiguous string, err error) {
	byID, err := collectStringsArgs(ctx, p.q, `
		SELECT c.id FROM library_collections c
		JOIN library_collection_libraries l ON l.collection_id = c.id AND l.library_id = $1
		WHERE c.id = $2`, library, c.ID)
	if err != nil {
		return "", "", err
	}
	if len(byID) == 1 {
		return byID[0], "", nil
	}
	named, err := collectionsNamed(ctx, p.q, library, c.Title)
	if err != nil {
		return "", "", err
	}
	var candidates []string
	for _, id := range named {
		if !p.isBackupCollection(id, c) && !p.claimedByOther(claimCollection, id, collectionItemID(c.ID)) {
			candidates = append(candidates, id)
		}
	}
	twins := p.collectionTwins(c, library, libMap)
	switch {
	case len(candidates) == 0:
		return "", "", nil
	case twins > 1:
		return "", fmt.Sprintf("%d collections of the backup have this name in its library, and a collection here does too.", twins), nil
	case len(candidates) > 1:
		return "", fmt.Sprintf("%d collections in its library here have this name.", len(candidates)), nil
	}
	return candidates[0], "", nil
}

// templateTwins counts the backup's template collections from t's bundle
// and template whose library maps to library, t included.
func (p *planner) templateTwins(t TemplateCollectionEntry, library int, libMap map[int]int) int {
	n := 0
	for _, other := range p.rs.Collections.TemplateCollections {
		if local, ok := libMap[other.LibraryID]; ok && local == library && other.BundleID == t.BundleID && other.TemplateID == t.TemplateID {
			n++
		}
	}
	return n
}

// freeCollectionID keeps the backup's collection id when no collection here
// has it, so a later restore finds the collection it created by id.
func freeCollectionID(ctx context.Context, q queryer, id string) (string, error) {
	var taken bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM library_collections WHERE id = $1)`, id).Scan(&taken); err != nil {
		return "", err
	}
	if !taken && id != "" {
		return id, nil
	}
	return idgen.NextID()
}
