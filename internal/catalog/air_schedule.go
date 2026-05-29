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

// CalendarEventLocalTime returns the wall-clock moment used to display and
// order a calendar event for a viewer in loc, plus whether the event has a
// known time of day.
//
// When the event's source timezone is known, the absolute instant is converted
// into loc. When it is not, the stored wall-clock air_time is interpreted
// directly in loc — mirroring the client, which renders the raw air_time when
// air_at is absent. Date-only entries (no air_time, e.g. movie releases) return
// the start of the source day with hasTime=false so they sort after timed
// entries within the same local day.
func CalendarEventLocalTime(airDate time.Time, airTime, airTimezone *string, loc *time.Location) (time.Time, bool) {
	if loc == nil {
		loc = time.UTC
	}
	if at := CalendarEventAirAt(airDate, airTime, airTimezone); at != nil {
		return at.In(loc), true
	}
	if airTime != nil {
		if parsed, ok := parseAirTime(*airTime); ok {
			return time.Date(
				airDate.Year(), airDate.Month(), airDate.Day(),
				parsed.Hour(), parsed.Minute(), parsed.Second(), 0, loc,
			), true
		}
	}
	return time.Date(airDate.Year(), airDate.Month(), airDate.Day(), 0, 0, 0, 0, loc), false
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
