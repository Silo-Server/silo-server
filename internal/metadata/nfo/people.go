package nfo

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/providerid"
)

// actorKinds maps a Jellyfin <actor><type> to the credit kind. An absent or
// unknown type is an actor.
var actorKinds = map[string]models.PersonKind{
	"actor":     models.PersonKindActor,
	"gueststar": models.PersonKindGuestStar,
	"director":  models.PersonKindDirector,
	"writer":    models.PersonKindWriter,
	"producer":  models.PersonKindProducer,
	"composer":  models.PersonKindComposer,
	"creator":   models.PersonKindCreator,
}

var personIMDbIDRe = regexp.MustCompile(`(?i)^nm\d{5,}$`)

// applyPeople maps <actor> entries to credits, and <director>, <credits>, and
// <writer> to crew. Kodi writes a film's writers as <credits> and Jellyfin
// writes them twice, as <credits> and <writer>, so a name in both is one
// writer credit. An actor's <thumb> is kept unresolved in LocalPhotoSource.
func applyPeople(p *parsedNFO, c *nfoCommon) {
	people := make([]models.ItemPerson, 0, len(c.Actors)+len(c.Directors)+len(c.Credits)+len(c.Writers))
	for i, actor := range c.Actors {
		name := strings.TrimSpace(actor.Name)
		if name == "" {
			continue
		}
		order := i
		if declared, ok := parseNFOInt(actor.Order); ok {
			order = declared
		} else if declared, ok := parseNFOInt(actor.SortOrder); ok {
			order = declared
		}
		kind, ok := actorKinds[strings.ToLower(strings.TrimSpace(actor.Type))]
		if !ok {
			kind = models.PersonKindActor
		}
		credit := models.ItemPerson{
			Person: models.Person{
				Name:   name,
				TmdbID: digitsOnly(actor.TmdbID),
				ImdbID: personIMDbID(actor.ImdbID),
				TvdbID: digitsOnly(actor.TvdbID),
			},
			Kind:             kind,
			SortOrder:        order,
			LocalPhotoSource: strings.TrimSpace(actor.Thumb),
		}
		if kind == models.PersonKindActor || kind == models.PersonKindGuestStar {
			credit.Character = strings.TrimSpace(actor.Role)
		}
		people = append(people, credit)
	}
	// Jellyfin lists a director or writer both as a typed <actor>, with
	// their ids and photo, and in the dedicated element; that is one credit.
	listed := make(map[string]bool, len(people))
	for _, credit := range people {
		listed[creditName(credit.Name, credit.Kind)] = true
	}
	people = append(people, crewPeople(splitNFOList(c.Directors), models.PersonKindDirector, listed)...)
	people = append(people, crewPeople(splitNFOList(slices.Concat(c.Credits, c.Writers)), models.PersonKindWriter, listed)...)
	if len(people) > 0 {
		p.People = people
	}
}

// crewPeople returns credits of kind for names, leaving out the ones listed
// already holds.
func crewPeople(names []string, kind models.PersonKind, listed map[string]bool) []models.ItemPerson {
	people := make([]models.ItemPerson, 0, len(names))
	for _, name := range names {
		if listed[creditName(name, kind)] {
			continue
		}
		people = append(people, models.ItemPerson{
			Person:    models.Person{Name: name},
			Kind:      kind,
			SortOrder: len(people),
		})
	}
	return people
}

// creditName keys a credit by its person's name, ignoring case, and kind.
func creditName(name string, kind models.PersonKind) string {
	return strings.ToLower(name) + "|" + strconv.Itoa(int(kind))
}

// digitsOnly returns a numeric provider id, or "" for anything else,
// including the zero some writers put in place of an unknown id.
func digitsOnly(raw string) string {
	value := strings.TrimSpace(raw)
	if !providerid.IsPositiveDecimal(value) {
		return ""
	}
	return value
}

// personIMDbID returns an IMDb person id (nm0000148), or "".
func personIMDbID(raw string) string {
	value := strings.TrimSpace(raw)
	if !personIMDbIDRe.MatchString(value) {
		return ""
	}
	return strings.ToLower(value)
}
