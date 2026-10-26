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
