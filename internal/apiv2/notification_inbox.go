package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

type NotificationInboxService interface {
	NotificationPushDisplay(context.Context, string, string) (notifications.NotificationDisplay, error)
	NotificationCapabilities(context.Context) handlers.NotificationCapabilitiesView
	ListNotificationInbox(context.Context, string, bool, int, *notifications.Cursor, *notifications.Cursor) (handlers.NotificationInboxPageView, error)
	SyncNotificationInbox(context.Context, string, int, *notifications.Cursor) ([]notifications.DeliveryRowPayload, bool, int, error)
	GetNotificationInboxItem(context.Context, string, string) (notifications.DeliveryRowPayload, error)
	NotificationUnreadCount(context.Context, string) (int, error)
	MarkNotificationRead(context.Context, int, string, string) error
	MarkNotificationInboxThrough(context.Context, int, string, notifications.Cursor) error
	NotificationPreferences(context.Context, string) (notifications.Preferences, error)
	PatchNotificationPreferences(context.Context, string, notifications.PreferencePatch) (notifications.Preferences, error)
}

// NotificationItem preserves event-specific reason data while normalizing IDs
// and timestamps for the native API. Realtime retains its existing event shape.
type NotificationItem struct {
	ID              ID              `json:"id"`
	Type            string          `json:"type"`
	ProfileID       ID              `json:"profile_id"`
	LibraryID       *ID             `json:"library_id,omitempty"`
	SeriesID        *ID             `json:"series_id,omitempty"`
	EpisodeID       *ID             `json:"episode_id,omitempty"`
	SeriesTitle     string          `json:"series_title,omitempty"`
	EpisodeTitle    string          `json:"episode_title,omitempty"`
	SeasonNumber    *int            `json:"season_number,omitempty"`
	EpisodeNumber   *int            `json:"episode_number,omitempty"`
	PosterPath      string          `json:"poster_path,omitempty"`
	PosterURL       string          `json:"poster_url,omitempty"`
	PosterThumbhash string          `json:"poster_thumbhash,omitempty"`
	ReasonFlags     JSONValue       `json:"reason_flags"`
	CreatedAt       Instant         `json:"created_at"`
	ReadAt          NullableInstant `json:"read_at"`
}

func notificationItemOf(row notifications.DeliveryRowPayload) NotificationItem {
	out := NotificationItem{ID: ID(row.ID), Type: row.Type, ProfileID: ID(row.ProfileID), SeriesTitle: row.SeriesTitle, EpisodeTitle: row.EpisodeTitle, SeasonNumber: row.SeasonNumber, EpisodeNumber: row.EpisodeNumber, PosterPath: row.PosterPath, PosterURL: row.PosterURL, PosterThumbhash: row.PosterThumbhash, ReasonFlags: JSONValue(row.ReasonFlags), CreatedAt: NewInstant(row.CreatedAt)}
	if row.LibraryID != nil {
		out.LibraryID = new(ID(strconv.Itoa(*row.LibraryID)))
	}
	if row.SeriesID != nil {
		out.SeriesID = new(ID(*row.SeriesID))
	}
	if row.EpisodeID != nil {
		out.EpisodeID = new(ID(*row.EpisodeID))
	}
	if row.ReadAt != nil {
		out.ReadAt = NullableInstant{Valid: true, Time: NewInstant(*row.ReadAt)}
	}
	return out
}

type NotificationListInput struct {
	LimitParam
	Cursor string `query:"cursor"`
	Status string `query:"status" default:"all" enum:"all,unread"`
}
type NotificationSyncInput struct {
	LimitParam
	Cursor string `query:"cursor"`
}
type NotificationItemInput struct {
	// Delivery IDs are ULIDs minted by the notification store (text, not UUID).
	ID ID `path:"id" minLength:"1"`
}
type NotificationListOutput struct {
	Body struct {
		Collection[NotificationItem]
		ReadCutoff string `json:"read_cutoff"`
	}
}
type NotificationSyncOutput struct {
	Body struct {
		Collection[NotificationItem]
		SyncCursor      string `json:"sync_cursor"`
		UnreadCount     int    `json:"unread_count"`
		InitialSnapshot bool   `json:"initial_snapshot"`
	}
}
type NotificationItemOutput struct{ Body NotificationItem }
type NotificationCountOutput struct {
	Body struct {
		Count int `json:"count"`
	}
}
type NotificationCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         NotificationCapabilities
}
type NotificationPreferences struct {
	ProfileID              ID   `json:"profile_id"`
	Enabled                bool `json:"enabled"`
	NotifyFavorites        bool `json:"notify_favorites"`
	NotifyWatchlist        bool `json:"notify_watchlist"`
	NotifyContinueWatching bool `json:"notify_continue_watching"`
	NotifyNextUp           bool `json:"notify_next_up"`
}

