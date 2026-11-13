package apiv2

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

const adminCollectionCapabilitiesPath = "/api/v2/admin/collections/capabilities"

func (f *fakeAdminCollections) AdminCollectionFeatures(context.Context) userstore.CollectionFeatures {
	f.featureReads++
	return userstore.CollectionFeatures{Groups: true, Imports: true, Artwork: true, ItemReorder: true}
}

func (f *fakeCollectionImports) MDBListConfigured() bool { return f.configured }

// TestScheduleTimeZoneAt pins the offset and abbreviation across daylight
// saving time, and the IANA name only when the local zone has one.
func TestScheduleTimeZoneAt(t *testing.T) {
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skipf("time zone database unavailable: %v", err)
	}
	summer := time.Date(2026, time.July, 1, 12, 0, 0, 0, chicago)
	winter := time.Date(2026, time.January, 1, 12, 0, 0, 0, chicago)
	if got, want := scheduleTimeZoneAt(summer, ianaZoneName("America/Chicago")), (CollectionScheduleTimeZone{UTCOffset: "-05:00", Abbreviation: "CDT", Name: "America/Chicago"}); got != want {
		t.Errorf("summer = %+v, want %+v", got, want)
	}
	if got, want := scheduleTimeZoneAt(winter, ""), (CollectionScheduleTimeZone{UTCOffset: "-06:00", Abbreviation: "CST"}); got != want {
		t.Errorf("winter without TZ = %+v, want %+v", got, want)
	}
	if got, want := scheduleTimeZoneAt(time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC), ""), (CollectionScheduleTimeZone{UTCOffset: "+00:00", Abbreviation: "UTC"}); got != want {
		t.Errorf("utc = %+v, want %+v", got, want)
	}
	kolkata := time.FixedZone("IST", 5*3600+30*60)
	if got := scheduleTimeZoneAt(time.Date(2026, time.July, 1, 12, 0, 0, 0, kolkata), "").UTCOffset; got != "+05:30" {
		t.Errorf("half-hour offset = %q, want +05:30", got)
	}

	// Names as Go assigns them to time.Local: the TZ value when it names a
	// zone, "UTC" when loading TZ fails, "Local" for /etc/localtime, and the
	// path for any other TZ file.
	for name, want := range map[string]string{
		"America/Chicago":                  "America/Chicago",
		"UTC":                              "UTC",
		"Local":                            "",
		"/usr/share/zoneinfo/Europe/Paris": "",
		"":                                 "",
	} {
		if got := ianaZoneName(name); got != want {
			t.Errorf("ianaZoneName(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestScheduleTimeZoneNamesTheZoneGoLoaded checks that the reported name
// comes from the zone the offset was read from, so the two cannot disagree.
func TestScheduleTimeZoneNamesTheZoneGoLoaded(t *testing.T) {
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skipf("time zone database unavailable: %v", err)
	}
	saved := time.Local
	time.Local = tokyo
	t.Cleanup(func() { time.Local = saved })

	want := CollectionScheduleTimeZone{UTCOffset: "+09:00", Abbreviation: "JST", Name: "Asia/Tokyo"}
	if got := (&Registry{}).scheduleTimeZone(); got != want {
		t.Errorf("scheduleTimeZone() = %+v, want %+v", got, want)
	}
}

// TestAdminCollectionCapabilitiesRequireActingAdmin restates #193 S5 for the
// administrator capability document: a regular account and a non-primary
// profile on an admin account are refused before any feature is read.
func TestAdminCollectionCapabilitiesRequireActingAdmin(t *testing.T) {
	f := newFakeAdminCollections()
	h := adminCollectionsTestHandler(t, f)
	for _, headers := range []map[string]string{bearer(memberToken), with(bearer(adminToken), "X-Profile-Id", "p-owner")} {
		requireProblem(t, do(t, h, http.MethodGet, adminCollectionCapabilitiesPath, "", headers), TypePermissionDenied)
	}
	if f.featureReads != 0 {
		t.Fatal("refused request read the collection features")
	}
}
