package scenariocatalog

import "testing"

func TestSectionReplaceAcceptanceRequiresEffectReads(t *testing.T) {
	for _, mutation := range []string{"missing pairing", "missing read", "wrong principal", "wrong scope"} {
		t.Run(mutation, func(t *testing.T) {
			catalogs, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			selected, err := SectionReplaceAcceptance(catalogs)
			if err != nil {
				t.Fatal(err)
			}
			s := &selected[0].Rows[0].Scenarios[0]
			switch mutation {
			case "missing pairing":
				s.V2Expectation = nil
			case "missing read":
				s.V2Expectation.Then = s.V2Expectation.Then[:2]
			case "wrong principal":
				s.V2Expectation.Then[2].Principal.Class = "profile"
			case "wrong scope":
				s.V2Expectation.Then[1].Request.Query = map[string]string{"scope": "library", "library_id": "7"}
			}
			if _, err := SectionReplaceAcceptance(selected); err == nil {
				t.Fatal("accepted incomplete effect evidence")
			}
		})
	}
}