func notificationPreferencesOf(p notifications.Preferences) NotificationPreferences {
	return NotificationPreferences{ProfileID: ID(p.ProfileID), Enabled: p.Enabled, NotifyFavorites: p.NotifyFavorites, NotifyWatchlist: p.NotifyWatchlist, NotifyContinueWatching: p.NotifyContinueWatching, NotifyNextUp: p.NotifyNextUp}
}

type NotificationPreferencesOutput struct{ Body NotificationPreferences }
type NotificationPreferencesPatch struct {
	Enabled                *bool `json:"enabled,omitempty"`
	NotifyFavorites        *bool `json:"notify_favorites,omitempty"`
	NotifyWatchlist        *bool `json:"notify_watchlist,omitempty"`
	NotifyContinueWatching *bool `json:"notify_continue_watching,omitempty"`
	NotifyNextUp           *bool `json:"notify_next_up,omitempty"`
}
type NotificationPreferencesInput struct {
	RawBody []byte
	Body    NotificationPreferencesPatch
}
type NotificationReadAllInput struct {
	Body struct {
		Through string `json:"through" minLength:"1"`
	}
}
type notificationListPosition struct {
	Before  *notifications.Cursor
	Through notifications.Cursor
}

func notificationOperation(method, path, id string) Operation {
	op := Operation{Operation: humaOp(method, Prefix+"/notifications"+path, id, "notifications", "Manage the acting profile's notification inbox."), Class: ClassProfileScoped, ServiceBacked: true, DemoRestricted: isMutatingMethod(method)}
	if method != http.MethodGet {
		op.RetrySafety = RetrySafetyNaturalIdempotent
	}
	return op
}
func (reg *Registry) notificationInbox() (NotificationInboxService, *Problem) {
	if reg.deps.NotificationInbox == nil {
		return nil, unavailable("notification inbox")
	}
	return reg.deps.NotificationInbox, nil
}
func notificationCursorScope(ctx context.Context, operation, filter string) CursorScope {
	return CursorScope{OperationID: operation, Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx), Filter: filter, Sort: "created_at,id", Tiebreaker: "id"}
}

const notificationApplePushDisplayOperation = "getNotificationApplePushDisplay"

type NotificationPushDisplay struct {
	DeliveryID ID     `json:"delivery_id"`
	Title      string `json:"title"`
	Body       string `json:"body,omitempty"`
	ThreadID   string `json:"thread_id,omitempty"`
	Category   string `json:"category"`
	URL        string `json:"url"`
}
type NotificationPushDisplayInput struct {
	// Delivery IDs are ULIDs minted by the notification store (text, not UUID).
	DeliveryID ID `path:"delivery_id" minLength:"1"`
}
type NotificationPushDisplayOutput struct{ Body NotificationPushDisplay }

