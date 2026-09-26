// Package testdata builds the frozen bridge source, independently of current migrations.
package testdata

import (
	"database/sql"
	_ "embed"

	"github.com/Silo-Server/silo-server/internal/userdb"
)

// Schema22 is sqlite_schema output from the schema22 NewUserDB constructor,
// including its indexes and triggers. It must not follow the current schema.
//
//go:embed schema22.sql
var Schema22 string

func NewSource(path string, userID int) (*userdb.UserDB, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?_journal_mode=WAL&_foreign_keys=ON")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(Schema22); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &userdb.UserDB{DB: db, Path: path, UserID: userID}, nil
}

// OpenSource reopens an existing fixture without initialization or migrations.
func OpenSource(path string, userID int) (*userdb.UserDB, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=rw&_journal_mode=WAL&_foreign_keys=ON")
	if err != nil {
		return nil, err
	}
	return &userdb.UserDB{DB: db, Path: path, UserID: userID}, nil
}

// Schema23Migration is the exact sink-table addition and version advance from
// 46df415bf. Combined with Schema22 it produces genuine historical schema23.
//
//go:embed schema23_migration.sql
var Schema23Migration string

func NewSource23(path string, userID int) (*userdb.UserDB, error) {
	source, err := NewSource(path, userID)
	if err != nil {
		return nil, err
	}
	if _, err := source.DB.Exec(Schema23Migration); err != nil {
		_ = source.Close()
		return nil, err
	}
	return source, nil
}

// Schema24Migration freezes the source-marker addition independently of current migrations.
//
//go:embed schema24_migration.sql
var Schema24Migration string

func NewSource24(path string, userID int) (*userdb.UserDB, error) {
	source, err := NewSource23(path, userID)
	if err != nil {
		return nil, err
	}
	if _, err := source.DB.Exec(Schema24Migration); err != nil {
		_ = source.Close()
		return nil, err
	}
	return source, nil
}
