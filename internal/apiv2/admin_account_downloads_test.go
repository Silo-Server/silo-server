package apiv2

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/downloads"
)

func TestAdminUserDownloadsContract(t *testing.T) {
	deps, accounts := adminAccountInsightDeps()
	fake := deps.AdminAccountDownloads.(*fakeAdminAccountDownloads)
	h := NewHandler(deps)
	path := Prefix + "/admin/users/7/downloads"
	query := "?limit=1&profile_id=p-owner&device_id=device-tv"

	first := do(t, h, http.MethodGet, path+query, "", actingRequestAdmin)
	if first.Code != http.StatusOK {
		t.Fatalf("%d %s", first.Code, first.Body.String())
	}
	// The handler asks for one row more than the page to learn has_more.
	if fake.user != 7 || fake.filter != (downloads.AdminDownloadFilter{ProfileID: "p-owner", DeviceID: "device-tv"}) || fake.limit != 2 || fake.after != nil {
		t.Fatalf("first call user=%d filter=%+v limit=%d after=%+v", fake.user, fake.filter, fake.limit, fake.after)
	}
	var page Collection[AdminUserDownload]
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Page == nil || !page.Page.HasMore || page.Page.NextCursor == "" {
		t.Fatalf("first page: %s", first.Body.String())
	}
	episode := page.Items[0]
	if episode.ID != "dl-2" || !episode.Episode.Valid || episode.Episode.Value != (AdminUserDownloadEpisode{SeasonNumber: 2, EpisodeNumber: 4, Title: "Synthetic Episode"}) ||
		episode.Title != "Synthetic Series" || episode.DeliveryFormat != downloads.FormatOriginal || !episode.CompletedAt.Valid || !episode.StatusEventAt.Valid || episode.BatchID != "batch-1" {
		t.Fatalf("episode row: %+v", episode)
	}

	cursor := url.QueryEscape(page.Page.NextCursor)
	second := do(t, h, http.MethodGet, path+query+"&cursor="+cursor, "", actingRequestAdmin)
	if second.Code != http.StatusOK || fake.after == nil || fake.after.ID != "dl-2" || !fake.after.CreatedAt.Equal(fixedTime()) {
		t.Fatalf("second: %d %s after=%+v", second.Code, second.Body.String(), fake.after)
	}
	if err := json.Unmarshal(second.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	movie := page.Items[0]
	if len(page.Items) != 1 || page.Page.HasMore || movie.ID != "dl-1" || movie.Episode.Valid || movie.CompletedAt.Valid || movie.StatusEventAt.Valid {
		t.Fatalf("last page: %s", second.Body.String())
	}
	for _, want := range []string{`"episode":null`, `"completed_at":null`, `"status_event_at":null`} {
		if !strings.Contains(second.Body.String(), want) {
			t.Fatalf("missing %s in %s", want, second.Body.String())
		}
	}
	if strings.Contains(second.Body.String(), `"episode_id"`) || strings.Contains(second.Body.String(), `"batch_id"`) {
		t.Fatalf("empty optional ids rendered: %s", second.Body.String())
	}

	calls := fake.calls
	// A cursor is bound to its account, filters, page size and caller.
	for _, bad := range []string{
		strings.Replace(path, "/7/", "/8/", 1) + query + "&cursor=" + cursor,
		path + strings.Replace(query, "device_id=device-tv", "device_id=device-phone", 1) + "&cursor=" + cursor,
		path + strings.Replace(query, "profile_id=p-owner", "profile_id=p-kid", 1) + "&cursor=" + cursor,
		path + strings.Replace(query, "limit=1", "limit=2", 1) + "&cursor=" + cursor,
		path + "?cursor=invalid",
	} {
		requireProblem(t, do(t, h, http.MethodGet, bad, "", actingRequestAdmin), TypeInvalidCursor)
	}
	requireProblem(t, do(t, h, http.MethodGet, path+query+"&cursor="+cursor, "", bearer(adminToken)), TypeInvalidCursor)
	for _, q := range []string{"?limit=0", "?limit=201", "?device_id=" + strings.Repeat("d", 129)} {
		requireProblem(t, do(t, h, http.MethodGet, path+q, "", actingRequestAdmin), TypeValidationFailed)
	}
	requireProblem(t, do(t, h, http.MethodGet, path, "", bearer(memberToken)), TypePermissionDenied)
	accounts.err = auth.ErrNotFound
	requireProblem(t, do(t, h, http.MethodGet, path, "", actingRequestAdmin), TypeNotFound)
	accounts.err = nil
	if fake.calls != calls {
		t.Fatalf("refused requests reached the service: %d -> %d", calls, fake.calls)
	}

	// Without filters every profile and device is listed.
	if reply := do(t, h, http.MethodGet, path, "", actingRequestAdmin); reply.Code != http.StatusOK || fake.filter != (downloads.AdminDownloadFilter{}) || fake.limit != 51 {
		t.Fatalf("unfiltered: %d filter=%+v limit=%d", reply.Code, fake.filter, fake.limit)
	}
	fake.err = errors.New("PRIVATE database failure")
	failed := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	requireProblem(t, failed, TypeInternalError)
	if strings.Contains(failed.Body.String(), "PRIVATE") {
		t.Fatal("private error leaked")
	}
	deps.AdminAccountDownloads = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodGet, path, "", actingRequestAdmin), TypeDependencyUnavailable)
}