func registerNotificationInbox(reg *Registry) {
	display := notificationOperation(http.MethodGet, "/push/apple/display/{delivery_id}", notificationApplePushDisplayOperation)
	display.Summary = "Read compact display metadata for an Apple push notification."
	Register(reg, display, func(ctx context.Context, in *NotificationPushDisplayInput) (*NotificationPushDisplayOutput, error) {
		svc, p := reg.notificationInbox()
		if p != nil {
			return nil, p
		}
		view, err := svc.NotificationPushDisplay(ctx, profileFrom(ctx), string(in.DeliveryID))
		if err != nil {
			return nil, serviceProblem(err)
		}
		return &NotificationPushDisplayOutput{Body: NotificationPushDisplay{DeliveryID: ID(view.DeliveryID), Title: view.Title, Body: view.Body, ThreadID: view.ThreadID, Category: view.Category, URL: view.URL}}, nil
	})
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, notificationOperation(http.MethodGet, "/capabilities", "getNotificationCapabilities"), func(ctx context.Context, _ *CapabilityInput) (*NotificationCapabilitiesOutput, error) {
		svc := reg.deps.NotificationInbox
		if svc == nil {
			body := notificationCapabilitiesOf(handlers.NotificationCapabilitiesView{})
			body.State = StateNotConfigured
			return &NotificationCapabilitiesOutput{Body: body}, nil
		}
		return &NotificationCapabilitiesOutput{Body: notificationCapabilitiesOf(svc.NotificationCapabilities(ctx))}, nil
	})
	Register(reg, notificationOperation(http.MethodGet, "", "listNotifications"), func(ctx context.Context, in *NotificationListInput) (*NotificationListOutput, error) {
		svc, p := reg.notificationInbox()
		if p != nil {
			return nil, p
		}
		scope := notificationCursorScope(ctx, "listNotifications", in.Status+"/"+strconv.Itoa(in.Limit))
		var pos notificationListPosition
		var through *notifications.Cursor
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &pos); p != nil {
				return nil, p
			}
			through = &pos.Through
		}
		view, err := svc.ListNotificationInbox(ctx, profileFrom(ctx), in.Status == "unread", in.Limit, pos.Before, through)
		if err != nil {
			return nil, serviceProblem(err)
		}
		items := make([]NotificationItem, 0, len(view.Items))
		for _, r := range view.Items {
			items = append(items, notificationItemOf(r))
		}
		next := ""
		if view.More && len(view.Items) > 0 {
			last := view.Items[len(view.Items)-1]
			next, err = cursors.Encode(scope, notificationListPosition{Before: &notifications.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}, Through: view.Through})
			if err != nil {
				return nil, serviceProblem(err)
			}
		}
		cutoff, err := cursors.Encode(notificationCursorScope(ctx, "notificationReadCutoff", ""), view.Through)
		if err != nil {
			return nil, serviceProblem(err)
		}
		out := new(NotificationListOutput)
		out.Body.Collection = Paginated(items, next)
		out.Body.ReadCutoff = cutoff
		return out, nil
	})
	Register(reg, notificationOperation(http.MethodGet, "/sync", "syncNotifications"), func(ctx context.Context, in *NotificationSyncInput) (*NotificationSyncOutput, error) {
		svc, p := reg.notificationInbox()
		if p != nil {
			return nil, p
		}
		scope := notificationCursorScope(ctx, "syncNotifications", strconv.Itoa(in.Limit))
		var since *notifications.Cursor
		if in.Cursor != "" {
			since = new(notifications.Cursor)
			if p := cursors.Decode(scope, in.Cursor, since); p != nil {
				return nil, p
			}
		}
		rows, more, count, err := svc.SyncNotificationInbox(ctx, profileFrom(ctx), in.Limit, since)
		if err != nil {
			return nil, serviceProblem(err)
		}
		items := make([]NotificationItem, 0, len(rows))
		for _, r := range rows {
			items = append(items, notificationItemOf(r))
		}
		position := since
		if len(rows) > 0 {
			last := rows[len(rows)-1]
			position = &notifications.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
		}
		if position == nil {
			position = &notifications.Cursor{CreatedAt: time.Unix(0, 0).UTC(), ID: "00000000-0000-0000-0000-000000000000"}
		}
		syncCursor, err := cursors.Encode(scope, position)
		if err != nil {
			return nil, serviceProblem(err)
		}
		next := ""
		if more {
			next = syncCursor
		}
		out := new(NotificationSyncOutput)
		out.Body.Collection = Paginated(items, next)
		out.Body.SyncCursor = syncCursor
		out.Body.UnreadCount = count
		out.Body.InitialSnapshot = since == nil
		return out, nil
	})
	Register(reg, notificationOperation(http.MethodGet, "/unread-count", "getNotificationUnreadCount"), func(ctx context.Context, _ *struct{}) (*NotificationCountOutput, error) {
		svc, p := reg.notificationInbox()
		if p != nil {
			return nil, p
		}
		count, err := svc.NotificationUnreadCount(ctx, profileFrom(ctx))
		if err != nil {
			return nil, serviceProblem(err)
		}
		out := new(NotificationCountOutput)
		out.Body.Count = count
		return out, nil
	})
	Register(reg, notificationOperation(http.MethodGet, "/{id}", "getNotification"), func(ctx context.Context, in *NotificationItemInput) (*NotificationItemOutput, error) {
		svc, p := reg.notificationInbox()
		if p != nil {
			return nil, p
		}
		row, err := svc.GetNotificationInboxItem(ctx, profileFrom(ctx), string(in.ID))
		if err != nil {
			return nil, serviceProblem(err)
		}
		return &NotificationItemOutput{Body: notificationItemOf(row)}, nil
	})
	one := notificationOperation(http.MethodPost, "/{id}/read", "markNotificationRead")
	one.DefaultStatus = 204
	Register(reg, one, func(ctx context.Context, in *NotificationItemInput) (*struct{}, error) {
		svc, p := reg.notificationInbox()
		if p != nil {
			return nil, p
		}
		if err := svc.MarkNotificationRead(ctx, claimsFrom(ctx).UserID, profileFrom(ctx), string(in.ID)); err != nil {
			return nil, serviceProblem(err)
		}
		return &struct{}{}, nil
	})
	all := notificationOperation(http.MethodPost, "/read-all", "markNotificationsRead")
	all.DefaultStatus = 204
	Register(reg, all, func(ctx context.Context, in *NotificationReadAllInput) (*struct{}, error) {
		svc, p := reg.notificationInbox()
		if p != nil {
			return nil, p
		}
		var through notifications.Cursor
		if p := cursors.Decode(notificationCursorScope(ctx, "notificationReadCutoff", ""), in.Body.Through, &through); p != nil {
			return nil, p
		}
		if err := svc.MarkNotificationInboxThrough(ctx, claimsFrom(ctx).UserID, profileFrom(ctx), through); err != nil {
			return nil, serviceProblem(err)
		}
		return &struct{}{}, nil
	})
	Register(reg, notificationOperation(http.MethodGet, "/preferences", "getNotificationPreferences"), func(ctx context.Context, _ *struct{}) (*NotificationPreferencesOutput, error) {
		svc, p := reg.notificationInbox()
		if p != nil {
			return nil, p
		}
		prefs, err := svc.NotificationPreferences(ctx, profileFrom(ctx))
		if err != nil {
			return nil, serviceProblem(err)
		}
		return &NotificationPreferencesOutput{Body: notificationPreferencesOf(prefs)}, nil
	})
	Register(reg, notificationOperation(http.MethodPut, "/preferences", "updateNotificationPreferences"), func(ctx context.Context, in *NotificationPreferencesInput) (*NotificationPreferencesOutput, error) {
		svc, p := reg.notificationInbox()
		if p != nil {
			return nil, p
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(in.RawBody, &fields); err != nil {
			return nil, NewProblem(TypeValidationFailed, "Invalid preference document.")
		}
		for _, value := range fields {
			if bytes.Equal(bytes.TrimSpace(value), jsonNull) {
				return nil, NewProblem(TypeValidationFailed, "Preference booleans cannot be null.")
			}
		}
		b := in.Body
		prefs, err := svc.PatchNotificationPreferences(ctx, profileFrom(ctx), notifications.PreferencePatch{Enabled: b.Enabled, NotifyFavorites: b.NotifyFavorites, NotifyWatchlist: b.NotifyWatchlist, NotifyContinueWatching: b.NotifyContinueWatching, NotifyNextUp: b.NotifyNextUp})
		if err != nil {
			return nil, serviceProblem(err)
		}
		return &NotificationPreferencesOutput{Body: notificationPreferencesOf(prefs)}, nil
	})
}

