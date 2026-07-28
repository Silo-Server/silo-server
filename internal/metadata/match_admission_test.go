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
