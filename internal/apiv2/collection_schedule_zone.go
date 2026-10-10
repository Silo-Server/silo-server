package apiv2

import (
	"strings"
	"time"
)

// CollectionScheduleTimeZone is the time zone the answering node runs cron
// collection schedules in. Each node reads its own local zone, so the value
// describes only the node that answered.
type CollectionScheduleTimeZone struct {
	UTCOffset    string `json:"utc_offset" pattern:"^[+-][0-9]{2}:[0-9]{2}$" doc:"Current offset from UTC, daylight saving time included" example:"-05:00"`
	Abbreviation string `json:"abbreviation" doc:"Current zone abbreviation as the node's time zone database reports it; some zones report a numeric form such as -03" example:"CDT"`
	Name         string `json:"name,omitempty" doc:"IANA zone name, from the node's TZ environment variable or UTC when TZ is empty or names no known zone; omitted when the node uses its system default zone or a TZ file path" example:"America/Chicago"`
}

// scheduleTimeZone reports the zone cron schedules use on this node now.
// Collection sync schedules evaluate cron with time.Now() in time.Local.
func (reg *Registry) scheduleTimeZone() CollectionScheduleTimeZone {
	if reg.deps.ScheduleZone != nil {
		return reg.deps.ScheduleZone()
	}
	return scheduleTimeZoneAt(time.Now(), ianaZoneName(time.Local.String()))
}

// scheduleTimeZoneAt describes now's zone, with name as the IANA name when
// one is known.
func scheduleTimeZoneAt(now time.Time, name string) CollectionScheduleTimeZone {
	abbreviation, _ := now.Zone()
	return CollectionScheduleTimeZone{UTCOffset: now.Format("-07:00"), Abbreviation: abbreviation, Name: name}
}

// ianaZoneName returns the IANA name of a time.Local location name, or "".
// Go names time.Local after the TZ value it loaded, "UTC" when loading TZ
// failed, "Local" for the system default, or the file path for a TZ path.
func ianaZoneName(name string) string {
	if name == "" || name == "Local" || strings.HasPrefix(name, "/") {
		return ""
	}
	return name
}

// mdblistSearch reports whether the MDBList search the collection editors use
// can answer: an import service is wired and has an MDBList API key.
func (reg *Registry) mdblistSearch() bool {
	return reg.deps.CollectionImports != nil && reg.deps.CollectionImports.MDBListConfigured()
}
