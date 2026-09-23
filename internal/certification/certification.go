package certification

import "strings"

// Certification preserves the source of a library's displayed rating. An
// equivalent is a Silo access-control estimate, never an official local rating.
type Certification struct {
	Country       string `json:"country"`
	Rating        string `json:"rating"`
	SourceCountry string `json:"source_country"`
	SourceRating  string `json:"source_rating"`
	Equivalent    bool   `json:"equivalent"`
}

func CertificationCountry(country string) string {
	country = strings.ToUpper(strings.TrimSpace(country))
	if country == "" {
		return "US"
	}
	return country
}

func SupportedCertificationCountry(country string) bool {
	switch CertificationCountry(country) {
	case "US", "AU":
		return true
	}
	return false
}

// NormalizeCertification handles provider and NFO spelling variants without
// guessing the country of ambiguous unqualified labels such as G and PG.
func NormalizeCertification(country, rating string) string {
	rating = strings.ToUpper(strings.TrimSpace(rating))
	country = CertificationCountry(country)
	rating = strings.TrimPrefix(rating, country+"-")
	rating = strings.TrimPrefix(rating, country+":")
	if country == "AU" {
		rating = strings.ReplaceAll(rating, " ", "")
		switch rating {
		case "M15+":
			rating = "M"
		case "MA", "MA15":
			rating = "MA15+"
		case "R", "R18":
			rating = "R18+"
		case "X", "X18":
			rating = "X18+"
		}
	}
	return rating
}

// USFallbackRating deliberately maps the broad US adult categories to R18+,
// not MA15+. Unknown and unrated values never become a recognised rating.
func USFallbackRating(rating string) string {
	switch NormalizeCertification("US", rating) {
	case "G", "TV-G", "TV-Y":
		return "G"
	case "PG", "TV-PG", "TV-Y7", "TV-Y7-FV":
		return "PG"
	case "PG-13", "TV-14":
		return "M"
	case "R", "NC-17", "TV-MA":
		return "R18+"
	}
	return ""
}

func ResolveCertification(country, base string, ratings map[string]string, locked bool) Certification {
	country = CertificationCountry(country)
	base = strings.ToUpper(strings.TrimSpace(base))
	if locked || strings.HasPrefix(base, "AU-") || strings.HasPrefix(base, "AU:") {
		sourceCountry := "US"
		if strings.HasPrefix(base, "AU-") || strings.HasPrefix(base, "AU:") {
			sourceCountry = "AU"
		}
		rating := NormalizeCertification(sourceCountry, base)
		return Certification{Country: sourceCountry, Rating: rating, SourceCountry: sourceCountry, SourceRating: rating}
	}
	if country == "AU" {
		if local := NormalizeCertification("AU", ratings["AU"]); local != "" {
			return Certification{Country: "AU", Rating: local, SourceCountry: "AU", SourceRating: local}
		}
		us := NormalizeCertification("US", ratings["US"])
		if us == "" {
			us = NormalizeCertification("US", base)
		}
		return Certification{Country: "AU", Rating: USFallbackRating(us), SourceCountry: "US", SourceRating: us, Equivalent: USFallbackRating(us) != ""}
	}
	if base == "" {
		base = NormalizeCertification("US", ratings["US"])
	}
	return Certification{Country: "US", Rating: base, SourceCountry: "US", SourceRating: base}
}

func (c Certification) Token() string {
	if c.Rating == "" {
		return ""
	}
	if c.Country == "AU" {
		return "AU-" + c.Rating
	}
	return c.Rating
}
