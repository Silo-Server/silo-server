package catalog

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type detailLocalizationTracer struct {
	folders, folderPaths, localizations int
}

func (tracer *detailLocalizationTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	switch {
	case strings.Contains(data.SQL, "FROM media_item_localizations"):
		tracer.localizations++
	case strings.Contains(data.SQL, "FROM media_folders"):
		tracer.folders++
	case strings.Contains(data.SQL, "FROM media_folder_paths"):
		tracer.folderPaths++
	}
	return ctx
}

func (*detailLocalizationTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func detailLocalizationFixture(t testing.TB) (*DetailService, *pgxpool.Pool, *detailLocalizationTracer) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	tracer := &detailLocalizationTracer{}
	config.ConnConfig.Tracer = tracer
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	// Session-local fixtures exercise real repository SQL without adding
	// catalog entries or changing the shared migrated schema.
	_, err = pool.Exec(t.Context(), `
		CREATE TEMP TABLE media_item_localizations (LIKE public.media_item_localizations INCLUDING DEFAULTS INCLUDING INDEXES);
		CREATE TEMP TABLE media_folders (LIKE public.media_folders INCLUDING DEFAULTS);
		CREATE TEMP TABLE media_folder_paths (LIKE public.media_folder_paths INCLUDING DEFAULTS);
		INSERT INTO media_folders (id, type, name, metadata_language)
		VALUES (1, 'series', 'French', 'fr'), (2, 'series', 'English', 'en');
	`)
	if err != nil {
		t.Fatal(err)
	}
	localizations := NewMediaItemLocalizationRepository(pool)
	for _, row := range []*models.MediaItemLocalization{
		{ContentID: "translated", Language: "fr", Title: "Titre", Overview: "Description"},
		{ContentID: "translated", Language: "no", Title: "Tittel", Overview: "Beskrivelse"},
		{ContentID: "partial", Language: "fr", Title: "Partial title"},
	} {
		if err := localizations.Upsert(t.Context(), row); err != nil {
			t.Fatal(err)
		}
	}
	return &DetailService{itemLocRepo: localizations, folderRepo: NewFolderRepository(pool)}, pool, tracer
}

func localizationDetailItem(id, overview string) *models.MediaItem {
	return &models.MediaItem{
		ContentID: id, Type: "series", Title: "Base title", Overview: overview,
		DefaultMetadataLanguage: "en", OriginalLanguage: "no",
	}
}

func TestBuildMediaItemDetailSharesLocalization(t *testing.T) {
	service, _, tracer := detailLocalizationFixture(t)
	for _, tc := range []struct {
		name, id, overview, original, title, localizedOverview, pending string
		filter                                                          AccessFilter
		folders, localizations                                          int
		noRepo                                                          bool
	}{
		{name: "translated", id: "translated", overview: "Base overview", original: "no", title: "Titre", localizedOverview: "Description", filter: AccessFilter{PresentationLibraryID: new(1)}, folders: 1, localizations: 1},
		{name: "base language", id: "translated", overview: "Base overview", original: "no", title: "Base title", localizedOverview: "Base overview", filter: AccessFilter{PresentationLibraryID: new(2)}, folders: 1},
		{name: "base profile language needs no database", id: "translated", overview: "Base overview", original: "no", title: "Base title", localizedOverview: "Base overview", filter: AccessFilter{ProfilePreferredLanguage: "en"}},
		{name: "missing localization", id: "missing", overview: "Base overview", title: "Base title", localizedOverview: "Base overview", pending: "fr", filter: AccessFilter{PresentationLibraryID: new(1)}, folders: 1, localizations: 1},
		{name: "partial localization", id: "partial", overview: "Base overview", title: "Partial title", localizedOverview: "Base overview", pending: "fr", filter: AccessFilter{PresentationLibraryID: new(1)}, folders: 1, localizations: 1},
		{name: "blank overview suppresses pending", id: "missing", overview: "  ", title: "Base title", localizedOverview: "  ", filter: AccessFilter{PresentationLibraryID: new(1)}, folders: 1, localizations: 1},
		{name: "missing repository suppresses pending", id: "missing", overview: "Base overview", title: "Base title", localizedOverview: "Base overview", filter: AccessFilter{PresentationLibraryID: new(1)}, folders: 1, noRepo: true},
		{name: "original language", id: "translated", overview: "Base overview", original: "nor", title: "Tittel", localizedOverview: "Beskrivelse", filter: AccessFilter{PresentationLibraryID: new(1), ProfilePreferredLanguage: access.OriginalMetadataLanguage}, folders: 1, localizations: 1},
		{name: "unknown original uses library", id: "translated", overview: "Base overview", title: "Titre", localizedOverview: "Description", filter: AccessFilter{PresentationLibraryID: new(1), ProfilePreferredLanguage: access.OriginalMetadataLanguage}, folders: 1, localizations: 1},
		{name: "source exception", id: "translated", overview: "Base overview", original: "nor", title: "Tittel", localizedOverview: "Beskrivelse", filter: AccessFilter{ProfilePreferredLanguage: "fr", MetadataLanguageOverrides: map[string]string{"no": access.OriginalMetadataLanguage}}, localizations: 1},
		{name: "explicit language bypasses exception", id: "translated", overview: "Base overview", original: "no", title: "Titre", localizedOverview: "Description", filter: AccessFilter{PresentationLanguage: "fr", MetadataLanguageOverrides: map[string]string{"no": access.OriginalMetadataLanguage}}, localizations: 1},
		{name: "no target language", id: "translated", overview: "Base overview", title: "Base title", localizedOverview: "Base overview"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := localizationDetailItem(tc.id, tc.overview)
			item.OriginalLanguage = tc.original
			original := *item
			repo := service.itemLocRepo
			if tc.noRepo {
				service.itemLocRepo = nil
			}
			defer func() { service.itemLocRepo = repo }()
			*tracer = detailLocalizationTracer{}
			detail, err := service.buildMediaItemDetail(t.Context(), item, item.ContentID, tc.filter, nil)
			if err != nil {
				t.Fatal(err)
			}
			if detail.Title != tc.title || detail.Overview != tc.localizedOverview || detail.PendingTranslationLanguage != tc.pending {
				t.Fatalf("localized fields = (%q, %q, %q), want (%q, %q, %q)", detail.Title, detail.Overview, detail.PendingTranslationLanguage, tc.title, tc.localizedOverview, tc.pending)
			}
			if tracer.folders != tc.folders || tracer.folderPaths != tc.folders || tracer.localizations != tc.localizations {
				t.Fatalf("reads = %#v, want %d folders/paths and %d localizations", tracer, tc.folders, tc.localizations)
			}
			if !reflect.DeepEqual(*item, original) {
				t.Fatal("detail construction mutated the source item")
			}
		})
	}
}
