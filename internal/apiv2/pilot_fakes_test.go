package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	mediacatalog "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// The pilot fakes stand in for the v1 handlers' extracted business logic:
// in-memory answers with the same shapes and *handlers.APIError failures.

type fakeAccounts struct {
	needsSetup bool
	users      map[int]handlers.UserView
	err        error
}

func (f fakeAccounts) NeedsSetup(context.Context) (bool, error) { return f.needsSetup, f.err }

func (f fakeAccounts) CurrentUser(_ context.Context, claims *auth.Claims) (handlers.UserView, error) {
	if f.err != nil {
		return handlers.UserView{}, f.err
	}
	view, ok := f.users[claims.UserID]
	if !ok {
		return handlers.UserView{}, &handlers.APIError{Status: 500, Code: TypeInternalError.ID, Message: "An unexpected error occurred"}
	}
	return view, nil
}

// fakeProgressQuery is one ListProgressPage call as the handler made it.
type fakeProgressQuery struct {
	Status    string
	LibraryID int
	After     *userstore.ProgressKey
	Limit     int
}

// fakeProgress stands in for handlers.ProgressHandler.ListProgressPage: a
// keyset store over entries plus the library filter (libraries maps
// media_item_id to its library; a nil map leaves the filter a no-op, a
// non-nil one puts unknown ids in no library), applied the way the real seam
// does — fetch, filter, re-fetch until limit+1 matches.
type fakeProgress struct {
	entries   []userstore.WatchProgress
	libraries map[string]int
	// calls records each query so a test can assert the window and filter
	// the handler asked for.
	calls []fakeProgressQuery
	err   error
}

func (f *fakeProgress) ListProgressPage(_ context.Context, _ int, profileID string, status string, libraryID int, after *userstore.ProgressKey, limit int) ([]userstore.WatchProgress, bool, error) {
	f.calls = append(f.calls, fakeProgressQuery{Status: status, LibraryID: libraryID, After: after, Limit: limit})
	if f.err != nil {
		return nil, false, f.err
	}
	want := limit + 1
	var matches []userstore.WatchProgress
	for len(matches) < want {
		batch := f.page(profileID, status, after, want)
		for _, e := range batch {
			if libraryID == 0 || f.libraries == nil || f.libraries[e.MediaItemID] == libraryID {
				matches = append(matches, e)
			}
		}
		if len(batch) < want {
			break
		}
		last := batch[len(batch)-1]
		after = &userstore.ProgressKey{UpdatedAt: last.UpdatedAt, MediaItemID: last.MediaItemID}
	}
	if len(matches) > limit {
		return matches[:limit], true, nil
	}
	return matches, false, nil
}

// keyBefore reports whether e sorts strictly after the key in
// (UpdatedAt desc, MediaItemID desc) order, i.e. its own key is smaller.
func keyBefore(e userstore.WatchProgress, key userstore.ProgressKey) bool {
	if e.UpdatedAt != key.UpdatedAt {
		return e.UpdatedAt < key.UpdatedAt
	}
	return e.MediaItemID < key.MediaItemID
}

