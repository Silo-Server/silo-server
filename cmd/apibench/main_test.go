package main

import "testing"

func TestDatabaseName(t *testing.T) {
	// Keep libpq environment defaults out of the keyword/value case.
	t.Setenv("PGDATABASE", "")
	t.Setenv("PGSERVICE", "")
	for _, tc := range []struct{ dsn, want string }{
		{"postgres://silo:silo@127.0.0.1:5432/silo?sslmode=disable", "silo"},
		{"postgresql://silo@db.internal/silo_bench", "silo_bench"},
		{"host=127.0.0.1 port=5432 user=silo password=silo dbname=silo sslmode=disable", "silo"},
		{"host=db.internal dbname=silo_bench user=silo", "silo_bench"},
	} {
		got, err := databaseName(tc.dsn)
		if err != nil {
			t.Fatalf("databaseName(%q): %v", tc.dsn, err)
		}
		if got != tc.want {
			t.Errorf("databaseName(%q) = %q, want %q", tc.dsn, got, tc.want)
		}
	}
	if _, err := databaseName("host=x port=notaport"); err == nil {
		t.Error("databaseName accepted an invalid DSN")
	}
	if _, err := databaseName("host=127.0.0.1 user=silo"); err == nil {
		t.Error("databaseName accepted a DSN that names no database")
	}
}
