package scenariocatalog

import "testing"

func TestRequiredSectionReadsCannotShrink(t *testing.T) {
	for _, id := range RequiredSectionReadScenarios {
		t.Run(id, func(t *testing.T) {
			catalogs, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			selected, err := SectionReadAcceptance(catalogs)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range selected {
				for ri := range c.Rows {
					for si := range c.Rows[ri].Scenarios {
						s := &c.Rows[ri].Scenarios[si]
						if s.ID == id {
							s.V2Expectation = nil
						}
					}
				}
			}
			if _, err := SectionReadAcceptance(selected); err == nil {
				t.Fatal("accepted missing required pairing")
			}
		})
	}
}
