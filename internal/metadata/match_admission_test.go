package metadata

import (
	"context"
	"errors"
	"testing"
)

type admissionOwner struct {
	owners map[string]string
	err    error
}

func (o admissionOwner) FindContentIDByProviderIDs(
	_ context.Context,
	providerIDs map[string]string,
	_ string,
	_ string,
) (string, error) {
	if o.err != nil {
		return "", o.err
	}
	for _, providerID := range providerIDs {
		if owner := o.owners[providerID]; owner != "" {
			return owner, nil
		}
	}
	return "", nil
}

func TestAdmitSearchMatchUsesTheAliasThatActuallyMatched(t *testing.T) {
	got, err := AdmitSearchMatch(context.Background(), SearchMatchAdmissionRequest{
		WantTitle: "Mother of Storms",
		Results: []SearchResult{{
			Name:         "Sturmmutter",
			TitleAliases: []TitleAlias{{Title: "Mother of Storms"}},
			ProviderIDs:  map[string]string{"openlibrary": "OL1M"},
		}},
	})
	if err != nil {
		t.Fatalf("AdmitSearchMatch: %v", err)
	}
	if got.Status != SearchMatchAccepted || got.AgreedTitle != "Mother of Storms" {
		t.Fatalf("admission = %+v, want accepted with the matching alias as agreement title", got)
	}
}

func TestAdmitSearchMatchRejectsCrossProviderDisagreement(t *testing.T) {
	got, err := AdmitSearchMatch(context.Background(), SearchMatchAdmissionRequest{
		WantTitle:   "Mother of Storms",
		AgreedTitle: "The Good Mothers",
		Results: []SearchResult{{
			Name:        "Mother of Storms",
			ProviderIDs: map[string]string{"openlibrary": "OL1M"},
		}},
	})
	if err != nil {
		t.Fatalf("AdmitSearchMatch: %v", err)
	}
	if got.Status != SearchMatchProviderDisagreement || len(got.ProviderIDs) != 0 {
		t.Fatalf("admission = %+v, want provider disagreement with no admitted IDs", got)
	}
}

func TestAdmitSearchMatchReportsOwnedIDsWithoutAnchoringAgreement(t *testing.T) {
	got, err := AdmitSearchMatch(context.Background(), SearchMatchAdmissionRequest{
		WantTitle: "Mother of Storms",
		Results: []SearchResult{{
			Name:        "Mother of Storms",
			ProviderIDs: map[string]string{"openlibrary": "OL1M"},
		}},
		Owner: admissionOwner{owners: map[string]string{"OL1M": "other-book"}},
	})
	if err != nil {
		t.Fatalf("AdmitSearchMatch: %v", err)
	}
	if got.Status != SearchMatchNoUsableProviderIDs || got.AgreedTitle != "" || len(got.Conflicts) != 1 {
		t.Fatalf("admission = %+v, want an unanchored ownership conflict", got)
	}
}

func TestAdmitSearchMatchOwnershipFailureIsAtomic(t *testing.T) {
	checkErr := errors.New("database unavailable")
	got, err := AdmitSearchMatch(context.Background(), SearchMatchAdmissionRequest{
		WantTitle: "Mother of Storms",
		Results: []SearchResult{{
			Name: "Mother of Storms",
			ProviderIDs: map[string]string{
				"openlibrary": "OL1M",
				"googlebooks": "GB1",
			},
		}},
		Owner: admissionOwner{err: checkErr},
	})
	if !errors.Is(err, checkErr) {
		t.Fatalf("AdmitSearchMatch error = %v, want %v", err, checkErr)
	}
	if len(got.ProviderIDs) != 0 {
		t.Fatalf("ownership failure partially admitted IDs: %+v", got.ProviderIDs)
	}
}

func TestAdmitProviderIDsQuarantinesOwnedCrossIDs(t *testing.T) {
	got, err := AdmitProviderIDs(context.Background(), ProviderIDAdmissionRequest{
		CandidateProviderIDs: map[string]string{
			"openlibrary": "OL-owned",
			"googlebooks": "GB-free",
		},
		ExistingProviderIDs: map[string]string{"isbn": "9780306406157"},
		Owner:               admissionOwner{owners: map[string]string{"OL-owned": "other-book"}},
		ItemType:            "ebook",
		ContentID:           "this-book",
	})
	if err != nil {
		t.Fatalf("AdmitProviderIDs: %v", err)
	}
	if got.ProviderIDs["googlebooks"] != "GB-free" {
		t.Fatalf("free identity was not admitted: %+v", got.ProviderIDs)
	}
	if _, exists := got.ProviderIDs["openlibrary"]; exists {
		t.Fatalf("owned identity was admitted: %+v", got.ProviderIDs)
	}
	if !got.HasUsableIdentity || len(got.Conflicts) != 1 || got.Conflicts[0].OwnedBy != "other-book" {
		t.Fatalf("provider-ID admission = %+v, want one quarantined conflict and one usable ID", got)
	}
}

