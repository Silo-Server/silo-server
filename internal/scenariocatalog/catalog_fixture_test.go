package scenariocatalog

import (
	"encoding/json"
	"sync"
	"testing"
)

type catalogFixture struct {
	Catalog *Catalog
	File    string
}

// Mutation tests need independent catalogs, not another schema compilation for
// every table row. Loader and schema tests still exercise Load directly.
var pairingCatalogFixture = sync.OnceValues(func() ([]byte, error) {
	catalogs, err := Load()
	if err != nil {
		return nil, err
	}
	fixtures := make([]catalogFixture, len(catalogs))
	for i, c := range catalogs {
		fixtures[i] = catalogFixture{Catalog: c, File: c.File}
	}
	return json.Marshal(fixtures)
})

func loadPairingCatalogs(t *testing.T) []*Catalog {
	t.Helper()
	data, err := pairingCatalogFixture()
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []catalogFixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	catalogs := make([]*Catalog, len(fixtures))
	for i, fixture := range fixtures {
		c := fixture.Catalog
		c.File = fixture.File
		for ri := range c.Rows {
			for si := range c.Rows[ri].Scenarios {
				c.Rows[ri].Scenarios[si].method = c.Rows[ri].Method
			}
		}
		catalogs[i] = c
	}
	return catalogs
}