// Only the extension display operation admits a display token. Reuse the
// bridge's session/profile validation and both limiter positions, including
// the pre-auth budget protecting the long-lived token's session lookup.
func notificationDisplayGateChain(deps Dependencies) ([]func(http.Handler) http.Handler, string) {
	if deps.Auth == nil {
		return nil, "auth"
	}
	if deps.ViewerAccess == nil {
		return nil, "viewer access"
	}
	postAuth := func(next http.Handler) http.Handler {
		h := deps.ViewerAccess.RequireViewerAccess(apimw.RequireProfile(next))
		if deps.RateLimit != nil {
			h = deps.RateLimit(h)
		}
		return h
	}
	fallback := func(next http.Handler) http.Handler { return deps.Auth.RequireAuth(postAuth(next)) }
	chain := make([]func(http.Handler) http.Handler, 0, 2)
	if deps.RateLimit != nil {
		chain = append(chain, deps.RateLimit)
	}
	return append(chain, deps.Auth.RequireApplePushDisplayAuth(fallback, postAuth)), ""
}

// NotificationCapabilities is the v2 projection of configured notification channels.
type NotificationCapabilities struct {
	Capability
	InApp       NotificationInAppCapability          `json:"in_app"`
	ApplePush   NotificationPushCapability           `json:"apple_push"`
	AndroidPush NotificationPushCapability           `json:"android_push"`
	WebPush     NotificationWebPushCapability        `json:"web_push"`
	Webhooks    NotificationWebhookCapability        `json:"webhooks"`
	Email       NotificationAccountChannelCapability `json:"email"`
	Discord     NotificationAccountChannelCapability `json:"discord"`
}
type NotificationInAppCapability struct {
	Enabled bool `json:"enabled"`
}
type NotificationPushCapability struct {
	Available      bool     `json:"available"`
	Provider       string   `json:"provider"`
	SupportedModes []string `json:"supported_modes"`
	DisplayToken   bool     `json:"display_token,omitempty"`
}
type NotificationWebPushCapability struct {
	Available bool   `json:"available"`
	PublicKey string `json:"public_key,omitempty"`
}
type NotificationWebhookCapability struct {
	Available      bool     `json:"available"`
	MaxPerProfile  int      `json:"max_per_profile"`
	SupportedTypes []string `json:"supported_types"`
}
type NotificationAccountChannelCapability struct {
	Available  bool     `json:"available"`
	Modes      []string `json:"modes"`
	DigestHour int      `json:"digest_hour"`
}

