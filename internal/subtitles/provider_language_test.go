package subtitles

import (
	"slices"
	"testing"
)

func TestProviderLanguageKeepsUnknownAndOtherProviders(t *testing.T) {
	for _, tt := range []struct{ provider, value, want string }{{"subdl", "English", "en"}, {"subdl", " Arabic ", "ar"}, {"subdl", "BR_PT", "pt-BR"}, {"subdl", "Brazillian Portuguese", "pt-BR"}, {"subdl", "PT", "pt"}, {"subdl", "ZH_BG", "zh-Hant"}, {"subdl", "Unrecognized provider language", "Unrecognized provider language"}, {"upload", "English", "en"}, {"upload", "BR_PT", "br-PT"}, {"subdl", "", ""}} {
		if got := NormalizeProviderLanguage(tt.provider, tt.value); got != tt.want {
			t.Errorf("%s %q: got %q, want %q", tt.provider, tt.value, got, tt.want)
		}
	}
}
func TestSubDLLanguageCodeKeepsRegionalVariants(t *testing.T) {
	for _, tt := range []struct{ value, want string }{{"en", "EN"}, {"eng", "EN"}, {"pt", "PT"}, {"pt-BR", "BR_PT"}, {"pt_br", "BR_PT"}, {"pt-PT", "PT"}, {"zh-Hant", "ZH_BG"}, {"zh", "ZH"}, {"en-US", "EN"}, {"klingon", "KLINGON"}} {
		if got := SubDLLanguageCode(tt.value); got != tt.want {
			t.Errorf("%q: got %q, want %q", tt.value, got, tt.want)
		}
	}
	for _, want := range []string{"br_pt", "pt-br", "brazilian portuguese", "pob", "br"} {
		if !slices.Contains(SubDLLanguageAliases("pt-BR"), want) {
			t.Errorf("pt-BR aliases missing %q: %v", want, SubDLLanguageAliases("pt-BR"))
		}
	}
	if slices.Contains(SubDLLanguageAliases("pt"), "brazilian portuguese") {
		t.Errorf("pt aliases include the Brazilian variant: %v", SubDLLanguageAliases("pt"))
	}
}

func TestNormalizeLanguageCodeKeepsDistinctVariants(t *testing.T) {
	for _, tt := range []struct{ value, want string }{{"en", "en"}, {"eng", "en"}, {"EN-us", "en-US"}, {"pt", "pt"}, {"por", "pt"}, {"pt-BR", "pt-BR"}, {"pt_BR", "pt-BR"}, {"pt-PT", "pt-PT"}, {"zh-Hant", "zh-Hant"}, {"zho", "zh"}, {"sr-Latn", "sr-Latn"}, {"fil", "fil"}} {
		got, err := NormalizeLanguageCode(tt.value)
		if err != nil || got != tt.want {
			t.Errorf("%q: got %q (%v), want %q", tt.value, got, err, tt.want)
		}
	}
	if _, err := NormalizeLanguageCode("Brazilian Portuguese"); err == nil {
		t.Error("display name accepted as a language code")
	}
}

func TestStoredSubDLLanguageReadDoesNotRewriteRow(t *testing.T) {
	pool := subtitleStorageDatabase(t)
	var id int
	err := pool.QueryRow(t.Context(), `INSERT INTO downloaded_subtitles(media_file_id,provider,language,format,release_name,s3_key,score,hearing_impaired,content_sha256) VALUES (7,'subdl','English','srt','synthetic','synthetic-key',0,false,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa') RETURNING id`).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewPgRepository(pool, nil)
	one, err := repo.GetDownloadedSubtitle(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if one.Language != "en" {
		t.Fatalf("single language = %q", one.Language)
	}
	rows, err := repo.ListDownloadedSubtitles(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Language != "en" {
		t.Fatalf("list: %+v", rows)
	}
	matched, err := repo.GetDownloadedSubtitleByContent(t.Context(), &DownloadedSubtitle{MediaFileID: 7, Provider: "subdl", Language: "en", Format: "srt", ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if err != nil || matched == nil || matched.ID != id || matched.Language != "en" {
		t.Fatalf("canonical content lookup: %+v, %v", matched, err)
	}
	var stored string
	var revision int64
	if err := pool.QueryRow(t.Context(), `SELECT language,revision FROM downloaded_subtitles WHERE id=$1`, id).Scan(&stored, &revision); err != nil {
		t.Fatal(err)
	}
	if stored != "English" || revision != one.Revision {
		t.Fatalf("read changed stored identity: %s/%d", stored, revision)
	}
}
