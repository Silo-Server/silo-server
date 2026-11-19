package metadata

import (
	"testing"
)

// FieldReleaseDates locks Year and the release/air dates against refreshes.
func TestMerge_ReleaseDatesLockBlocksReplace(t *testing.T) {
	t.Parallel()

	source := &MetadataResult{
		HasMetadata:         true,
		Year:                1982,
		ReleaseDate:         "1982-06-25",
		DigitalReleaseDate:  "1999-10-26",
		PhysicalReleaseDate: "1992-03-11",
		FirstAirDate:        "1982-06-25",
		LastAirDate:         "1983-01-01",
	}
	target := &MetadataResult{
		Year:                1990,
		ReleaseDate:         "1990-01-01",
		DigitalReleaseDate:  "2001-01-01",
		PhysicalReleaseDate: "1995-01-01",
		FirstAirDate:        "1990-01-01",
		LastAirDate:         "1991-01-01",
	}

	// A scheduled refresh fills only empty fields, but a stored item still
	// takes the providers' current home release dates unless they are locked.
	filled := *target
	MergeMetadata(source, &filled, nil, MergeFillEmpty)
	refreshHomeReleaseDates(&filled, source, nil)
	if filled.ReleaseDate != "1990-01-01" || filled.DigitalReleaseDate != "1999-10-26" || filled.PhysicalReleaseDate != "1992-03-11" {
		t.Errorf("fill-empty refresh: %+v", filled)
	}
	locked := *target
	refreshHomeReleaseDates(&locked, source, []MetadataField{FieldReleaseDates})
	if locked.DigitalReleaseDate != "2001-01-01" || locked.PhysicalReleaseDate != "1995-01-01" {
		t.Errorf("locked refresh replaced home release dates: %+v", locked)
	}
	// A refresh keeps a date the provider stopped sending.
	undated := &MetadataResult{HasMetadata: true}
	refreshed := *target
	refreshHomeReleaseDates(&refreshed, undated, nil)
	if refreshed.DigitalReleaseDate != "2001-01-01" || refreshed.PhysicalReleaseDate != "1995-01-01" {
		t.Errorf("refresh dropped stored home release dates: %+v", refreshed)
	}

	MergeMetadata(source, target, []MetadataField{FieldReleaseDates}, MergeReplaceUnlocked)

	if target.Year != 1990 || target.ReleaseDate != "1990-01-01" || target.DigitalReleaseDate != "2001-01-01" || target.PhysicalReleaseDate != "1995-01-01" ||
		target.FirstAirDate != "1990-01-01" || target.LastAirDate != "1991-01-01" {
		t.Errorf("locked dates overwritten: %+v", target)
	}

	// Unlocked, replace mode applies the source values.
	MergeMetadata(source, target, nil, MergeReplaceUnlocked)
	if target.Year != 1982 || target.ReleaseDate != "1982-06-25" || target.DigitalReleaseDate != "1999-10-26" || target.PhysicalReleaseDate != "1992-03-11" {
		t.Errorf("unlocked replace did not apply: %+v", target)
	}
}
