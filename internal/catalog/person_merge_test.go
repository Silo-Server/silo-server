package catalog

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestConflictingExternalID(t *testing.T) {
	p := models.Person{TmdbID: "111", ImdbID: "nm222"}

	tests := []struct {
		name       string
		constraint string
		wantField  string
		wantValue  string
		wantOK     bool
	}{
		{"tmdb", "idx_people_tmdb_id", "tmdb_id", "111", true},
		{"imdb", "idx_people_imdb_id", "imdb_id", "nm222", true},
		{"unknown constraint", "idx_people_name", "", "", false},
		{"empty constraint", "", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			field, value, ok := conflictingExternalID(tt.constraint, p)
			if field != tt.wantField || value != tt.wantValue || ok != tt.wantOK {
				t.Fatalf("conflictingExternalID(%q) = (%q,%q,%v), want (%q,%q,%v)",
					tt.constraint, field, value, ok, tt.wantField, tt.wantValue, tt.wantOK)
			}
		})
	}

	// A blank id must never be reported as the conflict source.
	if _, _, ok := conflictingExternalID("idx_people_tmdb_id", models.Person{}); ok {
		t.Fatal("expected ok=false when the conflicting field is empty")
	}
}

func TestExternalIDsCompatible(t *testing.T) {
	tests := []struct {
		name string
		a, b models.Person
		want bool
	}{
		{
			name: "tmdb-only vs imdb-only (the core dedup case)",
			a:    models.Person{TmdbID: "111", ImdbID: "nm9"},
			b:    models.Person{ImdbID: "nm9"},
			want: true,
		},
		{
			name: "identical ids",
			a:    models.Person{TmdbID: "111", ImdbID: "nm9"},
			b:    models.Person{TmdbID: "111", ImdbID: "nm9"},
			want: true,
		},
		{
			name: "conflicting tmdb ids",
			a:    models.Person{TmdbID: "111", ImdbID: "nm9"},
			b:    models.Person{TmdbID: "999", ImdbID: "nm9"},
			want: false,
		},
		{
			name: "conflicting tvdb ids",
			a:    models.Person{ImdbID: "nm9", TvdbID: "5"},
			b:    models.Person{ImdbID: "nm9", TvdbID: "6"},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := externalIDsCompatible(tt.a, tt.b); got != tt.want {
				t.Fatalf("externalIDsCompatible = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCanMergePeople(t *testing.T) {
	tests := []struct {
		name string
		a, b models.Person
		want bool
	}{
		{
			name: "same name, compatible ids -> merge",
			a:    models.Person{Name: "Jane Doe", TmdbID: "111"},
			b:    models.Person{Name: "Jane Doe", ImdbID: "nm9"},
			want: true,
		},
		{
			name: "case/space-insensitive name match -> merge",
			a:    models.Person{Name: "  jane doe ", TmdbID: "111"},
			b:    models.Person{Name: "Jane Doe", ImdbID: "nm9"},
			want: true,
		},
		{
			name: "different names, compatible ids -> do not merge",
			a:    models.Person{Name: "Jane Doe", TmdbID: "111"},
			b:    models.Person{Name: "John Smith", ImdbID: "nm9"},
			want: false,
		},
		{
			name: "same name but contradictory ids -> do not merge",
			a:    models.Person{Name: "Jane Doe", TmdbID: "111"},
			b:    models.Person{Name: "Jane Doe", TmdbID: "999"},
			want: false,
		},
		{
			name: "empty name on one side -> do not merge",
			a:    models.Person{Name: "", TmdbID: "111"},
			b:    models.Person{Name: "Jane Doe", ImdbID: "nm9"},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canMergePeople(tt.a, tt.b); got != tt.want {
				t.Fatalf("canMergePeople = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMergePersonFields(t *testing.T) {
	birth := time.Date(1980, 1, 2, 0, 0, 0, 0, time.UTC)

	t.Run("backfills only empty fields, survivor wins", func(t *testing.T) {
		dst := models.Person{
			Name:   "Jane Doe",
			TmdbID: "111",
			Bio:    "survivor bio",
		}
		src := models.Person{
			Name:       "Janey",
			TmdbID:     "999", // must NOT overwrite a populated survivor id
			ImdbID:     "nm9", // fills the gap
			Bio:        "partner bio",
			Birthplace: "Springfield",
			BirthDate:  &birth,
			PhotoPath:  "/p.jpg",
		}
		mergePersonFields(&dst, src)

		if dst.Name != "Jane Doe" {
			t.Errorf("Name overwritten: %q", dst.Name)
		}
		if dst.TmdbID != "111" {
			t.Errorf("populated TmdbID overwritten: %q", dst.TmdbID)
		}
		if dst.ImdbID != "nm9" {
			t.Errorf("empty ImdbID not backfilled: %q", dst.ImdbID)
		}
		if dst.Bio != "survivor bio" {
			t.Errorf("populated Bio overwritten: %q", dst.Bio)
		}
		if dst.Birthplace != "Springfield" {
			t.Errorf("empty Birthplace not backfilled: %q", dst.Birthplace)
		}
		if dst.BirthDate == nil || !dst.BirthDate.Equal(birth) {
			t.Errorf("nil BirthDate not backfilled: %v", dst.BirthDate)
		}
		if dst.PhotoPath != "/p.jpg" {
			t.Errorf("empty PhotoPath not backfilled: %q", dst.PhotoPath)
		}
	})

	t.Run("backfills name when survivor is blank", func(t *testing.T) {
		dst := models.Person{TmdbID: "111"}
		src := models.Person{Name: "Jane Doe"}
		mergePersonFields(&dst, src)
		if dst.Name != "Jane Doe" {
			t.Errorf("blank survivor Name not backfilled: %q", dst.Name)
		}
	})

	t.Run("photo trio is folded together", func(t *testing.T) {
		dst := models.Person{} // no photo at all
		src := models.Person{
			PhotoPath:       "/p.jpg",
			PhotoSourcePath: "tmdb://p",
			PhotoThumbhash:  "hash",
		}
		mergePersonFields(&dst, src)
		if dst.PhotoPath != "/p.jpg" || dst.PhotoSourcePath != "tmdb://p" || dst.PhotoThumbhash != "hash" {
			t.Errorf("photo trio not folded together: %+v", dst)
		}

		// A survivor that already has a photo keeps its own trio intact.
		keep := models.Person{PhotoPath: "/keep.jpg"}
		mergePersonFields(&keep, src)
		if keep.PhotoPath != "/keep.jpg" || keep.PhotoSourcePath != "" || keep.PhotoThumbhash != "" {
			t.Errorf("survivor photo trio was disturbed: %+v", keep)
		}
	})
}

func TestExternalIDValueAndSet(t *testing.T) {
	p := models.Person{TmdbID: "111", ImdbID: "nm9", TvdbID: "5", PlexGUID: "g"}

	for field, want := range map[string]string{
		"tmdb_id": "111", "imdb_id": "nm9", "tvdb_id": "5", "plex_guid": "g", "nope": "",
	} {
		if got := externalIDValue(p, field); got != want {
			t.Errorf("externalIDValue(%q) = %q, want %q", field, got, want)
		}
	}

	// Setting a field to a new value replaces only that column.
	updated := p
	setExternalIDField(&updated, "imdb_id", "nm12345")
	if updated.ImdbID != "nm12345" {
		t.Errorf("setExternalIDField did not set imdb_id: %q", updated.ImdbID)
	}
	if updated.TmdbID != "111" {
		t.Errorf("setExternalIDField touched the wrong field: %q", updated.TmdbID)
	}

	// Setting a field to "" blanks it, matching the old clear behavior.
	cleared := p
	setExternalIDField(&cleared, "imdb_id", "")
	if cleared.ImdbID != "" {
		t.Errorf("setExternalIDField did not blank imdb_id: %q", cleared.ImdbID)
	}
	if cleared.TmdbID != "111" {
		t.Errorf("setExternalIDField touched the wrong field: %q", cleared.TmdbID)
	}
}