func TestAdmitProviderIDsRejectsConflictingIDForExistingProvider(t *testing.T) {
	got, err := AdmitProviderIDs(context.Background(), ProviderIDAdmissionRequest{
		CandidateProviderIDs: map[string]string{
			"openlibrary": "OL-new",
			"googlebooks": "GB-free",
		},
		ExistingProviderIDs: map[string]string{"openlibrary": "OL-current"},
		ItemType:            "ebook",
		ContentID:           "this-book",
	})
	if err != nil {
		t.Fatalf("AdmitProviderIDs: %v", err)
	}
	if !got.ContradictsExisting || len(got.Conflicts) != 1 {
		t.Fatalf("admission = %+v, want one contradiction", got)
	}
	conflict := got.Conflicts[0]
	if conflict.Provider != "openlibrary" || conflict.ProviderID != "OL-new" ||
		conflict.ExistingProviderID != "OL-current" || conflict.OwnedBy != "this-book" {
		t.Fatalf("conflict = %+v", conflict)
	}
}

func TestAdmitSearchMatchRejectsConflictingExistingProviderID(t *testing.T) {
	got, err := AdmitSearchMatch(context.Background(), SearchMatchAdmissionRequest{
		WantTitle: "Mother of Storms",
		Results: []SearchResult{{
			Name: "Mother of Storms",
			ProviderIDs: map[string]string{
				"openlibrary": "OL-new",
				"googlebooks": "GB-free",
			},
		}},
		ExistingProviderIDs: map[string]string{"openlibrary": "OL-current"},
		ItemType:            "ebook",
		ContentID:           "this-book",
	})
	if err != nil {
		t.Fatalf("AdmitSearchMatch: %v", err)
	}
	if got.Status != SearchMatchProviderIDConflict || len(got.ProviderIDs) != 0 {
		t.Fatalf("admission = %+v, want provider-ID conflict with no admitted IDs", got)
	}
}

// An item with no author has only its title to vouch for a candidate, so the
// candidate must carry the whole title (#3161). The two untagged books from the
// issue stay unmatched; a provider's longer form of the same title, an item
// with an author, and an item with a provider ID are admitted as before.
func TestAdmitSearchMatchWithoutAnAuthorNeedsTheWholeTitle(t *testing.T) {
	for _, tc := range []struct {
		name      string
		want      string
		candidate string
		noAuthor  bool
		existing  map[string]string
		accept    bool
	}{
		{name: "a one-word prefix", want: "Raw Stream", candidate: "Raw", noAuthor: true},
		{name: "two shared words", want: "Unknown Author Book", candidate: "The Book of Unknown Americans", noAuthor: true},
		{name: "a contained shorter title", want: "Dune Messiah", candidate: "Dune", noAuthor: true},
		{name: "the same title", want: "Raw Stream", candidate: "Raw Stream", noAuthor: true, accept: true},
		{name: "the title with a series suffix", want: "Project Hail Mary", candidate: "Project Hail Mary (Unabridged)", noAuthor: true, accept: true},
		{name: "a partial match with an author", want: "Raw Stream", candidate: "Raw", accept: true},
		{name: "a partial match with a provider ID", want: "Raw Stream", candidate: "Raw", noAuthor: true, existing: map[string]string{"audible": "B0OTHER"}, accept: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AdmitSearchMatch(context.Background(), SearchMatchAdmissionRequest{
				WantTitle:           tc.want,
				Results:             []SearchResult{{Name: tc.candidate, ProviderIDs: map[string]string{"apple_books": "1417703839"}}},
				ExistingProviderIDs: tc.existing,
				NoAuthor:            tc.noAuthor,
			})
			if err != nil {
				t.Fatalf("AdmitSearchMatch: %v", err)
			}
			if accepted := got.Status == SearchMatchAccepted; accepted != tc.accept {
				t.Fatalf("%q for %q: status %s, want accepted = %v", tc.candidate, tc.want, got.Status, tc.accept)
			}
		})
	}
}