func TestAdminUserDownloadSummaryContract(t *testing.T) {
	deps, accounts := adminAccountInsightDeps()
	fake := deps.AdminAccountDownloads.(*fakeAdminAccountDownloads)
	h := NewHandler(deps)
	path := Prefix + "/admin/users/7/downloads/summary"
	reply := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	if reply.Code != http.StatusOK || fake.lastCall != "summary" || fake.user != 7 {
		t.Fatalf("%d %s", reply.Code, reply.Body.String())
	}
	var body AdminUserDownloadSummary
	if err := json.Unmarshal(reply.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body != (AdminUserDownloadSummary{Total: 17, Completed: 12, InProgress: 4, Failed: 1, Revoked: 2, TotalBytes: 42_000_000_000, Devices: 2, MonitoredSeries: 3}) {
		t.Fatalf("projection %+v", body)
	}
	accounts.err = auth.ErrNotFound
	requireProblem(t, do(t, h, http.MethodGet, path, "", actingRequestAdmin), TypeNotFound)
	deps.AdminAccountDownloads = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodGet, path, "", actingRequestAdmin), TypeDependencyUnavailable)
}

func TestAdminUserDownloadSubscriptionsContract(t *testing.T) {
	deps, _ := adminAccountInsightDeps()
	fake := deps.AdminAccountDownloads.(*fakeAdminAccountDownloads)
	h := NewHandler(deps)
	path := Prefix + "/admin/users/7/download-subscriptions"

	reply := do(t, h, http.MethodGet, path+"?device_id=device-tv", "", actingRequestAdmin)
	if reply.Code != http.StatusOK || fake.lastCall != "subscriptions" || fake.filter.DeviceID != "device-tv" || fake.limit != 51 {
		t.Fatalf("%d %s %+v", reply.Code, reply.Body.String(), fake.filter)
	}
	if !strings.Contains(reply.Body.String(), `"season_numbers":[]`) || !strings.Contains(reply.Body.String(), `"target_season":null`) {
		t.Fatalf("empty seasons not rendered as []: %s", reply.Body.String())
	}
	var page Collection[AdminUserDownloadSubscription]
	if err := json.Unmarshal(reply.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	sub := page.Items[0]
	if len(page.Items) != 1 || page.Page.HasMore || sub.SeriesTitle != "Synthetic Series" || sub.OnDevice != 3 || sub.InProgress != 1 || sub.RemovedEpisodes != 2 || !sub.DeleteWatched || sub.Mode != downloads.SubModeAll {
		t.Fatalf("projection %s", reply.Body.String())
	}

	// A cursor minted for one operation does not page the other.
	fake.subs = append(fake.subs, fixtureAdminSubscriptionRows()...)
	fake.subs[1].ID = "sub-0"
	reply = do(t, h, http.MethodGet, path+"?limit=1", "", actingRequestAdmin)
	if err := json.Unmarshal(reply.Body.Bytes(), &page); err != nil || !page.Page.HasMore {
		t.Fatalf("first page %s %v", reply.Body.String(), err)
	}
	cursor := url.QueryEscape(page.Page.NextCursor)
	if next := do(t, h, http.MethodGet, path+"?limit=1&cursor="+cursor, "", actingRequestAdmin); next.Code != http.StatusOK || !strings.Contains(next.Body.String(), `"id":"sub-0"`) {
		t.Fatalf("second page %d %s", next.Code, next.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/users/7/downloads?limit=1&cursor="+cursor, "", actingRequestAdmin), TypeInvalidCursor)

	fake.subsErr = downloads.ErrSubscriptionsUnavailable
	requireProblem(t, do(t, h, http.MethodGet, path, "", actingRequestAdmin), TypeDependencyUnavailable)
}
