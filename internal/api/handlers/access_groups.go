package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
)

type AccessGroupStore interface {
	List(ctx context.Context) ([]access.Group, error)
	Get(ctx context.Context, id int64) (*access.Group, error)
	Create(ctx context.Context, input access.CreateGroupInput) (*access.Group, error)
	Update(ctx context.Context, id int64, input access.UpdateGroupInput) (*access.Group, error)
	Delete(ctx context.Context, id int64) error
}

type AccessGroupHandler struct {
	store AccessGroupStore
}

func NewAccessGroupHandler(store AccessGroupStore) *AccessGroupHandler {
	return &AccessGroupHandler{store: store}
}

type accessGroupCreateRequest struct {
	Name                     string                      `json:"name"`
	Description              string                      `json:"description"`
	LibraryIDs               accessGroupIntSliceField    `json:"library_ids"`
	MaxPlaybackQuality       string                      `json:"max_playback_quality"`
	DownloadAllowed          *bool                       `json:"download_allowed,omitempty"`
	DownloadTranscodeAllowed *bool                       `json:"download_transcode_allowed,omitempty"`
	MaxStreams               *int                        `json:"max_streams,omitempty"`
	MaxTranscodes            *int                        `json:"max_transcodes,omitempty"`
	AllowedPermissions       accessGroupStringSliceField `json:"allowed_permissions"`
	RequestsAllowed          *bool                       `json:"requests_allowed,omitempty"`
	IsDefault                bool                        `json:"is_default"`
}

type accessGroupUpdateRequest struct {
	Name                     *string                     `json:"name,omitempty"`
	Description              *string                     `json:"description,omitempty"`
	LibraryIDs               accessGroupIntSliceField    `json:"library_ids,omitempty"`
	MaxPlaybackQuality       *string                     `json:"max_playback_quality,omitempty"`
	DownloadAllowed          *bool                       `json:"download_allowed,omitempty"`
	DownloadTranscodeAllowed *bool                       `json:"download_transcode_allowed,omitempty"`
	MaxStreams               *int                        `json:"max_streams,omitempty"`
	MaxTranscodes            *int                        `json:"max_transcodes,omitempty"`
	AllowedPermissions       accessGroupStringSliceField `json:"allowed_permissions,omitempty"`
	RequestsAllowed          *bool                       `json:"requests_allowed,omitempty"`
	IsDefault                *bool                       `json:"is_default,omitempty"`
}

type accessGroupResponse struct {
	ID                       int64     `json:"id"`
	Name                     string    `json:"name"`
	Description              string    `json:"description"`
	LibraryIDs               []int     `json:"library_ids"`
	MaxPlaybackQuality       string    `json:"max_playback_quality"`
	DownloadAllowed          bool      `json:"download_allowed"`
	DownloadTranscodeAllowed bool      `json:"download_transcode_allowed"`
	MaxStreams               int       `json:"max_streams"`
	MaxTranscodes            int       `json:"max_transcodes"`
	AllowedPermissions       []string  `json:"allowed_permissions"`
	RequestsAllowed          bool      `json:"requests_allowed"`
	IsDefault                bool      `json:"is_default"`
	MemberCount              int       `json:"member_count"`
	CreatedAt                time.Time `json:"created_at"`
	UpdatedAt                time.Time `json:"updated_at"`
}

type accessGroupIntSliceField struct {
	Set   bool
	Value []int
}

func (f *accessGroupIntSliceField) UnmarshalJSON(data []byte) error {
	f.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		f.Value = nil
		return nil
	}
	return json.Unmarshal(data, &f.Value)
}

func (f accessGroupIntSliceField) Ptr() *[]int {
	if !f.Set {
		return nil
	}
	value := append([]int(nil), f.Value...)
	return &value
}

type accessGroupStringSliceField struct {
	Set   bool
	Value []string
}

func (f *accessGroupStringSliceField) UnmarshalJSON(data []byte) error {
	f.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		f.Value = nil
		return nil
	}
	return json.Unmarshal(data, &f.Value)
}

func (f accessGroupStringSliceField) Ptr() *[]string {
	if !f.Set {
		return nil
	}
	value := append([]string(nil), f.Value...)
	return &value
}

