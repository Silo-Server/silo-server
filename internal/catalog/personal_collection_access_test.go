package catalog

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
)

func TestIntersectAccessLibraries(t *testing.T) {
	cases := []struct {
		name          string
		viewer, owner []int
		want          []int
	}{
		{"both unrestricted", nil, nil, nil},
		{"restricted owner limits an unrestricted viewer", nil, []int{2, 1}, []int{2, 1}},
		{"restricted viewer keeps its limit under an unrestricted owner", []int{3}, nil, []int{3}},
		{"overlap", []int{1, 2, 3}, []int{3, 2, 9}, []int{2, 3}},
		{"disjoint denies every library", []int{1}, []int{2}, []int{}},
		{"an owner with no library denies every library", nil, []int{}, []int{}},
		{"a viewer with no library stays denied", []int{}, nil, []int{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IntersectAccess(AccessFilter{AllowedLibraryIDs: tc.viewer}, AccessFilter{AllowedLibraryIDs: tc.owner}).AllowedLibraryIDs
			if (got == nil) != (tc.want == nil) || !reflect.DeepEqual(append([]int{}, got...), append([]int{}, tc.want...)) {
				t.Fatalf("AllowedLibraryIDs = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestIntersectAccessDoesNotAliasInputs(t *testing.T) {
	owner := []int{1, 2}
	got := IntersectAccess(AccessFilter{}, AccessFilter{AllowedLibraryIDs: owner})
	got.AllowedLibraryIDs[0] = 99
	if owner[0] != 1 {
		t.Fatal("the intersected filter shares the owner's library slice")
	}
}

func TestIntersectAccessMaturityLimits(t *testing.T) {
	cases := []struct {
		name          string
		viewer, owner access.MaturityLimits
		want          access.MaturityLimits
	}{
		{"no limits", access.MaturityLimits{}, access.MaturityLimits{}, access.MaturityLimits{}},
		{
			"a PG owner limits an unrestricted viewer",
			access.MaturityLimits{AllowUnratedContent: true},
			access.MaturityLimits{MaxContentRating: "PG"},
			access.MaturityLimits{MaxContentRating: "PG", AllowUnratedContent: true},
		},
		{
			"a PG viewer keeps its ceiling under an unrestricted owner",
			access.MaturityLimits{MaxContentRating: "PG"},
			access.MaturityLimits{},
			access.MaturityLimits{MaxContentRating: "PG"},
		},
		{
			"the lower ceiling wins either way",
			access.MaturityLimits{MaxContentRating: "R"},
			access.MaturityLimits{MaxContentRating: "PG-13"},
			access.MaturityLimits{MaxContentRating: "PG-13"},
		},
		{
			"an unusable ceiling stays fail-closed",
			access.MaturityLimits{MaxContentRating: "PG"},
			access.MaturityLimits{MaxContentRating: "not a rating"},
			access.MaturityLimits{MaxContentRating: "not a rating"},
		},
		{
			"the smaller advisory limit wins",
			access.MaturityLimits{MaxAdvisoryAge: 16},
			access.MaturityLimits{MaxAdvisoryAge: 12},
			access.MaturityLimits{MaxAdvisoryAge: 12},
		},
		{
			"an advisory limit on one side applies",
			access.MaturityLimits{},
			access.MaturityLimits{MaxAdvisoryAge: 10},
			access.MaturityLimits{MaxAdvisoryAge: 10},
		},
		{
			"either side requiring an advisory age hides unadvised titles",
			access.MaturityLimits{MaxAdvisoryAge: 12},
			access.MaturityLimits{MaxAdvisoryAge: 16, RequireAdvisoryAge: true},
			access.MaturityLimits{MaxAdvisoryAge: 12, RequireAdvisoryAge: true},
		},
		{
			"a stored requirement without a limit changes nothing",
			access.MaturityLimits{MaxAdvisoryAge: 12},
			access.MaturityLimits{RequireAdvisoryAge: true},
			access.MaturityLimits{MaxAdvisoryAge: 12},
		},
		{
			"the unrated policy is the viewer's",
			access.MaturityLimits{AllowUnratedContent: false},
			access.MaturityLimits{AllowUnratedContent: true, MaxContentRating: "PG"},
			access.MaturityLimits{MaxContentRating: "PG"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IntersectAccess(AccessFilter{MaturityLimits: tc.viewer}, AccessFilter{MaturityLimits: tc.owner}).MaturityLimits
			if got != tc.want {
				t.Fatalf("MaturityLimits = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestIntersectAccessKeepsTheViewersIdentityAndPreferences(t *testing.T) {
	library := 4
	viewer := AccessFilter{
		DisabledLibraryIDs:    []int{7},
		PresentationLibraryID: &library,
		PresentationLanguage:  "fr",
		MaxPlaybackQuality:    "1080p",
		UserID:                3,
		ProfileID:             "viewer",
		DeviceID:              "device",
		ExcludedMediaTypes:    []string{"audiobook"},
		AllowedContentIDs:     []string{"a"},
	}
	owner := AccessFilter{
		DisabledLibraryIDs: []int{8},
		UserID:             3,
		ProfileID:          "owner",
		MaxPlaybackQuality: "480p",
	}
	got := IntersectAccess(viewer, owner)
	want := viewer
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IntersectAccess = %+v, want the viewer's filter %+v", got, want)
	}
}

type fakeOwnerAccess struct {
	calls  []string
	filter AccessFilter
	err    error
}

func (f *fakeOwnerAccess) OwnerFilter(_ context.Context, userID int, ownerProfileID string) (AccessFilter, error) {
	f.calls = append(f.calls, ownerProfileID)
	return f.filter, f.err
}

func TestPersonalCollectionFilter(t *testing.T) {
	viewer := AccessFilter{UserID: 3, ProfileID: "viewer", AllowedLibraryIDs: []int{1, 2}}
	t.Run("the owner reads with its own filter and no lookup", func(t *testing.T) {
		owners := &fakeOwnerAccess{err: errors.New("must not be called")}
		got, err := PersonalCollectionFilter(t.Context(), owners, viewer, 3, "viewer", "viewer")
		if err != nil || !reflect.DeepEqual(got, viewer) || len(owners.calls) != 0 {
			t.Fatalf("got %+v, %v after %d lookups; want the viewer's filter and none", got, err, len(owners.calls))
		}
		if _, err := PersonalCollectionFilter(t.Context(), nil, viewer, 3, "viewer", "viewer"); err != nil {
			t.Fatalf("an own collection needs no owner access: %v", err)
		}
	})
	t.Run("another profile's collection is limited to its owner", func(t *testing.T) {
		owners := &fakeOwnerAccess{filter: AccessFilter{AllowedLibraryIDs: []int{2, 5}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}}
		got, err := PersonalCollectionFilter(t.Context(), owners, viewer, 3, "viewer", "owner")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.AllowedLibraryIDs, []int{2}) || got.MaxContentRating != "PG" || got.ProfileID != "viewer" {
			t.Fatalf("got %+v; want libraries [2], ceiling PG, viewer identity", got)
		}
		if !reflect.DeepEqual(owners.calls, []string{"owner"}) {
			t.Fatalf("owner lookups = %v", owners.calls)
		}
	})
	t.Run("an unresolvable owner fails closed", func(t *testing.T) {
		owners := &fakeOwnerAccess{err: errors.New("boom")}
		if _, err := PersonalCollectionFilter(t.Context(), owners, viewer, 3, "viewer", "owner"); !errors.Is(err, ErrPersonalCollectionOwnerAccess) {
			t.Fatalf("err = %v, want ErrPersonalCollectionOwnerAccess", err)
		}
	})
	t.Run("without an owner resolver another profile's collection fails closed", func(t *testing.T) {
		if _, err := PersonalCollectionFilter(t.Context(), nil, viewer, 3, "viewer", "owner"); !errors.Is(err, ErrPersonalCollectionOwnerAccess) {
			t.Fatalf("err = %v, want ErrPersonalCollectionOwnerAccess", err)
		}
	})
	t.Run("a collection with no recorded owner fails closed", func(t *testing.T) {
		owners := &fakeOwnerAccess{}
		if _, err := PersonalCollectionFilter(t.Context(), owners, viewer, 3, "viewer", " "); !errors.Is(err, ErrPersonalCollectionOwnerAccess) {
			t.Fatalf("err = %v, want ErrPersonalCollectionOwnerAccess", err)
		}
		if len(owners.calls) != 0 {
			t.Fatalf("looked up a blank owner: %v", owners.calls)
		}
	})
}
