package apiv2

import (
	"context"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

func TestNumericIdentifiersRequireCanonicalPositiveDecimal(t *testing.T) {
	intParser := func(parse func(ID) (int, *Problem)) func(ID) (int64, *Problem) {
		return func(id ID) (int64, *Problem) {
			n, p := parse(id)
			return int64(n), p
		}
	}
	for name, parse := range map[string]func(ID) (int64, *Problem){
		"API key identifier":  adminAPIKeyID,
		"access_group":        adminGroupID,
		"invitation":          invitationID,
		"invite_code":         intParser(inviteCodeID),
		"plugin_installation": intParser(adminPluginInstallationID),
		"history_import":      intParser(adminHistoryID),
		"library":             intParser(libraryID),
		"scan_library":        intParser(scanControlLibraryID),
		"positive": intParser(func(id ID) (int, *Problem) {
			return id.positive("path.id")
		}),
	} {
		t.Run(name, func(t *testing.T) {
			for _, raw := range []string{"", "007", "+7", "0", "-7", "9223372036854775808", "seven", "7.0", " 7", "7 "} {
				t.Run(raw, func(t *testing.T) {
					if n, p := parse(ID(raw)); p == nil || n != 0 {
						t.Fatalf("accepted noncanonical ID %q: value=%d problem=%v", raw, n, p)
					}
				})
			}
			for _, want := range []int64{1, 7, int64(math.MaxInt)} {
				if n, p := parse(ID(strconv.FormatInt(want, 10))); p != nil || n != want {
					t.Fatalf("canonical ID %d: value=%d problem=%v", want, n, p)
				}
			}
		})
	}
}

func TestInt64IdentifiersRetainDatabaseKeyRange(t *testing.T) {
	for _, parse := range []func(ID) (int64, *Problem){adminAPIKeyID, adminGroupID, invitationID} {
		if got, p := parse("9223372036854775807"); p != nil || got != math.MaxInt64 {
			t.Fatalf("largest int64 ID: value=%d problem=%v", got, p)
		}
	}
}

func TestNumericIdentifierFiltersRejectAlternateSpellingsHTTP(t *testing.T) {
	h := newTestHandler(t, fixtureDeps())
	for _, route := range []struct {
		path    string
		key     string
		headers map[string]string
	}{
		{"/progress", "library_id", profileOwner()},
		{"/admin/subtitles", "user_id", bearer(adminToken)},
		{"/admin/subtitles", "media_file_id", bearer(adminToken)},
		{"/admin/playback-history", "user_id", bearer(adminToken)},
	} {
		t.Run(route.path+"/"+route.key, func(t *testing.T) {
			for _, raw := range []string{"007", "+7", "0", "-7", "9223372036854775808"} {
				out := do(t, h, http.MethodGet, Prefix+route.path+"?"+route.key+"="+url.QueryEscape(raw), "", route.headers)
				if out.Code != http.StatusUnprocessableEntity {
					t.Fatalf("ID %q: %d %s", raw, out.Code, out.Body.String())
				}
			}
			out := do(t, h, http.MethodGet, Prefix+route.path+"?"+route.key+"=7", "", route.headers)
			if out.Code != http.StatusOK {
				t.Fatalf("canonical ID: %d %s", out.Code, out.Body.String())
			}
		})
	}
}

func TestSubtitleAIJobIDsCanonicalInt64HTTP(t *testing.T) {
	deps, _ := catalogDeps(t)
	reads := new(fakeSubtitleAIReads)
	deps.SubtitleAIReads = reads
	deps.SubtitleAICancel = numericJobCancelFunc(func(_ context.Context, _ catalogpkg.AccessFilter, id int64) error {
		reads.jobID = id
		return nil
	})
	h := newTestHandler(t, deps)
	for _, route := range []struct {
		method, suffix string
		status         int
	}{
		{http.MethodGet, "", http.StatusOK},
		{http.MethodPost, "/cancel", http.StatusNoContent},
	} {
		t.Run(route.method, func(t *testing.T) {
			for _, raw := range []string{"007", "+7", "0", "-7", "9223372036854775808"} {
				reads.jobID = 0
				out := do(t, h, route.method, Prefix+"/subtitles/ai/jobs/"+url.PathEscape(raw)+route.suffix, "", viewerHeaders())
				if out.Code != http.StatusUnprocessableEntity || reads.jobID != 0 {
					t.Fatalf("ID %q: %d %s, service ID %d", raw, out.Code, out.Body.String(), reads.jobID)
				}
			}
			out := do(t, h, route.method, Prefix+"/subtitles/ai/jobs/9223372036854775807"+route.suffix, "", viewerHeaders())
			if out.Code != route.status || reads.jobID != math.MaxInt64 {
				t.Fatalf("full int64 range: %d %s, service ID %d", out.Code, out.Body.String(), reads.jobID)
			}
		})
	}
}

type numericJobCancelFunc func(context.Context, catalogpkg.AccessFilter, int64) error

func (f numericJobCancelFunc) CancelSubtitleAIJob(ctx context.Context, filter catalogpkg.AccessFilter, id int64) error {
	return f(ctx, filter, id)
}
