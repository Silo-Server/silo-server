package bookid

import "strings"

// ISBNProvider is the provider-ID key that holds an item's ISBN.
const ISBNProvider = "isbn"

// CanonicalISBN returns raw as the 13 digits of an ISBN-13. It accepts an
// ISBN-10 or ISBN-13 with or without hyphens and spaces, after an optional
// "ISBN-13", "ISBN-10" or "ISBN" label (with or without a ":"), or written as
// "urn:isbn:". An ISBN-10 is converted to its 978 form. A 13-digit value must
// start 978, or 979 followed by 1 to 9; other EAN-13 barcodes, the 979-0 music
// numbers included, are not ISBNs. ok is false when raw is not an ISBN.
func CanonicalISBN(raw string) (string, bool) {
	value := strings.ToUpper(strings.TrimSpace(raw))
	value = strings.TrimPrefix(value, "URN:")
	for _, label := range []string{"ISBN-13", "ISBN-10", "ISBN"} {
		if strings.HasPrefix(value, label) {
			value = strings.TrimLeft(strings.TrimPrefix(value, label), ": ")
			break
		}
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= '0' && r <= '9') || r == 'X' {
			b.WriteRune(r)
		}
	}
	candidate := b.String()
	switch len(candidate) {
	case 10:
		if !validISBN10(candidate) {
			return "", false
		}
		isbn13 := "978" + candidate[:9]
		return isbn13 + string(isbn13CheckDigit(isbn13)), true
	case 13:
		// 979-0 is the ISMN range (printed music), not ISBNs.
		if !strings.HasPrefix(candidate, "978") &&
			(!strings.HasPrefix(candidate, "979") || strings.HasPrefix(candidate, "9790")) {
			return "", false
		}
		if !allDigits(candidate) || isbn13CheckDigit(candidate[:12]) != candidate[12] {
			return "", false
		}
		return candidate, true
	}
	return "", false
}

// CanonicalProviderID returns value as an ISBN-13 when provider is the isbn
// key (in any case) and value parses as an ISBN, and value unchanged
// otherwise. A value that does not parse is kept, never dropped.
func CanonicalProviderID(provider, value string) string {
	if !strings.EqualFold(strings.TrimSpace(provider), ISBNProvider) {
		return value
	}
	if isbn, ok := CanonicalISBN(value); ok {
		return isbn
	}
	return value
}

func validISBN10(candidate string) bool {
	sum := 0
	for i, r := range candidate {
		var digit int
		switch {
		case r >= '0' && r <= '9':
			digit = int(r - '0')
		case r == 'X' && i == 9:
			digit = 10
		default:
			return false
		}
		sum += digit * (10 - i)
	}
	return sum%11 == 0
}

// isbn13CheckDigit returns the check digit for the first 12 digits of an
// ISBN-13. The caller has checked they are digits.
func isbn13CheckDigit(first12 string) byte {
	sum := 0
	for i := 0; i < 12; i++ {
		digit := int(first12[i] - '0')
		if i%2 == 1 {
			digit *= 3
		}
		sum += digit
	}
	return byte('0' + (10-sum%10)%10)
}

func allDigits(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}
