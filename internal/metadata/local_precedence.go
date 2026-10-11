package metadata

import "github.com/Silo-Server/silo-server/internal/models"

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

// restrict returns a remote result with the lists the local provider supplied
// removed, so merging it cannot extend them. Its cast and crew instead enrich
// the matching local credits in accumulator (provider IDs, photos, roles)
// without adding anyone the local files did not list. The result itself is
// not modified: provider results can be shared.
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
	if l.cast || l.crew {
		remoteCast, remoteCrew := splitPeopleByRole(result.People)
		var kept []models.ItemPerson
		if l.cast {
			enrichPeople(accumulator.People, remoteCast)
		} else {
			kept = append(kept, remoteCast...)
		}
		if l.crew {
			enrichPeople(accumulator.People, remoteCrew)
		} else {
			kept = append(kept, remoteCrew...)
		}
		out.People = kept
	}
	return &out
}
