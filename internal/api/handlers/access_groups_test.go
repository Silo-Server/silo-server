package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/access"
)

func TestAccessGroupHandlerIsDefaultRoundTrips(t *testing.T) {
	store := newAccessGroupHandlerTestStore()
	handler := NewAccessGroupHandler(store)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/access-groups", strings.NewReader(`{
		"name": "Users",
		"is_default": true
	}`))
	handler.HandleCreate(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("HandleCreate status = %d, body %s", rec.Code, rec.Body.String())
	}
	created := decodeAccessGroupResponse(t, rec)
	if !created.IsDefault {
		t.Fatalf("created is_default = false, want true")
	}

	rec = httptest.NewRecorder()
	req = accessGroupRequestWithID(http.MethodGet, "/api/v1/admin/access-groups/1", nil, "1")
	handler.HandleGet(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HandleGet status = %d, body %s", rec.Code, rec.Body.String())
	}
	got := decodeAccessGroupResponse(t, rec)
	if !got.IsDefault {
		t.Fatalf("get is_default = false, want true")
	}
}

func TestAccessGroupHandlerDefaultGroupGuards(t *testing.T) {
	store := newAccessGroupHandlerTestStore()
	store.groups[1] = access.Group{ID: 1, Name: "Default", DownloadAllowed: true, RequestsAllowed: true, IsDefault: true}
	store.nextID = 2
	handler := NewAccessGroupHandler(store)

	rec := httptest.NewRecorder()
	req := accessGroupRequestWithID(http.MethodDelete, "/api/v1/admin/access-groups/1", nil, "1")
	handler.HandleDelete(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("HandleDelete(default) status = %d, want %d, body %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if _, ok := store.groups[1]; !ok {
		t.Fatalf("default group removed despite conflict response")
	}

	rec = httptest.NewRecorder()
	req = accessGroupRequestWithID(http.MethodPut, "/api/v1/admin/access-groups/1", strings.NewReader(`{
		"is_default": false
	}`), "1")
	handler.HandleUpdate(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("HandleUpdate(demote default) status = %d, want %d, body %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !store.groups[1].IsDefault {
		t.Fatalf("default group demoted despite conflict response")
	}
}

type accessGroupHandlerTestStore struct {
	nextID int64
	groups map[int64]access.Group
}

func newAccessGroupHandlerTestStore() *accessGroupHandlerTestStore {
	return &accessGroupHandlerTestStore{
		nextID: 1,
		groups: map[int64]access.Group{},
	}
}

func (s *accessGroupHandlerTestStore) List(context.Context) ([]access.Group, error) {
	groups := make([]access.Group, 0, len(s.groups))
	for _, group := range s.groups {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].ID < groups[j].ID
	})
	return groups, nil
}

func (s *accessGroupHandlerTestStore) Get(_ context.Context, id int64) (*access.Group, error) {
	group, ok := s.groups[id]
	if !ok {
		return nil, access.ErrGroupNotFound
	}
	return &group, nil
}

func (s *accessGroupHandlerTestStore) Create(_ context.Context, input access.CreateGroupInput) (*access.Group, error) {
	now := time.Unix(1, 0).UTC()
	group := access.Group{
		ID:                       s.nextID,
		Name:                     input.Name,
		Description:              input.Description,
		LibraryIDs:               slices.Clone(input.LibraryIDs),
		MaxPlaybackQuality:       input.MaxPlaybackQuality,
		DownloadAllowed:          input.DownloadAllowed,
		DownloadTranscodeAllowed: input.DownloadTranscodeAllowed,
		MaxStreams:               input.MaxStreams,
		MaxTranscodes:            input.MaxTranscodes,
		AllowedPermissions:       slices.Clone(input.AllowedPermissions),
		RequestsAllowed:          input.RequestsAllowed,
		IsDefault:                input.IsDefault,
		CreatedAt:                now,
		UpdatedAt:                now,
	}
	if input.IsDefault {
		s.clearDefault()
	}
	s.groups[group.ID] = group
	s.nextID++
	return &group, nil
}

func (s *accessGroupHandlerTestStore) Update(_ context.Context, id int64, input access.UpdateGroupInput) (*access.Group, error) {
	group, ok := s.groups[id]
	if !ok {
		return nil, access.ErrGroupNotFound
	}
	if input.Name != nil {
		group.Name = *input.Name
	}
	if input.Description != nil {
		group.Description = *input.Description
	}
	if input.LibraryIDs != nil {
		group.LibraryIDs = slices.Clone(*input.LibraryIDs)
	}
	if input.MaxPlaybackQuality != nil {
		group.MaxPlaybackQuality = *input.MaxPlaybackQuality
	}
	if input.DownloadAllowed != nil {
		group.DownloadAllowed = *input.DownloadAllowed
	}
	if input.DownloadTranscodeAllowed != nil {
		group.DownloadTranscodeAllowed = *input.DownloadTranscodeAllowed
	}
	if input.MaxStreams != nil {
		group.MaxStreams = *input.MaxStreams
	}
	if input.MaxTranscodes != nil {
		group.MaxTranscodes = *input.MaxTranscodes
	}
	if input.AllowedPermissions != nil {
		group.AllowedPermissions = slices.Clone(*input.AllowedPermissions)
	}
	if input.RequestsAllowed != nil {
		group.RequestsAllowed = *input.RequestsAllowed
	}
	if input.IsDefault != nil {
		if *input.IsDefault {
			s.clearDefault()
		} else if group.IsDefault {
			return nil, access.ErrDefaultGroupRequired
		}
		group.IsDefault = *input.IsDefault
	}
	group.UpdatedAt = time.Unix(2, 0).UTC()
	s.groups[id] = group
	return &group, nil
}

func (s *accessGroupHandlerTestStore) Delete(_ context.Context, id int64) error {
	group, ok := s.groups[id]
	if !ok {
		return access.ErrGroupNotFound
	}
	if group.IsDefault {
		return access.ErrDefaultGroupRequired
	}
	delete(s.groups, id)
	return nil
}

func (s *accessGroupHandlerTestStore) clearDefault() {
	for id, group := range s.groups {
		group.IsDefault = false
		s.groups[id] = group
	}
}

func accessGroupRequestWithID(method, path string, body *strings.Reader, id string) *http.Request {
	var reader io.Reader
	if body != nil {
		reader = body
	}
	req := httptest.NewRequest(method, path, reader)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

func decodeAccessGroupResponse(t *testing.T, rec *httptest.ResponseRecorder) accessGroupResponse {
	t.Helper()
	var response accessGroupResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response
}

// An empty allowlist denies every library; null removes the group restriction.
func TestAccessGroupUpdateLibraryPolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want []int
	}{
		{"omitted", `{}`, []int{11}},
		{"null", `{"library_ids":null}`, nil},
		{"empty", `{"library_ids":[]}`, []int{}},
		{"populated", `{"library_ids":[22]}`, []int{22}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newAccessGroupHandlerTestStore()
			store.groups[1] = access.Group{ID: 1, Name: "Synthetic group", LibraryIDs: []int{11}}
			h := NewAccessGroupHandler(store)
			rec := httptest.NewRecorder()
			h.HandleUpdate(rec, accessGroupRequestWithID(http.MethodPut, "/api/v1/admin/access-groups/1", strings.NewReader(tc.body), "1"))
			if rec.Code != http.StatusOK {
				t.Fatalf("update status = %d, body %s", rec.Code, rec.Body.String())
			}
			if got := decodeAccessGroupResponse(t, rec).LibraryIDs; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("library_ids = %#v, want %#v", got, tc.want)
			}
			read := httptest.NewRecorder()
			h.HandleGet(read, accessGroupRequestWithID(http.MethodGet, "/api/v1/admin/access-groups/1", nil, "1"))
			policy := decodeAccessGroupResponse(t, read)
			body, err := json.Marshal(map[string][]int{"library_ids": policy.LibraryIDs})
			if err != nil {
				t.Fatal(err)
			}
			replayed := httptest.NewRecorder()
			h.HandleUpdate(replayed, accessGroupRequestWithID(http.MethodPut, "/api/v1/admin/access-groups/1", strings.NewReader(string(body)), "1"))
			if replayed.Code != http.StatusOK || !reflect.DeepEqual(store.groups[1].LibraryIDs, tc.want) {
				t.Fatalf("read/save round trip: status = %d, library_ids = %#v, want %#v", replayed.Code, store.groups[1].LibraryIDs, tc.want)
			}
		})
	}
}

// A v1 read then save must keep an empty permission mask; null allows every
// assignable permission.
func TestAccessGroupPermissionPolicyRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stored []string
	}{
		{"all", nil},
		{"none", []string{}},
		{"some", []string{"marker_edit"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newAccessGroupHandlerTestStore()
			store.groups[1] = access.Group{ID: 1, Name: "Synthetic group", AllowedPermissions: tc.stored}
			h := NewAccessGroupHandler(store)
			read := httptest.NewRecorder()
			h.HandleGet(read, accessGroupRequestWithID(http.MethodGet, "/api/v1/admin/access-groups/1", nil, "1"))
			if read.Code != http.StatusOK {
				t.Fatalf("get status = %d, body %s", read.Code, read.Body.String())
			}
			body, err := json.Marshal(map[string][]string{"allowed_permissions": decodeAccessGroupResponse(t, read).AllowedPermissions})
			if err != nil {
				t.Fatal(err)
			}
			saved := httptest.NewRecorder()
			h.HandleUpdate(saved, accessGroupRequestWithID(http.MethodPut, "/api/v1/admin/access-groups/1", strings.NewReader(string(body)), "1"))
			if saved.Code != http.StatusOK || !reflect.DeepEqual(store.groups[1].AllowedPermissions, tc.stored) {
				t.Fatalf("read/save: status = %d, allowed_permissions = %#v, want %#v", saved.Code, store.groups[1].AllowedPermissions, tc.stored)
			}
		})
	}
}