// page is the store: the status set sorted by (UpdatedAt desc, MediaItemID
// desc), strictly after the key, at most limit rows.
func (f *fakeProgress) page(profileID, status string, after *userstore.ProgressKey, limit int) []userstore.WatchProgress {
	var out []userstore.WatchProgress
	for _, e := range f.entries {
		if e.ProfileID != profileID {
			continue
		}
		switch status {
		case "completed":
			if !e.Completed {
				continue
			}
		case "in_progress":
			if e.Completed {
				continue
			}
		}
		if after != nil && !keyBefore(e, *after) {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].MediaItemID > out[j].MediaItemID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

type fakeProfiles struct {
	last             *handlers.ProfileUpdateCommand
	lastCreate       *handlers.ProfileCreateCommand
	lastDelete       *handlers.ProfileDeleteCommand
	lastSessions     *handlers.HouseholdSessionsQuery
	lastVerify       *handlers.ProfileVerifyPINCommand
	lastUpload       *fakeUpload
	lastAvatarDelete string
	view             handlers.ProfileView
	sessions         []handlers.PlaybackSessionView
	err              error
	avatarStore      bool
	// noAvatarStore makes UploadAvatar answer the v1 503 (no upload store).
	noAvatarStore bool
	// lockedPrimary, when set, is the PIN-locked primary profile the fake
	// treats as managing the household: like v1 canManageHouseholdAs it runs
	// the command's verifier for it and answers an unverified one with the
	// 403 profile_management the real service returns.
	lockedPrimary string
}

func (f *fakeProfiles) ListProfiles(_ context.Context, _ int) (handlers.ProfileListView, error) {
	if f.err != nil {
		return handlers.ProfileListView{}, f.err
	}
	return handlers.ProfileListView{Profiles: []handlers.ProfileView{f.view}, AvatarUploadEnabled: f.avatarStore}, nil
}

func (f *fakeProfiles) CreateProfile(_ context.Context, cmd handlers.ProfileCreateCommand) (handlers.ProfileView, error) {
	f.lastCreate = &cmd
	if f.err != nil {
		return handlers.ProfileView{}, f.err
	}
	if f.lockedPrimary != "" && cmd.ActiveProfileID == f.lockedPrimary {
		if err := cmd.VerifyProfile(f.lockedPrimary); err != nil {
			return handlers.ProfileView{}, &handlers.APIError{Status: http.StatusForbidden, Code: codeProfileManagement, Message: "Profile management requires verifying the primary profile PIN"}
		}
	}
	view := f.view
	view.ID, view.Name = "p-new", cmd.Request.Name
	return view, nil
}

func (f *fakeProfiles) UpdateProfile(_ context.Context, cmd handlers.ProfileUpdateCommand) (handlers.ProfileView, error) {
	f.last = &cmd
	if f.err != nil {
		return handlers.ProfileView{}, f.err
	}
	if f.lockedPrimary != "" && cmd.ActiveProfileID == f.lockedPrimary {
		if err := cmd.VerifyProfile(f.lockedPrimary); err != nil {
			return handlers.ProfileView{}, &handlers.APIError{Status: http.StatusForbidden, Code: codeProfileManagement, Message: "Profile management requires verifying the primary profile PIN"}
		}
	}
	return f.view, nil
}

// manage runs the fake's household-manager check: like v1
// canManageHouseholdAs it runs the command's verifier for a PIN-locked
// primary profile and answers an unverified one with the 403
// profile_management the real service returns.
func (f *fakeProfiles) manage(activeProfileID string, verify func(string) error) error {
	if f.err != nil {
		return f.err
	}
	if f.lockedPrimary != "" && activeProfileID == f.lockedPrimary {
		if err := verify(f.lockedPrimary); err != nil {
			return &handlers.APIError{Status: http.StatusForbidden, Code: codeProfileManagement, Message: "Profile management requires verifying the primary profile PIN"}
		}
	}
	return nil
}

func (f *fakeProfiles) DeleteProfile(_ context.Context, cmd handlers.ProfileDeleteCommand) error {
	f.lastDelete = &cmd
	if err := f.manage(cmd.ActiveProfileID, cmd.VerifyProfile); err != nil {
		return err
	}
	if cmd.ProfileID == "p-primary" {
		return &handlers.APIError{Status: http.StatusConflict, Code: "primary_profile_protected", Message: "The primary profile cannot be deleted. Delete the user account instead."}
	}
	if cmd.ProfileID != f.view.ID {
		return &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Profile not found"}
	}
	return nil
}

func (f *fakeProfiles) ListHouseholdSessions(_ context.Context, q handlers.HouseholdSessionsQuery) ([]handlers.PlaybackSessionView, error) {
	f.lastSessions = &q
	if err := f.manage(q.ActiveProfileID, q.VerifyProfile); err != nil {
		return nil, err
	}
	return f.sessions, nil
}

func (f *fakeProfiles) VerifyPIN(_ context.Context, cmd handlers.ProfileVerifyPINCommand) (handlers.ProfileVerification, error) {
	f.lastVerify = &cmd
	if f.err != nil {
		return handlers.ProfileVerification{}, f.err
	}
	if cmd.ProfileID != f.view.ID {
		return handlers.ProfileVerification{}, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Profile not found or has no PIN"}
	}
	if cmd.PIN != "1234" {
		return handlers.ProfileVerification{Valid: false}, nil
	}
	return handlers.ProfileVerification{Valid: true, ProfileToken: "pvt_fixture", ExpiresAt: fixedTime().Add(12 * time.Hour)}, nil
}

func (f *fakeProfiles) UploadAvatar(_ context.Context, up handlers.ProfileAvatarUpload) (handlers.ProfileView, error) {
	data, _ := io.ReadAll(up.File)
	f.lastUpload = &fakeUpload{ProfileID: up.ProfileID, ContentType: up.ContentType, Size: len(data)}
	if f.err != nil {
		return handlers.ProfileView{}, f.err
	}
	if f.noAvatarStore {
		return handlers.ProfileView{}, &handlers.APIError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "Avatar upload storage is not configured"}
	}
	if up.ProfileID != f.view.ID {
		return handlers.ProfileView{}, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Profile not found"}
	}
	if len(data) > 10<<20 {
		return handlers.ProfileView{}, &handlers.APIError{Status: http.StatusRequestEntityTooLarge, Code: "too_large", Message: "Avatar must be under 10 MB"}
	}
	if string(data) == "not-an-image" {
		return handlers.ProfileView{}, &handlers.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Invalid image file"}
	}
	view := f.view
	view.Avatar, view.AvatarSource, view.AvatarURL = "upload:avatars/1/p-owner/original", "upload", "https://s3.example.test/avatars/1/p-owner/256.jpg"
	return view, nil
}

func (f *fakeProfiles) DeleteAvatar(_ context.Context, _ int, profileID string) (handlers.ProfileView, error) {
	f.lastAvatarDelete = profileID
	if f.err != nil {
		return handlers.ProfileView{}, f.err
	}
	if profileID != f.view.ID {
		return handlers.ProfileView{}, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Profile not found"}
	}
	return f.view, nil
}

type fakeUpload struct {
	ProfileID   string
	ContentType string
	Size        int
}

// fixtureSession is one live session with every member set, so the fixture
// shows the whole shape.
func fixtureSession() handlers.PlaybackSessionView {
	season, episode, duration, kbps, channels, node := 1, 3, 2640, 12000, 6, 3
	return handlers.PlaybackSessionView{
		SessionID: "ps-7f3a", UserID: 1, Username: "laura", ProfileID: "p-owner", ProfileName: "Laura",
		MediaFileID: 42, RequestedMediaFileID: 42, ContentID: "ep-123", MediaTitle: "Pilot", MediaType: "episode",
		SeriesName: "Example Show", EpisodeName: "Pilot", SeasonNumber: &season, EpisodeNumber: &episode,
		PosterURL: "/api/v1/images/poster/42", PlayMethod: "transcode", ReportingNode: "node-3", NodeDisplayName: "Basement",
		FileDuration: &duration, StartedAt: fixedTime(), UpdatedAt: fixedTime().Add(time.Minute),
		PositionSeconds: 61.5, IsPaused: false, HasPlaybackControl: true,
		ClientIP: "", ClientName: "Silo for Apple TV", ClientVersion: "1.4.0", ClientBuild: "1400", ClientChannel: "release",
		ClientLabel: "Silo for Apple TV 1.4", ClientLabelFull: "Silo for Apple TV 1.4.0 (1400)", ClientUserAgent: "Silo/1.4.0",
		AudioTrackIndex: 0, TranscodeAudio: true, StreamBitrateKbps: &kbps,
		TargetResolution: "1080p", TargetVideoCodec: "h264", TargetAudioCodec: "aac", TargetAudioChannels: &channels, TargetBitrateKbps: &kbps,
		TranscodeHWAccel: "videotoolbox", ToneMapMode: "hardware", SourceContainer: "mkv", SourceBitrateKbps: &kbps,
		SourceVideoCodec: "hevc", SourceVideoResolution: "2160p", SourceAudioCodec: "truehd", SourceAudioChannels: &channels,
		SourceAudioLanguage: "eng", SourceAudioTitle: "TrueHD 7.1", SourceAudioLayout: "7.1",
		RequestedVideoCodec: "h264", RequestedVideoResolution: "1080p", VideoDecision: "transcode", AudioDecision: "transcode",
		EffectivePlayMethod: "transcode", IsJellyfinClient: false,
		RoutingWorkload: "video_transcode", RoutingExecution: "prefer_worker", RoutingExecutionNodeID: &node, RoutingExecutionNodeName: "Basement",
		RoutingEgress: "prefer_proxy", RoutingEgressNodeID: &node, RoutingEgressNodeName: "Basement",
	}
}

// fakeLibraries knows the libraries whose ids are in known.
type fakeLibraries struct {
	known []int
	err   error
}

func (f fakeLibraries) ExistingIDs(_ context.Context, ids []int) ([]int, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []int
	for _, id := range ids {
		if slices.Contains(f.known, id) {
			out = append(out, id)
		}
	}
	return out, nil
}

type fakeAdminUsers struct {
	users []handlers.AdminUserView
	err   error
}

// ListAdminUsersPage mirrors the handler seam: users are kept in id order,
// the page starts strictly after afterID, and has_more is decided by the
// limit+1 probe.
func (f fakeAdminUsers) ListAdminUsersPage(_ context.Context, afterID, limit int) ([]handlers.AdminUserView, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	page := make([]handlers.AdminUserView, 0, limit)
	for _, u := range f.users {
		if u.ID <= afterID {
			continue
		}
		if len(page) == limit {
			return page, true, nil
		}
		page = append(page, u)
	}
	return page, false, nil
}

var errStore = errors.New("store unavailable")

func fixedTime() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 678_000_000, time.UTC) }

