package apiv2

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

// AdminAccountDeviceService lists one account's devices
// (*handlers.AdminHandler).
type AdminAccountDeviceService interface {
	ReadAdminUserDevices(context.Context, int) ([]handlers.AdminUserDeviceView, error)
}

// AdminWatchSummaryService totals one account's finalized playback attempts
// (*handlers.AdminHandler).
type AdminWatchSummaryService interface {
	ReadAdminWatchSummary(context.Context, handlers.AdminWatchSummaryQuery) (handlers.AdminWatchSummaryView, error)
}

// AdminUserDeviceProfile is one profile that used a device or saved
// settings on it.
type AdminUserDeviceProfile struct {
	ProfileID     ID              `json:"profile_id"`
	ProfileName   string          `json:"profile_name" doc:"Empty when the profile no longer exists"`
	OverrideCount int             `json:"override_count" minimum:"0"`
	LastSeenAt    NullableInstant `json:"last_seen_at"`
}

// AdminUserDevice is one device of one account: every device the app
// registered and every device that holds saved per-device settings.
type AdminUserDevice struct {
	DeviceID       string                   `json:"device_id"`
	DeviceName     string                   `json:"device_name"`
	DevicePlatform string                   `json:"device_platform"`
	LastSeenAt     NullableInstant          `json:"last_seen_at" doc:"Latest registration by the app; null when the device is known only from saved settings"`
	LastUpdated    NullableInstant          `json:"last_updated" doc:"Latest of registration and saved-setting writes"`
	OverrideCount  int                      `json:"override_count" minimum:"0" doc:"Saved per-device settings across profiles"`
	Profiles       []AdminUserDeviceProfile `json:"profiles"`
}

type AdminUserDevicesOutput struct {
	Body Collection[AdminUserDevice]
}

// AdminUserWatchSummaryInput selects the window and optional profile.
type AdminUserWatchSummaryInput struct {
	ID        ID     `path:"id"`
	Days      int    `query:"days" minimum:"1" maximum:"365" default:"30" doc:"Window length in days, ending now"`
	ProfileID string `query:"profile_id" maxLength:"1024" doc:"Only attempts by this household profile"`
}

// AdminUserWatchSummary totals an account's finalized playback attempts that
// ended in the window. It counts the attempts listAdminPlaybackHistory lists
// with the same user_id, profile_id and ended_after = since.
type AdminUserWatchSummary struct {
	Days           int             `json:"days" example:"30"`
	ProfileID      string          `json:"profile_id,omitempty" doc:"Echoes the profile filter; absent for the whole account"`
	Since          Instant         `json:"since" doc:"Start of the window: now minus days"`
	Plays          int             `json:"plays" minimum:"0" doc:"Finalized playback attempts that ended in the window"`
	CompletedPlays int             `json:"completed_plays" minimum:"0"`
	WatchedSeconds float64         `json:"watched_seconds" minimum:"0"`
	LastPlayedAt   NullableInstant `json:"last_played_at" doc:"Latest ended_at in the window; null when none"`
}

type AdminUserWatchSummaryOutput struct {
	Body AdminUserWatchSummary
}

// adminAccountTarget validates an account path id and confirms the account
// exists, so a read about an unknown account is a 404 rather than an empty
// projection.
func (reg *Registry) adminAccountTarget(ctx context.Context, raw ID) (int, *Problem) {
	svc, p := reg.adminAccounts()
	if p != nil {
		return 0, p
	}
	id, p := adminAccountID(raw)
	if p != nil {
		return 0, p
	}
	if _, err := svc.GetAdminAccount(ctx, id); err != nil {
		return 0, adminAccountError(err)
	}
	return id, nil
}

func adminUserDeviceOf(v handlers.AdminUserDeviceView) AdminUserDevice {
	out := AdminUserDevice{
		DeviceID: v.DeviceID, DeviceName: v.DeviceName, DevicePlatform: v.DevicePlatform,
		LastSeenAt: deviceMetadataInstant(v.LastSeenAt), LastUpdated: deviceMetadataInstant(v.LastUpdated),
		OverrideCount: v.OverrideCount, Profiles: make([]AdminUserDeviceProfile, 0, len(v.Profiles)),
	}
	for _, p := range v.Profiles {
		out.Profiles = append(out.Profiles, AdminUserDeviceProfile{ProfileID: ID(p.ProfileID), ProfileName: p.ProfileName, OverrideCount: p.OverrideCount, LastSeenAt: deviceMetadataInstant(v.ProfileLastSeenAt[p.ProfileID])})
	}
	return out
}

// compareAdminUserDevices orders devices most recently seen first, devices
// never seen last, then by name and id.
func compareAdminUserDevices(a, b AdminUserDevice) int {
	if a.LastSeenAt.Valid != b.LastSeenAt.Valid {
		if a.LastSeenAt.Valid {
			return -1
		}
		return 1
	}
	return cmp.Or(b.LastSeenAt.Time.Compare(a.LastSeenAt.Time.Time), cmp.Compare(a.DeviceName, b.DeviceName), cmp.Compare(a.DeviceID, b.DeviceID))
}

// registerAdminAccountInsights registers the per-account reads the account
// page summarizes: devices and watch totals.
func registerAdminAccountInsights(reg *Registry) {
	devices := adminAccountOperation(http.MethodGet, "/{id}/devices", "listAdminUserDevices", false)
	devices.Summary = "List an account's devices."
	devices.Description = "Every device the account's apps registered and every device holding saved per-device settings, most recently seen first. The collection is bounded by the account's devices and is not paginated."
	Register(reg, devices, func(ctx context.Context, in *AdminAccountInput) (*AdminUserDevicesOutput, error) {
		if reg.deps.AdminAccountDevices == nil {
			return nil, unavailable("account devices")
		}
		id, p := reg.adminAccountTarget(ctx, in.ID)
		if p != nil {
			return nil, p
		}
		rows, err := reg.deps.AdminAccountDevices.ReadAdminUserDevices(ctx, id)
		if err != nil {
			return nil, adminAccountError(err)
		}
		items := make([]AdminUserDevice, 0, len(rows))
		for _, row := range rows {
			items = append(items, adminUserDeviceOf(row))
		}
		slices.SortFunc(items, compareAdminUserDevices)
		return &AdminUserDevicesOutput{Body: Paginated(items, "")}, nil
	})
	summary := adminAccountOperation(http.MethodGet, "/{id}/watch-summary", "getAdminUserWatchSummary", false)
	summary.Summary = "Total an account's finalized plays over recent days."
	Register(reg, summary, func(ctx context.Context, in *AdminUserWatchSummaryInput) (*AdminUserWatchSummaryOutput, error) {
		if reg.deps.AdminWatchSummary == nil {
			return nil, unavailable("watch summary")
		}
		id, p := reg.adminAccountTarget(ctx, in.ID)
		if p != nil {
			return nil, p
		}
		profile := strings.TrimSpace(in.ProfileID)
		view, err := reg.deps.AdminWatchSummary.ReadAdminWatchSummary(ctx, handlers.AdminWatchSummaryQuery{UserID: id, Days: in.Days, ProfileID: profile, Now: time.Now().UTC()})
		if err != nil {
			return nil, serviceProblem(err)
		}
		return &AdminUserWatchSummaryOutput{Body: AdminUserWatchSummary{
			Days: in.Days, ProfileID: profile, Since: NewInstant(view.Since), Plays: view.Plays, CompletedPlays: view.CompletedPlays,
			WatchedSeconds: view.WatchedSeconds, LastPlayedAt: nullableInstantOf(view.LastPlayedAt),
		}}, nil
	})
}
