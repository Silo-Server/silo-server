package catalog

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
)

func TestSeriesOriginalLanguagesUsesSuppliedParentContext(t *testing.T) {
	svc := &DetailService{}
	got, err := svc.seriesOriginalLanguages(
		context.Background(),
		[]string{"series-a", "series-a", "", "series-b"},
		AccessFilter{PresentationOriginalLanguage: "no"},
	)
	if err != nil {
		t.Fatalf("seriesOriginalLanguages: %v", err)
	}
	want := map[string]string{"series-a": "no", "series-b": "no"}
	if len(got) != len(want) {
		t.Fatalf("original languages = %#v, want %#v", got, want)
	}
	for seriesID, language := range want {
		if got[seriesID] != language {
			t.Errorf("original language for %q = %q, want %q", seriesID, got[seriesID], language)
		}
	}
}

func TestPresentationLanguageForOriginal(t *testing.T) {
	tests := []struct {
		name     string
		base     presentationLanguageBase
		original string
		filter   AccessFilter
		want     string
	}{
		{
			name:     "fixed fallback",
			base:     presentationLanguageBase{target: "en"},
			original: "ja",
			want:     "en",
		},
		{
			name:     "source stays original",
			base:     presentationLanguageBase{target: "en"},
			original: "nor",
			filter: AccessFilter{MetadataLanguageOverrides: map[string]string{
				"no": access.OriginalMetadataLanguage,
			}},
			want: "no",
		},
		{
			name:     "source maps to another target",
			base:     presentationLanguageBase{target: "en"},
			original: "ja",
			filter: AccessFilter{MetadataLanguageOverrides: map[string]string{
				"ja": "de",
			}},
			want: "de",
		},
		{
			name:     "global original",
			base:     presentationLanguageBase{target: access.OriginalMetadataLanguage},
			original: "jpn",
			want:     "ja",
		},
		{
			name: "unknown original inherits library",
			base: presentationLanguageBase{
				target:          access.OriginalMetadataLanguage,
				libraryFallback: "fr",
			},
			want: "fr",
		},
		{
			name:     "explicit target bypasses exceptions",
			base:     presentationLanguageBase{target: "fr", explicit: true},
			original: "no",
			filter: AccessFilter{MetadataLanguageOverrides: map[string]string{
				"no": access.OriginalMetadataLanguage,
			}},
			want: "fr",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := presentationLanguageForOriginal(tt.base, tt.original, tt.filter); got != tt.want {
				t.Fatalf("presentation language = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMetadataLanguageMayUseOriginalHonorsExplicitTarget(t *testing.T) {
	filter := AccessFilter{
		PresentationLanguage:      "fr",
		ProfilePreferredLanguage:  access.OriginalMetadataLanguage,
		MetadataLanguageOverrides: map[string]string{"no": access.OriginalMetadataLanguage},
	}
	if metadataLanguageMayUseOriginal(filter) {
		t.Fatal("concrete explicit language must bypass profile original-language preferences")
	}

	filter.PresentationLanguage = access.OriginalMetadataLanguage
	if !metadataLanguageMayUseOriginal(filter) {
		t.Fatal("explicit original-language target must resolve the parent language")
	}
}