func fixtureProfileView() handlers.ProfileView {
	return handlers.ProfileView{
		ID: "p-owner", Name: "Laura", Avatar: "preset:fox", AvatarURL: "/avatars/presets/fox.png", AvatarSource: "preset",
		IsPrimary: true, QualityPreference: "auto", Language: "en", SubtitleMode: "auto",
		AutoSkipIntro: true, AllowedLibraryIDs: []int{3}, MaxPlaybackQuality: "1080p",
		CreatedAt: "2026-01-02T03:04:05Z", UpdatedAt: "2026-01-02T03:04:05Z",
	}
}

// pilotDeps is parityDeps with every pilot service wired to a fake.
func pilotDeps(progress *fakeProgress, profiles *fakeProfiles) Dependencies {
	deps := parityDeps(false)
	deps.Accounts = fakeAccounts{
		users: map[int]handlers.UserView{
			1: {ID: 1, Username: "laura", Email: "laura@example.test", Role: "user", Permissions: []string{"marker_edit"}, DownloadAllowed: true},
			2: {ID: 2, Username: "ada", Email: "ada@example.test", Role: "admin", Permissions: []string{"marker_edit", "metadata_curation"}},
		},
	}
	if progress == nil {
		progress = &fakeProgress{}
	}
	deps.Progress = progress
	if profiles == nil {
		profiles = &fakeProfiles{view: fixtureProfileView(), sessions: []handlers.PlaybackSessionView{fixtureSession()}}
	}
	deps.Profiles = profiles
	deps.Libraries = fakeLibraries{known: []int{1, 2, 3, 4}}
	deps.ProfileSections = &fakeProfileSections{rows: fixtureSectionOverrides()}
	deps.SectionFlags = fakeSectionFlags{allow: true}
	two, yes := 2, true
	groupID := int64(2)
	last := fixedTime()
	deps.AdminUsers = fakeAdminUsers{users: []handlers.AdminUserView{
		{ID: 1, Username: "laura", Email: "laura@example.test", Role: "user", Permissions: []string{}, Enabled: true,
			MaxStreams: &two, DownloadAllowed: &yes, MaxProfiles: 5, AccessGroupID: &groupID,
			EffectivePolicy: handlers.EffectivePolicyView{LibraryIDs: []int{3}, MaxPlaybackQuality: "1080p", MaxStreams: 2, TranscodeAllowed: true, Permissions: []string{}},
			CreatedAt:       fixedTime(), UpdatedAt: fixedTime(), LastActiveAt: &last},
		{ID: 2, Username: "ada", Role: "admin", Permissions: []string{"marker_edit"}, Enabled: true, LibraryIDs: []int{}, MaxProfiles: 5,
			EffectivePolicy: handlers.EffectivePolicyView{Permissions: []string{"marker_edit", "metadata_curation"}},
			CreatedAt:       fixedTime(), UpdatedAt: fixedTime()},
	}}
	return deps
}

