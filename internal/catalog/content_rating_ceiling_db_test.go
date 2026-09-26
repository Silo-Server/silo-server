package catalog

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
)

// TestContentRatingCeilingSQLShape pins the two predicate forms the
// access.unrated_content setting selects between. Both compare the stored
// minimum age; they differ only in what happens to a title that has none.
func TestContentRatingCeilingSQLShape(t *testing.T) {
	if got, want := contentRatingCeilingSQL("mi", false, 4),
		"(mi.content_rating_age IS NOT NULL AND mi.content_rating_age <= $4)"; got != want {
		t.Errorf("hide predicate = %q, want %q", got, want)
	}
	if got, want := contentRatingCeilingSQL("ece", true, 2),
		"(ece.content_rating_age IS NULL OR ece.content_rating_age <= $2)"; got != want {
		t.Errorf("allow predicate = %q, want %q", got, want)
	}
}

// TestApplyMaturityLimitsBindsTheCeilingAge checks the three cases the
// helper distinguishes: no ceiling, an unusable ceiling, and a real one.
func TestApplyMaturityLimitsBindsTheCeilingAge(t *testing.T) {
	t.Run("no ceiling emits no predicate", func(t *testing.T) {
		var conditions []string
		var args []any
		argIdx := 1
		ApplyMaturityLimits("mi", AccessFilter{}, &conditions, &args, &argIdx)
		if len(conditions) != 0 || len(args) != 0 || argIdx != 1 {
			t.Fatalf("expected nothing emitted; got %v / %v / %d", conditions, args, argIdx)
		}
	})

	t.Run("unusable ceiling fails closed", func(t *testing.T) {
		var conditions []string
		var args []any
		argIdx := 1
		// "NR" is recognized but carries no age, so it cannot order anything.
		ApplyMaturityLimits("mi", AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: "NR"}}, &conditions, &args, &argIdx)
		if len(conditions) != 1 || conditions[0] != "1 = 0" {
			t.Fatalf("expected a blocking predicate; got %v", conditions)
		}
		if len(args) != 0 {
			t.Fatalf("a blocking predicate binds nothing; got %v", args)
		}
	})

	t.Run("whitespace-only ceiling fails closed", func(t *testing.T) {
		// max_content_rating is free text on both profile APIs, so a stored
		// " " is reachable. It is a set ceiling nothing resolves under, and
		// must block rather than lift the profile's parental control.
		for _, ceiling := range []string{" ", "\t", "\u00A0"} {
			var conditions []string
			var args []any
			argIdx := 1
			ApplyMaturityLimits("mi", AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: ceiling}}, &conditions, &args, &argIdx)
			if len(conditions) != 1 || conditions[0] != "1 = 0" {
				t.Fatalf("ceiling %q: expected a blocking predicate; got %v", ceiling, conditions)
			}
		}
	})

	t.Run("non-US ceiling resolves through its age", func(t *testing.T) {
		var conditions []string
		var args []any
		argIdx := 7
		ApplyMaturityLimits("mi", AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: "FSK 16"}}, &conditions, &args, &argIdx)
		if len(args) != 1 || args[0] != 16 {
			t.Fatalf("expected the FSK 16 ceiling to bind age 16; got %v", args)
		}
		if !strings.Contains(conditions[0], "$7") {
			t.Fatalf("expected the age bound at $7; got %v", conditions)
		}
		if argIdx != 8 {
			t.Fatalf("argIdx = %d, want it advanced to 8", argIdx)
		}
	})
}

