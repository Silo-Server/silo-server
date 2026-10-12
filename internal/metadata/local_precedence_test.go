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

// The lists a local NFO supplies are the whole list: a remote provider folded
// after it cannot extend them, and only fills the lists the NFO left empty.
func TestFold_LocalListsAreAuthoritative(t *testing.T) {
	t.Parallel()

	local := &MetadataResult{
		HasMetadata: true,
		Title:       "Local Title",
		Genres:      []string{"Local Genre"},
		Studios:     []string{"Local Studio"},
		Countries:   []string{"Localland"},
		People:      []models.ItemPerson{actor("Leonardo DiCaprio", "Local Role"), actor("Local Only Actor", "Somebody")},
	}
	remote := &MetadataResult{
		HasMetadata: true,
		Title:       "Remote Title",
		Genres:      []string{"Action", "Science Fiction"},
		Studios:     []string{"Remote Studio"},
		Networks:    []string{"Remote Network"},
		Countries:   []string{"United States"},
		Keywords:    []string{"dream", "heist"},
		People: []models.ItemPerson{
			{Person: models.Person{Name: "Leonardo DiCaprio", TmdbID: "6193", PhotoPath: "/dicaprio.jpg"}, Kind: models.PersonKindActor, Character: "Cobb"},
			actor("Joseph Gordon-Levitt", "Arthur"),
			crewMember("Christopher Nolan", models.PersonKindDirector),
		},
	}

	accumulator := &MetadataResult{}
	foldProviderResult(accumulator, local, "en", "nfo", true, nil)
	foldProviderResult(accumulator, remote, "en", "tmdb", false, nil)

	if accumulator.Title != "Local Title" {
		t.Errorf("Title = %q, want the NFO title", accumulator.Title)
	}
	for field, got := range map[string][]string{
		"Genres":    accumulator.Genres,
		"Studios":   accumulator.Studios,
		"Countries": accumulator.Countries,
	} {
		want := map[string][]string{"Genres": {"Local Genre"}, "Studios": {"Local Studio"}, "Countries": {"Localland"}}[field]
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v, want only the NFO's %v", field, got, want)
		}
	}
	// Lists the NFO left empty come from the remote provider.
	if !reflect.DeepEqual(accumulator.Networks, []string{"Remote Network"}) {
		t.Errorf("Networks = %v, want the remote list", accumulator.Networks)
	}
	if !reflect.DeepEqual(accumulator.Keywords, []string{"dream", "heist"}) {
		t.Errorf("Keywords = %v, want the remote list", accumulator.Keywords)
	}

	// Cast: the NFO's two actors, the matching one enriched, nobody added.
	cast, crew := splitPeopleByRole(accumulator.People)
	if got, want := personNames(cast), []string{"Leonardo DiCaprio", "Local Only Actor"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cast = %v, want %v (remote must not add actors the NFO did not list)", got, want)
	}
	if cast[0].TmdbID != "6193" || cast[0].PhotoPath != "/dicaprio.jpg" {
		t.Errorf("matching actor not enriched: %+v", cast[0])
	}
	if cast[0].Character != "Local Role" {
		t.Errorf("Character = %q, want the NFO role kept", cast[0].Character)
	}
	// The NFO listed no crew, so the remote crew fills it.
	if got := personNames(crew); !reflect.DeepEqual(got, []string{"Christopher Nolan"}) {
		t.Errorf("crew = %v, want the remote crew (NFO listed none)", got)
	}

	// The remote result itself is untouched: provider results can be shared.
	if len(remote.Genres) != 2 || len(remote.People) != 3 {
		t.Errorf("remote result was modified: genres=%v people=%d", remote.Genres, len(remote.People))
	}
}

