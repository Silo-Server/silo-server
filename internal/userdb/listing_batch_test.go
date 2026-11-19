package userdb

import (
	"testing"
)

func TestBatchAttachDoesNotScaleBindParametersWithRows(t *testing.T) {
	db := newSingleConnSchemaDB(t)
	const aboveSQLiteVariableLimit = 32767

	profiles := make([]Profile, aboveSQLiteVariableLimit)
	if err := attachAllowedLibraries(db, profiles); err != nil {
		t.Fatalf("attachAllowedLibraries: %v", err)
	}
}