// TestContentRatingCeilingAcrossSystemsDB is the point of the age axis: a
// ceiling expressed in one country's system has to order a title certified in
// another's. It also pins what access.unrated_content decides — a title whose
// rating carries no age is hidden by default and shown when the setting allows
// it — against a real database rather than a rendered string.
func TestContentRatingCeilingAcrossSystemsDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	prefix := fmt.Sprintf("rating-ceiling-%d", time.Now().UnixNano())
	var library int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, prefix,
	).Scan(&library); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id=$1`, library)
	})

	// Every row is written the way the Go write path writes one: the verbatim
	// provider string in content_rating and access.StoredRating in
	// content_rating_age, which is NULL for no rating and
	// access.UnrecognizedRatingAge for text no ladder reads.
	items := []struct {
		id     string
		rating string
		age    *int
	}{
		{prefix + "-fsk16", "FSK 16", ageOf(16)},
		{prefix + "-bbfc12a", "12A", ageOf(12)},
		{prefix + "-pg13", "PG-13", ageOf(13)},
		{prefix + "-tv14", "TV-14", ageOf(14)},
		{prefix + "-unrated", "NR", nil},
		{prefix + "-junk", "Contains mild peril", ageOf(access.UnrecognizedRatingAge)},
	}
	for _, item := range items {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items(content_id,type,title,status,genres,content_rating,content_rating_age)
			VALUES($1,'movie',$1,'released','{}',$2,$3)`, item.id, item.rating, item.age); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, item.id, library,
		); err != nil {
			t.Fatal(err)
		}
	}

	all := make([]string, 0, len(items))
	for _, item := range items {
		all = append(all, item.id)
	}
	repo := NewLibraryItemRepository(pool)
	visible := func(t *testing.T, ceiling string, allowUnrated bool) map[string]bool {
		t.Helper()
		got, err := repo.FilterAccessibleContentIDs(ctx, all, []int{library}, nil, access.MaturityLimits{MaxContentRating: ceiling, AllowUnratedContent: allowUnrated})
		if err != nil {
			t.Fatalf("FilterAccessibleContentIDs(%q): %v", ceiling, err)
		}
		return got
	}

	t.Run("a PG-13 ceiling hides an FSK 16 title", func(t *testing.T) {
		got := visible(t, "PG-13", false)
		if got[prefix+"-fsk16"] {
			t.Error("FSK 16 (age 16) must not pass a PG-13 ceiling (age 14, its US tier)")
		}
		if !got[prefix+"-bbfc12a"] {
			t.Error("12A (age 12) must pass a PG-13 ceiling")
		}
		if !got[prefix+"-pg13"] {
			t.Error("a ceiling must admit its own rating")
		}
		if !got[prefix+"-tv14"] {
			t.Error("a PG-13 ceiling must admit TV-14, the other rating in its US tier")
		}
	})

	t.Run("an R ceiling shows the FSK 16 title", func(t *testing.T) {
		if got := visible(t, "R", false); !got[prefix+"-fsk16"] {
			t.Error("FSK 16 (age 16) must pass an R ceiling (age 18, its US tier)")
		}
	})

	t.Run("a ceiling in another system orders US ratings", func(t *testing.T) {
		// The mirror image of the case above: an "FSK 16" ceiling used to match
		// no stored string at all and hid the whole catalog.
		got := visible(t, "FSK 16", false)
		if !got[prefix+"-pg13"] {
			t.Error("PG-13 (age 13) must pass an FSK 16 ceiling")
		}
		if !got[prefix+"-fsk16"] {
			t.Error("FSK 16 must pass its own ceiling")
		}
	})

	t.Run("unrated follows the setting", func(t *testing.T) {
		hidden := visible(t, "PG-13", false)
		if hidden[prefix+"-unrated"] || hidden[prefix+"-junk"] {
			t.Error("a title with no resolvable age must be hidden by default")
		}
		allowed := visible(t, "PG-13", true)
		if !allowed[prefix+"-unrated"] {
			t.Error("access.unrated_content=allow must show titles with no rating")
		}
		if allowed[prefix+"-junk"] {
			t.Error("access.unrated_content=allow must not show a title whose rating is unrecognized")
		}
		if allowed[prefix+"-fsk16"] {
			t.Error("allowing unrated titles must not raise the ceiling itself")
		}
	})

	t.Run("no ceiling shows everything", func(t *testing.T) {
		got := visible(t, "", false)
		for _, item := range items {
			if !got[item.id] {
				t.Errorf("%s (%q) hidden without a ceiling", item.id, item.rating)
			}
		}
	})

	t.Run("a whitespace ceiling shows nothing", func(t *testing.T) {
		// This is the path the progress list and sync take, so a stored " "
		// reading as "no ceiling" here would let a restricted viewer read and
		// write progress for titles browse hides. It must agree with
		// ApplyMaturityLimits and block, however the setting is set.
		for _, allowUnrated := range []bool{false, true} {
			got := visible(t, " ", allowUnrated)
			if len(got) != 0 {
				t.Errorf("allow_unrated=%v: a whitespace ceiling admitted %d titles, want none", allowUnrated, len(got))
			}
		}
	})
}

func ageOf(age int) *int { return &age }