// An empty NFO list leaves the field to remote providers, and remote cast
// enriches an NFO actor's empty role.
func TestFold_LocalWithoutListsLeavesThemToRemote(t *testing.T) {
	t.Parallel()

	local := &MetadataResult{HasMetadata: true, Title: "Local Title", People: []models.ItemPerson{actor("Keanu Reeves", "")}}
	remote := &MetadataResult{
		HasMetadata: true,
		Genres:      []string{"Action"},
		Studios:     []string{"Warner Bros."},
		People:      []models.ItemPerson{actor("Keanu Reeves", "Neo"), actor("Laurence Fishburne", "Morpheus")},
	}
	accumulator := &MetadataResult{}
	foldProviderResult(accumulator, local, "en", "nfo", true, nil)
	foldProviderResult(accumulator, remote, "en", "tmdb", false, nil)

	if !reflect.DeepEqual(accumulator.Genres, []string{"Action"}) || !reflect.DeepEqual(accumulator.Studios, []string{"Warner Bros."}) {
		t.Errorf("genres=%v studios=%v, want the remote lists", accumulator.Genres, accumulator.Studios)
	}
	if got := personNames(accumulator.People); !reflect.DeepEqual(got, []string{"Keanu Reeves"}) {
		t.Errorf("people = %v, want only the NFO cast", got)
	}
	if accumulator.People[0].Character != "Neo" {
		t.Errorf("Character = %q, want the remote role filling the NFO's empty one", accumulator.People[0].Character)
	}
}

// Between remote providers lists still accumulate as before.
func TestFold_RemoteProvidersStillUnionLists(t *testing.T) {
	t.Parallel()

	first := &MetadataResult{HasMetadata: true, Studios: []string{"A"}, People: []models.ItemPerson{actor("One", "")}}
	second := &MetadataResult{HasMetadata: true, Studios: []string{"B"}, People: []models.ItemPerson{actor("Two", "")}}
	accumulator := &MetadataResult{}
	foldProviderResult(accumulator, first, "en", "tvdb", false, nil)
	foldProviderResult(accumulator, second, "en", "tmdb", false, nil)

	if !reflect.DeepEqual(accumulator.Studios, []string{"A", "B"}) {
		t.Errorf("Studios = %v, want the union of remote lists", accumulator.Studios)
	}
	if got := personNames(accumulator.People); !reflect.DeepEqual(got, []string{"One", "Two"}) {
		t.Errorf("people = %v, want the union of remote cast", got)
	}
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

// When the NFO lists cast but no crew, the remote crew fills the crew matched
// within its own role: a director the NFO lists as an actor, with the same
// provider ID, keeps the director credit.
func TestFold_RemoteCrewFillsWithoutFoldingIntoLocalCast(t *testing.T) {
	t.Parallel()

	local := &MetadataResult{HasMetadata: true, People: []models.ItemPerson{
		{Person: models.Person{Name: "Clint Eastwood", TmdbID: "190"}, Kind: models.PersonKindActor, Character: "Will Munny"},
	}}
	remote := &MetadataResult{HasMetadata: true, People: []models.ItemPerson{
		{Person: models.Person{Name: "Clint Eastwood", TmdbID: "190", PhotoPath: "/clint.jpg"}, Kind: models.PersonKindActor, Character: "William Munny"},
		{Person: models.Person{Name: "Clint Eastwood", TmdbID: "190"}, Kind: models.PersonKindDirector},
		{Person: models.Person{Name: "Gene Hackman", TmdbID: "193"}, Kind: models.PersonKindActor, Character: "Little Bill"},
	}}
	accumulator := &MetadataResult{}
	foldProviderResult(accumulator, local, "en", "nfo", true, nil)
	foldProviderResult(accumulator, remote, "en", "tmdb", false, nil)

	cast, crew := splitPeopleByRole(accumulator.People)
	if got := personNames(cast); !reflect.DeepEqual(got, []string{"Clint Eastwood"}) {
		t.Errorf("cast = %v, want only the NFO actor", got)
	}
	if len(cast) == 1 && (cast[0].Character != "Will Munny" || cast[0].PhotoPath != "/clint.jpg") {
		t.Errorf("actor credit = %+v, want the NFO role, enriched", cast[0])
	}
	if len(crew) != 1 || crew[0].Kind != models.PersonKindDirector || crew[0].TmdbID != "190" {
		t.Errorf("crew = %+v, want the remote director credit", crew)
	}
	if len(remote.People) != 3 {
		t.Errorf("remote result was modified: %d people", len(remote.People))
	}
}