func (h *AccessGroupHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Access groups are not configured")
		return
	}
	groups, err := h.store.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list access groups")
		return
	}
	resp := make([]accessGroupResponse, 0, len(groups))
	for _, group := range groups {
		resp = append(resp, toAccessGroupResponse(group))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *AccessGroupHandler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Access groups are not configured")
		return
	}
	var req accessGroupCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	input, ok := req.toInput(w)
	if !ok {
		return
	}
	group, err := h.store.Create(r.Context(), input)
	if err != nil {
		writeAccessGroupError(w, err, "Failed to create access group")
		return
	}
	writeJSON(w, http.StatusCreated, toAccessGroupResponse(*group))
}

func (h *AccessGroupHandler) HandleGet(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Access groups are not configured")
		return
	}
	id, ok := parseAccessGroupID(w, r)
	if !ok {
		return
	}
	group, err := h.store.Get(r.Context(), id)
	if err != nil {
		writeAccessGroupError(w, err, "Failed to load access group")
		return
	}
	writeJSON(w, http.StatusOK, toAccessGroupResponse(*group))
}

func (h *AccessGroupHandler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Access groups are not configured")
		return
	}
	id, ok := parseAccessGroupID(w, r)
	if !ok {
		return
	}
	var req accessGroupUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	input, ok := req.toInput(w)
	if !ok {
		return
	}
	group, err := h.store.Update(r.Context(), id, input)
	if err != nil {
		writeAccessGroupError(w, err, "Failed to update access group")
		return
	}
	writeJSON(w, http.StatusOK, toAccessGroupResponse(*group))
}

// HandleDelete removes an access group. The users foreign key clears member
// assignments. The default group cannot be deleted (409) — new non-admin users
// are assigned to it at creation, so another group must be made default first.
func (h *AccessGroupHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Access groups are not configured")
		return
	}
	id, ok := parseAccessGroupID(w, r)
	if !ok {
		return
	}
	if err := h.store.Delete(r.Context(), id); err != nil {
		writeAccessGroupError(w, err, "Failed to delete access group")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (r accessGroupCreateRequest) toInput(w http.ResponseWriter) (access.CreateGroupInput, bool) {
	name := strings.TrimSpace(r.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name is required")
		return access.CreateGroupInput{}, false
	}
	maxPlaybackQuality, ok := normalizeAccessGroupQuality(w, r.MaxPlaybackQuality)
	if !ok {
		return access.CreateGroupInput{}, false
	}
	if !validateAccessGroupIDs(w, r.LibraryIDs.Value) {
		return access.CreateGroupInput{}, false
	}
	allowedPermissions, ok := normalizeAccessGroupPermissions(w, r.AllowedPermissions)
	if !ok {
		return access.CreateGroupInput{}, false
	}
	downloadAllowed := true
	if r.DownloadAllowed != nil {
		downloadAllowed = *r.DownloadAllowed
	}
	downloadTranscodeAllowed := true
	if r.DownloadTranscodeAllowed != nil {
		downloadTranscodeAllowed = *r.DownloadTranscodeAllowed
	}
	requestsAllowed := true
	if r.RequestsAllowed != nil {
		requestsAllowed = *r.RequestsAllowed
	}
	maxStreams := 0
	if r.MaxStreams != nil {
		maxStreams = *r.MaxStreams
	}
	maxTranscodes := 0
	if r.MaxTranscodes != nil {
		maxTranscodes = *r.MaxTranscodes
	}
	if maxStreams < 0 || maxTranscodes < 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "stream limits must be non-negative")
		return access.CreateGroupInput{}, false
	}
	return access.CreateGroupInput{
		Name:                     name,
		Description:              r.Description,
		LibraryIDs:               r.LibraryIDs.Value,
		MaxPlaybackQuality:       maxPlaybackQuality,
		DownloadAllowed:          downloadAllowed,
		DownloadTranscodeAllowed: downloadTranscodeAllowed,
		MaxStreams:               maxStreams,
		MaxTranscodes:            maxTranscodes,
		AllowedPermissions:       allowedPermissions,
		RequestsAllowed:          requestsAllowed,
		IsDefault:                r.IsDefault,
	}, true
}

