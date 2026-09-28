package scenariocatalog

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestAPIKeyListAbsenceOverlayPreservesOriginals(t *testing.T) {
	catalogs, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := APIKeyListAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				count++
				if err := ValidateScenarioPairing(r, s); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if count != 6 {
		t.Fatalf("selected %d scenarios, want exactly 6", count)
	}
	after, err := json.Marshal(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("selector mutated original catalog bytes")
	}
	again, err := APIKeyListAcceptance(selected)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(selected)
	b, _ := json.Marshal(again)
	if !bytes.Equal(a, b) {
		t.Fatal("selection is not stable")
	}
	// Altering a copied expectation must not change the original or its sibling.
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				if s.ID == "keys_list.meaning" {
					s.V2Expectation.Expect.Body[0].Value[0] = '9'
				}
			}
		}
	}
	after, _ = json.Marshal(catalogs)
	if !bytes.Equal(before, after) {
		t.Fatal("overlay aliases original bytes")
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				if s.ID == "keys_list.shape" && string(s.V2Expectation.Expect.Body[0].Value) != "2" {
					t.Fatal("overlay expectations alias each other")
				}
			}
		}
	}
}

func TestAPIKeyListAbsenceOverlayRefusesChangedOriginals(t *testing.T) {
	for _, id := range []string{"keys_list.meaning", "keys_list.shape"} {
		for _, mutation := range []string{"missing", "duplicate", "id", "oracle", "description", "principal", "request", "row", "pair"} {
			t.Run(id+"/"+mutation, func(t *testing.T) {
				catalogs, err := Load()
				if err != nil {
					t.Fatal(err)
				}
				for _, c := range catalogs {
					for ri := range c.Rows {
						r := &c.Rows[ri]
						for si := range r.Scenarios {
							s := &r.Scenarios[si]
							if s.ID != id {
								continue
							}
							switch mutation {
							case "missing":
								r.Scenarios = append(r.Scenarios[:si], r.Scenarios[si+1:]...)
							case "duplicate":
								r.Scenarios = append(r.Scenarios, *s)
							case "id":
								s.ID += ".changed"
							case "oracle":
								s.Expect.Body[0].Value = json.RawMessage(`0`)
							case "description":
								s.Description += " changed"
							case "principal":
								s.Principal.Class = "user"
							case "request":
								s.Request.Path = "/api/v1/api-keys/changed"
							case "row":
								r.RegistrationIndex = 1
							case "pair":
								s.V2Expectation = &V2Expectation{Kind: "equivalent"}
							}
							break
						}
					}
				}
				if _, err := APIKeyListAcceptance(catalogs); err == nil || (mutation != "row" && !strings.Contains(err.Error(), id)) {
					t.Fatalf("accepted %s: %v", mutation, err)
				}
			})
		}
	}
}
