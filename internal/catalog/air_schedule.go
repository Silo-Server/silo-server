package catalog

import (
	"strings"
	"time"
)

var networkAirTimezones = map[string]string{
	"abc":       "America/New_York",
	"cbs":       "America/New_York",
	"nbc":       "America/New_York",
	"fox":       "America/New_York",
	"the cw":    "America/New_York",
	"cw":        "America/New_York",
	"hbo":       "America/New_York",
	"showtime":  "America/New_York",
	"fx":        "America/New_York",
	"amc":       "America/New_York",
	"bbc":       "Europe/London",
	"bbc one":   "Europe/London",
	"bbc two":   "Europe/London",
	"itv":       "Europe/London",
	"channel 4": "Europe/London",
}

var countryAirTimezones = map[string]string{
	"france":         "Europe/Paris",
	"japan":          "Asia/Tokyo",
	"south korea":    "Asia/Seoul",
	"korea":          "Asia/Seoul",
	"united kingdom": "Europe/London",
	"uk":             "Europe/London",
}

// ValidateAirTimezone reports whether tz is empty or a valid IANA timezone.
func ValidateAirTimezone(tz string) bool {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return true
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}

// InferAirTimezone returns a conservative source airing timezone for series metadata.
func InferAirTimezone(networks, countries []string) string {
	for _, network := range networks {
		if tz := networkAirTimezones[normalizeScheduleLookupKey(network)]; tz != "" {
			return tz
		}
	}
	for _, country := range countries {
		if tz := countryAirTimezones[normalizeScheduleLookupKey(country)]; tz != "" {
			return tz
		}
	}
	return ""
}

// CalendarEventAirAt combines a source date, source wall-clock time, and source timezone.
func CalendarEventAirAt(airDate time.Time, airTime, airTimezone *string) *time.Time {
	if airTime == nil || strings.TrimSpace(*airTime) == "" {
		return nil
	}
	if airTimezone == nil || strings.TrimSpace(*airTimezone) == "" {
		return nil
	}
	loc, err := time.LoadLocation(strings.TrimSpace(*airTimezone))
	if err != nil {
		return nil
	}
	parsed, ok := parseAirTime(*airTime)
	if !ok {
		return nil
	}
	local := time.Date(
		airDate.Year(),
		airDate.Month(),
		airDate.Day(),
		parsed.Hour(),
		parsed.Minute(),
		parsed.Second(),
		0,
		loc,
	)
	utc := local.UTC()
	return &utc
}

func CalendarLocation(name string) *time.Location {
	if strings.TrimSpace(name) == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(strings.TrimSpace(name))
	if err != nil {
		return time.UTC
	}
	return loc
}

func parseAirTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{"15:04:05", "15:04"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func normalizeScheduleLookupKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
