package requests

import "testing"

func TestDetectAnime(t *testing.T) {
	const genreAnimation = 16

	t.Run("keyword_210024_detects_anime", func(t *testing.T) {
		if !detectAnime([]int{99, animeKeywordID, 7}, nil, "") {
			t.Fatal("expected anime when keyword 210024 present")
		}
	})

	t.Run("keyword_only_no_genre", func(t *testing.T) {
		if !detectAnime([]int{animeKeywordID}, nil, "") {
			t.Fatal("expected anime for keyword-only match")
		}
	})

	t.Run("keyword_with_genre_16", func(t *testing.T) {
		if !detectAnime([]int{animeKeywordID}, []int{genreAnimation}, "ja") {
			t.Fatal("expected anime when keyword and genre both present")
		}
	})

	t.Run("no_keyword_no_genre", func(t *testing.T) {
		if detectAnime([]int{99, 7}, nil, "") {
			t.Fatal("expected non-anime when keyword 210024 absent and no genre")
		}
	})

	t.Run("nil_keywords", func(t *testing.T) {
		if detectAnime(nil, nil, "") {
			t.Fatal("expected non-anime for nil keywords")
		}
	})

	t.Run("empty_keywords", func(t *testing.T) {
		if detectAnime([]int{}, nil, "") {
			t.Fatal("expected non-anime for empty keywords")
		}
	})

	t.Run("genre_16_ja_detects_anime", func(t *testing.T) {
		if !detectAnime(nil, []int{genreAnimation}, "ja") {
			t.Fatal("expected anime for genre 16 + language ja")
		}
	})

	t.Run("genre_16_zh_detects_anime", func(t *testing.T) {
		if !detectAnime(nil, []int{genreAnimation}, "zh") {
			t.Fatal("expected anime for genre 16 + language zh")
		}
	})

	t.Run("genre_16_ko_detects_anime", func(t *testing.T) {
		if !detectAnime(nil, []int{genreAnimation}, "ko") {
			t.Fatal("expected anime for genre 16 + language ko")
		}
	})

	t.Run("genre_16_en_western_animation_false", func(t *testing.T) {
		if detectAnime(nil, []int{genreAnimation}, "en") {
			t.Fatal("expected non-anime for genre 16 + language en")
		}
	})

	t.Run("genre_16_fr_western_animation_false", func(t *testing.T) {
		if detectAnime(nil, []int{genreAnimation}, "fr") {
			t.Fatal("expected non-anime for genre 16 + language fr")
		}
	})

	t.Run("ja_without_genre_16_false", func(t *testing.T) {
		if detectAnime(nil, []int{28}, "ja") {
			t.Fatal("expected non-anime for language ja without genre 16")
		}
	})

	t.Run("zh_without_genre_16_false", func(t *testing.T) {
		if detectAnime(nil, []int{18, 10759}, "zh") {
			t.Fatal("expected non-anime for language zh without genre 16")
		}
	})

	t.Run("genre_16_no_language_false", func(t *testing.T) {
		if detectAnime(nil, []int{genreAnimation}, "") {
			t.Fatal("expected non-anime for genre 16 with empty language")
		}
	})

	t.Run("language_whitespace_trimmed", func(t *testing.T) {
		if !detectAnime(nil, []int{genreAnimation}, "  ja  ") {
			t.Fatal("expected anime for genre 16 + language '  ja  ' (trimmed)")
		}
	})

	t.Run("language_uppercase_normalized", func(t *testing.T) {
		if !detectAnime(nil, []int{genreAnimation}, "JA") {
			t.Fatal("expected anime for genre 16 + language 'JA' (lowercased)")
		}
	})

	t.Run("language_mixed_case_whitespace", func(t *testing.T) {
		if !detectAnime(nil, []int{genreAnimation}, "  ZH  ") {
			t.Fatal("expected anime for genre 16 + language '  ZH  '")
		}
	})

	t.Run("nil_genre_ids", func(t *testing.T) {
		if detectAnime(nil, nil, "ja") {
			t.Fatal("expected non-anime for nil genre IDs with language ja")
		}
	})

	t.Run("empty_genre_ids", func(t *testing.T) {
		if detectAnime(nil, []int{}, "ja") {
			t.Fatal("expected non-anime for empty genre IDs with language ja")
		}
	})

	t.Run("gun_gunners_shape_zh_plus_16", func(t *testing.T) {
		if !detectAnime([]int{999}, []int{genreAnimation, 10759, 80}, "zh") {
			t.Fatal("expected anime for Gun-Girls-shaped detail: zh + genre 16")
		}
	})

	t.Run("multiple_genre_ids_with_16", func(t *testing.T) {
		if !detectAnime(nil, []int{28, genreAnimation, 12}, "ko") {
			t.Fatal("expected anime for genre 16 among multiple genres + ko")
		}
	})

	t.Run("all_nil_and_empty", func(t *testing.T) {
		if detectAnime(nil, nil, "") {
			t.Fatal("expected non-anime for all-nil/empty inputs")
		}
	})

	t.Run("literal_keyword_210024_independent_of_constant", func(t *testing.T) {
		if !detectAnime([]int{210024}, nil, "") {
			t.Fatal("expected anime for literal keyword ID 210024")
		}
	})

	t.Run("literal_210024_with_fallback_fields_present", func(t *testing.T) {
		if !detectAnime([]int{210024}, []int{16}, "en") {
			t.Fatal("expected anime for literal 210024 even with western animation fields")
		}
	})

	t.Run("negative_genre_id_ignored", func(t *testing.T) {
		if detectAnime(nil, []int{-16}, "ja") {
			t.Fatal("expected non-anime for negative genre ID -16")
		}
	})

	t.Run("zero_genre_id_ignored", func(t *testing.T) {
		if detectAnime(nil, []int{0}, "zh") {
			t.Fatal("expected non-anime for zero genre ID")
		}
	})

	t.Run("duplicate_genre_16_still_detects", func(t *testing.T) {
		if !detectAnime(nil, []int{16, 16, 16}, "ko") {
			t.Fatal("expected anime for duplicate genre 16 entries + ko")
		}
	})

	t.Run("negative_keyword_ignored", func(t *testing.T) {
		if detectAnime([]int{-210024}, nil, "") {
			t.Fatal("expected non-anime for negative keyword ID")
		}
	})

	t.Run("very_large_genre_id_ignored", func(t *testing.T) {
		if detectAnime(nil, []int{999999}, "ja") {
			t.Fatal("expected non-anime for very large non-animation genre ID")
		}
	})
}