func notificationCapabilitiesOf(v handlers.NotificationCapabilitiesView) NotificationCapabilities {
	return NotificationCapabilities{
		InApp:       NotificationInAppCapability{Enabled: v.InApp.Enabled},
		ApplePush:   NotificationPushCapability{Available: v.ApplePush.Available, Provider: v.ApplePush.Provider, SupportedModes: NonNil(v.ApplePush.SupportedModes), DisplayToken: v.ApplePush.DisplayToken},
		AndroidPush: NotificationPushCapability{Available: v.AndroidPush.Available, Provider: v.AndroidPush.Provider, SupportedModes: NonNil(v.AndroidPush.SupportedModes), DisplayToken: v.AndroidPush.DisplayToken},
		WebPush:     NotificationWebPushCapability{Available: v.WebPush.Available, PublicKey: v.WebPush.PublicKey},
		Webhooks:    NotificationWebhookCapability{Available: v.Webhooks.Available, MaxPerProfile: v.Webhooks.MaxPerProfile, SupportedTypes: NonNil(v.Webhooks.SupportedTypes)},
		Email:       NotificationAccountChannelCapability{Available: v.Email.Available, Modes: NonNil(v.Email.Modes), DigestHour: v.Email.DigestHour},
		Discord:     NotificationAccountChannelCapability{Available: v.Discord.Available, Modes: NonNil(v.Discord.Modes), DigestHour: v.Discord.DigestHour},
	}
}
func (c NotificationCapabilities) capabilityState() string {
	return enabledCapabilityState(c.InApp.Enabled || c.ApplePush.Available || c.AndroidPush.Available || c.WebPush.Available || c.Webhooks.Available || c.Email.Available || c.Discord.Available)
}
