package catalog

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

// searchFoldLetters maps the letters NFD leaves whole, and spells out ß, æ
// and œ, exactly as public.fold_search_diacritics does.
var searchFoldLetters = strings.NewReplacer(
	"Ø", "O", "ø", "o", "Đ", "D", "đ", "d", "Ð", "D", "ð", "d", "Ł", "L", "ł", "l",
	"Ħ", "H", "ħ", "h", "ı", "i", "Ŧ", "T", "ŧ", "t", "Ŀ", "L", "ŀ", "l",
	"ß", "ss", "ẞ", "SS", "æ", "ae", "Æ", "AE", "œ", "oe", "Œ", "OE",
)

// foldSearchDiacritics mirrors public.fold_search_diacritics (migration
// 20261023173138) so queries fold like the stored titles they are compared
// with: decompose, drop combining marks U+0300-U+036F, recompose, then map the
// letters NFD leaves whole. TestSearchTextFoldMatchesGoPostgres fails when the
// two drift apart.
func foldSearchDiacritics(input string) string {
	if isASCII(input) {
		return input
	}
	stripped := strings.Map(func(r rune) rune {
		if r >= 0x300 && r <= 0x36f {
			return -1
		}
		return r
	}, norm.NFD.String(input))
	return searchFoldLetters.Replace(norm.NFC.String(stripped))
}

func isASCII(input string) bool {
	for i := 0; i < len(input); i++ {
		if input[i] >= 0x80 {
			return false
		}
	}
	return true
}
