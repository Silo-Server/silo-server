package metadata

import "slices"

// localListFields records which list fields the local provider (NFO)
// supplied while one pipeline run folds provider results together. Precedence
// is field locks, then local metadata, then remote providers, so a list the
// local files supply is the whole list: remote providers fill only the lists
// they left empty. Cast and crew count as two lists, the way their locks do.
// Between remote providers lists still accumulate as before.
type localListFields struct {
	genres, studios, networks, countries, keywords, cast, crew bool
}

// record notes the lists a local result supplies.
func (l *localListFields) record(result *MetadataResult) {
	l.genres = l.genres || len(result.Genres) > 0
	l.studios = l.studios || len(result.Studios) > 0
	l.networks = l.networks || len(result.Networks) > 0
	l.countries = l.countries || len(result.Countries) > 0
	l.keywords = l.keywords || len(result.Keywords) > 0
	cast, crew := splitPeopleByRole(result.People)
	l.cast = l.cast || len(cast) > 0
	l.crew = l.crew || len(crew) > 0
}

// keepStoredLists prepares a fill-empty write (initial match of a stored
// item, scheduled refresh): a list the local provider supplied fills a stored
// list only when it is empty, like the NFO's scalar fields, instead of being
// added to it. A manual refresh replaces it. Credits follow the same rule in
// keepStoredLocalCredits, which has to read them.
func (l localListFields) keepStoredLists(accumulator, stored *MetadataResult) {
	keep := func(supplied bool, local *[]string, storedList []string) {
		if supplied && len(storedList) > 0 {
			*local = nil
		}
	}
	keep(l.genres, &accumulator.Genres, stored.Genres)
	keep(l.studios, &accumulator.Studios, stored.Studios)
	keep(l.networks, &accumulator.Networks, stored.Networks)
	keep(l.countries, &accumulator.Countries, stored.Countries)
	keep(l.keywords, &accumulator.Keywords, stored.Keywords)
}

// restrict returns a remote result with the lists the local provider supplied
// removed, so merging it cannot extend them. Its cast and crew instead enrich
// the matching local credits in accumulator (provider IDs, photos, roles)
// without adding anyone the local files did not list, and a credit group the
// local files left empty fills from the remote one here, matched within its
// own role. The result itself is not modified: provider results can be
// shared.
func (l localListFields) restrict(accumulator, result *MetadataResult) *MetadataResult {
	if l == (localListFields{}) {
		return result
	}
	out := *result
	if l.genres {
		out.Genres = nil
	}
	if l.studios {
		out.Studios = nil
	}
	if l.networks {
		out.Networks = nil
	}
	if l.countries {
		out.Countries = nil
	}
	if l.keywords {
		out.Keywords = nil
	}
	if (l.cast || l.crew) && len(result.People) > 0 {
		// The accumulator's credits may share their array with the local
		// result; enrich a copy.
		accumulator.People = slices.Clone(accumulator.People)
		remoteCast, remoteCrew := splitPeopleByRole(result.People)
		if l.cast {
			enrichPeople(accumulator.People, remoteCast, true)
		}
		if l.crew {
			enrichPeople(accumulator.People, remoteCrew, false)
		}
		// Merging across roles would match a remote director to the same
		// person's local actor credit by provider ID and drop the director.
		mergePeopleUnlocked(&accumulator.People, result.People, MergeFillEmpty, l.cast, l.crew)
		out.People = nil
	}
	return &out
}
