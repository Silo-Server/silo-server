package userstore

// CatalogSQLProvider identifies stores whose authoritative viewer state lives
// beside the PostgreSQL catalog. Callers must not infer this from wrapper types
// or the availability of unrelated store features.
type CatalogSQLProvider interface {
	CatalogStateInPostgres() bool
}

func HasCatalogSQLState(store UserStore) bool {
	provider, ok := store.(CatalogSQLProvider)
	return ok && provider.CatalogStateInPostgres()
}