// fakeProfileSections stores one override set and resolves a fixed page.
type fakeProfileSections struct {
	rows       []userstore.SectionOverride
	lastQuery  handlers.SectionOverridesQuery
	lastWrites []handlers.SectionOverrideWrite
	lastFilter mediacatalog.AccessFilter
	reset      bool
	err        error
}

func (f *fakeProfileSections) ListProfileOverrides(_ context.Context, q handlers.SectionOverridesQuery) ([]userstore.SectionOverride, error) {
	f.lastQuery = q
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

func (f *fakeProfileSections) SaveProfileOverrides(_ context.Context, q handlers.SectionOverridesQuery, writes []handlers.SectionOverrideWrite) error {
	f.lastQuery, f.lastWrites = q, writes
	return f.err
}

func (f *fakeProfileSections) ResetProfileOverrides(_ context.Context, q handlers.SectionOverridesQuery) error {
	f.lastQuery, f.reset = q, true
	return f.err
}

func (f *fakeProfileSections) ResolveProfileSectionSettings(_ context.Context, userID int, profileID, scope string, libraryID *int, filter mediacatalog.AccessFilter) ([]sections.ResolvedSection, error) {
	f.lastQuery = handlers.SectionOverridesQuery{UserID: userID, ProfileID: profileID, Scope: scope}
	if libraryID != nil {
		f.lastQuery.LibraryID = strconv.Itoa(*libraryID)
	}
	f.lastFilter = filter
	if f.err != nil {
		return nil, f.err
	}
	return []sections.ResolvedSection{
		{ID: "s-continue", SectionType: "continue_watching", Title: "Continue Watching", ItemLimit: 20, Position: 0, Customized: true, Hidden: true},
		{ID: "u-gems", SectionType: "hidden_gems", Title: "Hidden gems", ItemLimit: 12, Position: 1, IsCustom: true, Config: json.RawMessage(`{"library_ids":[3]}`)},
	}, nil
}

type fakeSectionFlags struct{ allow bool }

func (f fakeSectionFlags) AllowProfileCustomSections(context.Context) bool { return f.allow }

func fixtureSectionOverrides() []userstore.SectionOverride {
	pos, featured, limit := 2, false, 10
	return []userstore.SectionOverride{
		{ID: "o-1", ProfileID: "p-owner", Scope: "home", SectionID: "s-continue", Position: &pos, Hidden: true, Featured: &featured, ItemLimit: &limit, Title: "Keep watching", CreatedAt: "2026-01-02T03:04:05Z", UpdatedAt: "2026-01-02T03:04:05Z"},
		{ID: "o-2", ProfileID: "p-owner", Scope: "home", IsUserAdded: true, UserSectionType: "hidden_gems", UserConfig: `{"library_ids":[3]}`, UserTitle: "Hidden gems"},
	}
}