func (r accessGroupUpdateRequest) toInput(w http.ResponseWriter) (access.UpdateGroupInput, bool) {
	if r.Name != nil {
		name := strings.TrimSpace(*r.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "name is required")
			return access.UpdateGroupInput{}, false
		}
		r.Name = &name
	}
	var maxPlaybackQuality *string
	if r.MaxPlaybackQuality != nil {
		normalized, ok := normalizeAccessGroupQuality(w, *r.MaxPlaybackQuality)
		if !ok {
			return access.UpdateGroupInput{}, false
		}
		maxPlaybackQuality = &normalized
	}
	if r.LibraryIDs.Set && !validateAccessGroupIDs(w, r.LibraryIDs.Value) {
		return access.UpdateGroupInput{}, false
	}
	if r.MaxStreams != nil && *r.MaxStreams < 0 || r.MaxTranscodes != nil && *r.MaxTranscodes < 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "stream limits must be non-negative")
		return access.UpdateGroupInput{}, false
	}
	var allowedPermissions *[]string
	if r.AllowedPermissions.Set {
		normalized, ok := normalizeAccessGroupPermissions(w, r.AllowedPermissions)
		if !ok {
			return access.UpdateGroupInput{}, false
		}
		allowedPermissions = &normalized
	}
	return access.UpdateGroupInput{
		Name:                     r.Name,
		Description:              r.Description,
		LibraryIDs:               r.LibraryIDs.Ptr(),
		MaxPlaybackQuality:       maxPlaybackQuality,
		DownloadAllowed:          r.DownloadAllowed,
		DownloadTranscodeAllowed: r.DownloadTranscodeAllowed,
		MaxStreams:               r.MaxStreams,
		MaxTranscodes:            r.MaxTranscodes,
		AllowedPermissions:       allowedPermissions,
		RequestsAllowed:          r.RequestsAllowed,
		IsDefault:                r.IsDefault,
	}, true
}

func normalizeAccessGroupQuality(w http.ResponseWriter, raw string) (string, bool) {
	normalized, ok := access.ParsePlaybackQualityPreset(raw)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid max_playback_quality")
		return "", false
	}
	return normalized, true
}

func normalizeAccessGroupPermissions(w http.ResponseWriter, field accessGroupStringSliceField) ([]string, bool) {
	if !field.Set || field.Value == nil {
		return nil, true
	}
	normalized, err := auth.NormalizePermissions(field.Value)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return nil, false
	}
	return normalized, true
}

func validateAccessGroupIDs(w http.ResponseWriter, ids []int) bool {
	for _, id := range ids {
		if id <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "library_ids must contain positive integers")
			return false
		}
	}
	return true
}

func parseAccessGroupID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid access group ID")
		return 0, false
	}
	return id, true
}

func writeAccessGroupError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, access.ErrGroupNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Access group not found")
	case errors.Is(err, access.ErrGroupDuplicate):
		writeError(w, http.StatusConflict, "conflict", "Access group name already exists")
	case errors.Is(err, access.ErrDefaultGroupRequired):
		writeError(w, http.StatusConflict, "conflict",
			"This is the default group for new users. Make another group the default first.")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", fallback)
	}
}

func toAccessGroupResponse(group access.Group) accessGroupResponse {
	return accessGroupResponse{
		ID:                       group.ID,
		Name:                     group.Name,
		Description:              group.Description,
		LibraryIDs:               append([]int(nil), group.LibraryIDs...),
		MaxPlaybackQuality:       access.NormalizePlaybackQuality(group.MaxPlaybackQuality),
		DownloadAllowed:          group.DownloadAllowed,
		DownloadTranscodeAllowed: group.DownloadTranscodeAllowed,
		MaxStreams:               group.MaxStreams,
		MaxTranscodes:            group.MaxTranscodes,
		AllowedPermissions:       append([]string(nil), group.AllowedPermissions...),
		RequestsAllowed:          group.RequestsAllowed,
		IsDefault:                group.IsDefault,
		MemberCount:              group.MemberCount,
		CreatedAt:                group.CreatedAt,
		UpdatedAt:                group.UpdatedAt,
	}
}
