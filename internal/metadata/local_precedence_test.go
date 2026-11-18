package metadata

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func actor(name, role string) models.ItemPerson {
	return models.ItemPerson{Person: models.Person{Name: name}, Kind: models.PersonKindActor, Character: role}
}

func crewMember(name string, kind models.PersonKind) models.ItemPerson {
	return models.ItemPerson{Person: models.Person{Name: name}, Kind: kind}
}

func personNames(people []models.ItemPerson) []string {
	names := make([]string, 0, len(people))
	for _, p := range people {
		names = append(names, p.Name)
	}
	return names
}

// A Cast lock keeps the cast and lets the crew refresh; a Crew lock the
// reverse; both keep everything. Before, a Cast-only lock did not hold.
func TestMergeMetadata_CastAndCrewLocksHoldSeparately(t *testing.T) {
	t.Parallel()

	stored := []models.ItemPerson{actor("Stored Actor", "Hero"), crewMember("Stored Director", models.PersonKindDirector)}
	incoming := []models.ItemPerson{actor("New Actor", "Villain"), crewMember("New Director", models.PersonKindDirector)}

	cases := []struct {
		name   string
		locked []MetadataField
		want   []string
	}{
		{"cast locked", []MetadataField{FieldCast}, []string{"Stored Actor", "New Director"}},
		{"crew locked", []MetadataField{FieldCrew}, []string{"New Actor", "Stored Director"}},
		{"both locked", []MetadataField{FieldCast, FieldCrew}, []string{"Stored Actor", "Stored Director"}},
		{"neither locked", nil, []string{"New Actor", "New Director"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, merge := range []struct {
				name string
				fn   func(source, target *MetadataResult, locked []MetadataField, mode MergeMode)
			}{{"MergeMetadata", MergeMetadata}, {"MergeGlobalMetadata", MergeGlobalMetadata}} {
				target := &MetadataResult{People: append([]models.ItemPerson(nil), stored...)}
				source := &MetadataResult{People: append([]models.ItemPerson(nil), incoming...)}
				merge.fn(source, target, tc.locked, MergeReplaceUnlocked)
				if got := personNames(target.People); !reflect.DeepEqual(got, tc.want) {
					t.Errorf("%s: people = %v, want %v", merge.name, got, tc.want)
				}
			}
		})
	}
}

// One person credited as both actor and director gets each credit enriched
// from the matching remote credit, not both from whichever matched first.
func TestFold_EnrichesEachCreditWithinItsRole(t *testing.T) {
	t.Parallel()

	local := &MetadataResult{HasMetadata: true, People: []models.ItemPerson{
		actor("Clint Eastwood", ""),
		crewMember("Clint Eastwood", models.PersonKindDirector),
	}}
	remote := &MetadataResult{HasMetadata: true, People: []models.ItemPerson{
		{Person: models.Person{Name: "Clint Eastwood", TmdbID: "190"}, Kind: models.PersonKindActor, Character: "William Munny"},
		{Person: models.Person{Name: "Clint Eastwood", TmdbID: "190"}, Kind: models.PersonKindDirector},
	}}
	accumulator := &MetadataResult{}
	foldProviderResult(accumulator, local, "en", "nfo", true, nil)
	foldProviderResult(accumulator, remote, "en", "tmdb", false, nil)

	if len(accumulator.People) != 2 {
		t.Fatalf("people = %+v, want the two NFO credits", accumulator.People)
	}
	for _, p := range accumulator.People {
		if p.TmdbID != "190" {
			t.Errorf("%v credit not enriched: %+v", p.Kind, p)
		}
		if p.Kind == models.PersonKindDirector && p.Character != "" {
			t.Errorf("director credit took the actor's role %q", p.Character)
		}
	}
}
