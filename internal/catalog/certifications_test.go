package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/certification"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLibraryCertificationAccessDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	id := fmt.Sprintf("movie:certification-%d", time.Now().UnixNano())
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var au, us int
	for country, dest := range map[string]*int{"AU": &au, "US": &us} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,certification_country) VALUES('movies',$1,$1) RETURNING id`, country).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id=$1`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id=ANY($1)`, []int{au, us})
	}()
	exec(`INSERT INTO media_items(content_id,type,title,content_rating,tmdb_id) VALUES($1,'movie','Certification test','R','123')`, id)
	exec(`INSERT INTO media_item_libraries(content_id,media_folder_id,first_seen_at) VALUES($1,$2,now()),($1,$3,now())`, id, au, us)
	exec(`INSERT INTO media_item_certifications(content_id,tmdb_id,ratings) VALUES($1,'123','{"AU":"M","US":"R"}')`, id)
	repo := NewItemRepository(pool)
	check := func(name string, filter AccessFilter, want bool) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			if got := repo.EnsureAccessible(ctx, id, filter); (got == nil) != want {
				t.Fatalf("access error=%v, want allowed=%v", got, want)
			}
			query, args := repo.buildGetByIDsWithAccessSQL([]string{id}, filter)
			rows, err := pool.Query(ctx, query, args...)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			if rows.Next() != want {
				t.Fatal("listing and direct access disagree")
			}
		})
	}
	check("AU official rating allows M", AccessFilter{AllowedLibraryIDs: []int{au}, MaxContentRating: "AU-M"}, true)
	check("AU official rating blocks PG", AccessFilter{AllowedLibraryIDs: []int{au}, MaxContentRating: "AU-PG"}, false)
	browse := NewBrowseRepository(pool)
	facets, err := browse.ListContentRatings(ctx, BrowseFilters{LibraryID: au, LibraryIDs: []int{au}})
	if err != nil || !slices.Equal(facets, []string{"AU-M"}) {
		t.Fatalf("local rating facets: %v %v", facets, err)
	}
	result, err := browse.Browse(ctx, BrowseFilters{LibraryID: au, LibraryIDs: []int{au}, ContentRating: []string{"AU-M"}, Limit: 10})
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("local rating filter: %v %v", result, err)
	}

	executor := &QueryExecutor{Pool: pool}
	definition := QueryDefinition{LibraryIDs: []int{au}, MediaScope: "movie", Groups: []QueryGroup{{Rules: []QueryRule{{Field: "content_rating", Op: "is", Value: "AU-M"}}}}, Sort: QuerySort{Field: "content_rating", Order: "asc"}}
	matched, _, err := executor.Preview(ctx, definition, AccessFilter{AllowedLibraryIDs: []int{au}}, 10)
	if err != nil || len(matched) != 1 {
		t.Fatalf("advanced local rating filter and sort: %v %v", matched, err)
	}
	check("US keeps R", AccessFilter{AllowedLibraryIDs: []int{us}, MaxContentRating: "PG-13"}, false)
	check("shared access chooses strictest", AccessFilter{AllowedLibraryIDs: []int{au, us}, MaxContentRating: "AU-M", PresentationLibraryID: &au}, false)
	svc := &DetailService{itemRepo: repo}
	item, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	shown, err := svc.applyCertifications(ctx, []*models.MediaItem{item}, AccessFilter{PresentationLibraryID: &au})
	if err != nil || shown[0].ContentRating != "AU-M" || shown[0].Certification.Equivalent {
		t.Fatalf("official presentation: %v %v", shown, err)
	}
	exec(`UPDATE media_item_certifications SET ratings='{"US":"PG-13"}' WHERE content_id=$1`, id)
	shown, err = svc.applyCertifications(ctx, []*models.MediaItem{item}, AccessFilter{PresentationLibraryID: &au})
	if err != nil || shown[0].ContentRating != "AU-M" || !shown[0].Certification.Equivalent || shown[0].Certification.SourceRating != "PG-13" {
		t.Fatalf("fallback presentation: %v %v", shown, err)
	}
	check("fallback permits mapped M", AccessFilter{AllowedLibraryIDs: []int{au}, MaxContentRating: "AU-M"}, true)
	exec(`UPDATE media_item_certifications SET ratings='{"US":"TV-MA"}' WHERE content_id=$1`, id)
	check("adult fallback blocks MA15", AccessFilter{AllowedLibraryIDs: []int{au}, MaxContentRating: "AU-MA15+"}, false)
	exec(`UPDATE media_items SET locked_fields=ARRAY[9] WHERE content_id=$1`, id)
	exec(`UPDATE media_item_certifications SET ratings='{"AU":"G"}' WHERE content_id=$1`, id)
	check("lock preserves stricter override", AccessFilter{AllowedLibraryIDs: []int{au}, MaxContentRating: "AU-M"}, false)
	exec(`UPDATE media_items SET locked_fields='{}',tmdb_id='456' WHERE content_id=$1`, id)
	check("reidentified item cannot reuse old AU rating", AccessFilter{AllowedLibraryIDs: []int{au}, MaxContentRating: "AU-M"}, false)
	exec(`UPDATE media_items SET content_rating='' WHERE content_id=$1`, id)
	check("unknown is blocked", AccessFilter{AllowedLibraryIDs: []int{au}, MaxContentRating: "AU-R18+"}, false)
}

func TestCertificationSQLMatchesGoDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, country := range []string{"US", "AU"} {
		for _, base := range []string{"", "G", "PG", "PG-13", "R", "NC-17", "TV-Y", "TV-Y7", "TV-PG", "TV-14", "TV-MA", "NR", "DE-12", "AU-M", "au: ma 15+ ", "AU:", "US-PG", " tv-14 "} {
			for _, local := range []string{"", "PG", "M", "MA15+", "R18+", "CTC", "au:ma", " r18 "} {
				for _, locked := range []bool{false, true} {
					ratings := map[string]string{"AU": local}
					data, _ := json.Marshal(ratings)
					var got string
					if err := pool.QueryRow(ctx, `SELECT silo_certification_rating($1,$2,$3::jsonb,$4)`, base, country, data, locked).Scan(&got); err != nil {
						t.Fatal(err)
					}
					want := certification.ResolveCertification(country, base, ratings, locked).Token()
					if got != want {
						t.Fatalf("country=%s base=%s local=%s lock=%v: SQL=%s Go=%s", country, base, local, locked, got, want)
					}
				}
			}
		}
	}
}
