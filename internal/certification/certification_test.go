package certification

import "testing"

func TestResolveCertification(t *testing.T) {
	for _, tc := range []struct {
		name, country, base, au, us, want string
		locked, equivalent                bool
	}{
		{name: "official AU wins", country: "AU", base: "R", au: "M", us: "R", want: "AU-M"},
		{name: "missing AU uses US", country: "AU", base: "PG-13", want: "AU-M", equivalent: true},
		{name: "adult movie fallback", country: "AU", base: "R", want: "AU-R18+", equivalent: true},
		{name: "adult TV fallback", country: "AU", base: "TV-MA", want: "AU-R18+", equivalent: true},
		{name: "neither exists", country: "AU", want: ""},
		{name: "unrated US", country: "AU", base: "NR", want: ""},
		{name: "unknown local is not downgraded", country: "AU", base: "G", au: "CTC", want: "AU-CTC"},
		{name: "US remains unchanged", country: "US", base: "PG-13", au: "R18+", want: "PG-13"},
		{name: "locked rating wins", country: "AU", base: "R", au: "G", want: "R", locked: true},
		{name: "NFO country is explicit", country: "AU", base: "AU-MA15+", au: "PG", want: "AU-MA15+"},
		{name: "normalise local spelling", country: "AU", au: "MA 15+", want: "AU-MA15+"},
		{name: "do not interpret foreign rating as US", country: "AU", base: "DE-12", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveCertification(tc.country, tc.base, map[string]string{"AU": tc.au, "US": tc.us}, tc.locked)
			if got.Token() != tc.want || got.Equivalent != tc.equivalent {
				t.Fatalf("got %+v, want %q equivalent=%v", got, tc.want, tc.equivalent)
			}
			if got.Equivalent && (got.SourceCountry != "US" || got.SourceRating == "") {
				t.Fatalf("fallback lost source: %+v", got)
			}
		})
	}
}
