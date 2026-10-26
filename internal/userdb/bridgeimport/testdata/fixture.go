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
