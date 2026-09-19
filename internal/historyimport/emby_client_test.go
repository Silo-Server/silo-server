package historyimport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Emby returns the base UserData block for EnableUserData=true but omits
// LastPlayedDate and PlayCount unless they are named in Fields. normalizeEmbyItem
// reads both, so a request without them silently yields records with no watch
// timestamp, and the importer then creates no history rows.
func TestEmbyFetchItems_RequestsUserDataFieldsTheImporterReads(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		fetch func(*EmbyClient, embyLocalAuth) error
	}{
		{
			name: "FetchItems",
			fetch: func(c *EmbyClient, auth embyLocalAuth) error {
				_, err := c.FetchItems(context.Background(), auth, "IsPlayed")
				return err
			},
		},
		{
			name: "FetchFavoriteItems",
			fetch: func(c *EmbyClient, auth embyLocalAuth) error {
				_, err := c.FetchFavoriteItems(context.Background(), auth)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// The handler runs on the server's goroutine; guard the capture so
			// reading it back here has a happens-before edge.
			var mu sync.Mutex
			var gotFields string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				gotFields = r.URL.Query().Get("Fields")
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(embyItemsResponse{}); err != nil {
					t.Errorf("encode response: %v", err)
				}
			}))
			defer server.Close()

			auth := embyLocalAuth{BaseURL: server.URL, UserID: "user-1", AccessToken: "token-1"}
			if err := tc.fetch(NewEmbyClient(), auth); err != nil {
				t.Fatalf("fetch returned error: %v", err)
			}

			mu.Lock()
			captured := gotFields
			mu.Unlock()

			fields := strings.Split(captured, ",")
			for i, field := range fields {
				fields[i] = strings.TrimSpace(field)
			}
			for _, want := range []string{"ProviderIds", "UserDataLastPlayedDate", "UserDataPlayCount"} {
				if !slices.Contains(fields, want) {
					t.Errorf("Fields = %q, missing %q", captured, want)
				}
			}
		})
	}
}

// A played item carrying LastPlayedDate must reach the record as the watch
// timestamp: addImportedHistoryIfMissing drops the record when it is nil, which
// is what leaves imported history empty.
func TestNormalizeEmbyItem_CarriesLastPlayedDateAndPlayCount(t *testing.T) {
	t.Parallel()

	const payload = `{"Items":[{
		"Id":"item-1","Name":"Arrival","Type":"Movie","ProductionYear":2016,
		"UserData":{"Played":true,"PlayCount":3,"LastPlayedDate":"2026-01-22T22:03:35Z"}
	}],"TotalRecordCount":1}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(payload)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	auth := embyLocalAuth{BaseURL: server.URL, UserID: "user-1", AccessToken: "token-1"}
	items, err := NewEmbyClient().FetchItems(context.Background(), auth, "IsPlayed")
	if err != nil {
		t.Fatalf("FetchItems returned error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}

	record := normalizeEmbyItem(items[0], embyItem{})
	if record.LastPlayedAt == nil {
		t.Fatal("record.LastPlayedAt is nil, want the item's LastPlayedDate")
	}
	if got := record.LastPlayedAt.UTC().Format("2006-01-02T15:04:05Z"); got != "2026-01-22T22:03:35Z" {
		t.Errorf("record.LastPlayedAt = %s, want 2026-01-22T22:03:35Z", got)
	}
	if record.PlayCount != 3 {
		t.Errorf("record.PlayCount = %d, want 3", record.PlayCount)
	}
	if record.UpdatedAt.IsZero() {
		t.Error("record.UpdatedAt is zero, want the last-played time")
	}
}
