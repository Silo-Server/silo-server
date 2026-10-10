package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
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

type syncCapabilities struct {
	MDBListSearch    *bool `json:"mdblist_search"`
	ScheduleTimeZone *struct {
		UTCOffset    string  `json:"utc_offset"`
		Abbreviation string  `json:"abbreviation"`
		Name         *string `json:"name"`
	} `json:"schedule_time_zone"`
}

func readSyncCapabilities(t *testing.T, h http.Handler, path string, headers map[string]string) syncCapabilities {
	t.Helper()
	rec := do(t, h, http.MethodGet, path, "", headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
	}
	var got syncCapabilities
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.MDBListSearch == nil || got.ScheduleTimeZone == nil {
		t.Fatalf("%s: missing mdblist_search or schedule_time_zone in %s", path, rec.Body)
	}
	return got
}

// TestCollectionCapabilitiesReportMDBListSearch checks that both capability
// documents report mdblist_search from the MDBList client the search route
// uses, and report false when no import service is wired.
func TestCollectionCapabilitiesReportMDBListSearch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		imports CollectionImportService
		want    bool
	}{
		{name: "configured", imports: &fakeCollectionImports{configured: true}, want: true},
		{name: "no api key", imports: &fakeCollectionImports{configured: false}, want: false},
		{name: "no import service", imports: nil, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, _, _ := collectionDeps(t)
			deps.CollectionImports = tc.imports
			deps.AdminCollections = newFakeAdminCollections()
			h := newTestHandler(t, deps)
			if got := *readSyncCapabilities(t, h, "/api/v2/collections/capabilities", viewerHeaders()).MDBListSearch; got != tc.want {
				t.Errorf("personal mdblist_search = %v, want %v", got, tc.want)
			}
			if got := *readSyncCapabilities(t, h, adminCollectionCapabilitiesPath, bearer(adminToken)).MDBListSearch; got != tc.want {
				t.Errorf("admin mdblist_search = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCollectionCapabilitiesReportScheduleTimeZone checks that both documents
// report the answering node's current UTC offset and abbreviation, and leave
// out the zone name when the process has no TZ setting.
func TestCollectionCapabilitiesReportScheduleTimeZone(t *testing.T) {
	deps, _, _ := collectionDeps(t)
	deps.AdminCollections = newFakeAdminCollections()
	h := newTestHandler(t, deps)
	offsetPattern := regexp.MustCompile(`^[+-][0-9]{2}:[0-9]{2}$`)
	for _, read := range []struct {
		path    string
		headers map[string]string
	}{
		{"/api/v2/collections/capabilities", viewerHeaders()},
		{adminCollectionCapabilitiesPath, bearer(adminToken)},
	} {
		zone := readSyncCapabilities(t, h, read.path, read.headers).ScheduleTimeZone
		now := time.Now()
		abbreviation, seconds := now.Zone()
		if !offsetPattern.MatchString(zone.UTCOffset) {
			t.Fatalf("%s: utc_offset %q is not ±hh:mm", read.path, zone.UTCOffset)
		}
		parsed, err := time.Parse("-07:00", zone.UTCOffset)
		if err != nil {
			t.Fatalf("%s: utc_offset %q: %v", read.path, zone.UTCOffset, err)
		}
		if _, got := parsed.Zone(); got != seconds {
			t.Errorf("%s: utc_offset %q is %ds, want %ds", read.path, zone.UTCOffset, got, seconds)
		}
		if zone.Abbreviation != abbreviation {
			t.Errorf("%s: abbreviation %q, want %q", read.path, zone.Abbreviation, abbreviation)
		}
		if _, set := os.LookupEnv("TZ"); !set && zone.Name != nil {
			t.Errorf("%s: name %q reported without a TZ setting", read.path, *zone.Name)
		}
	}
}

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
