package push

import "context"

// settingsReader is the slice of the settings repo we need.
type settingsReader interface {
	Get(ctx context.Context, key string) (string, error)
}

type APNsConfig struct {
	P8Key, KeyID, TeamID, BundleID string
}
type FCMConfig struct {
	ServiceAccountJSON string
}
type WebPushConfig struct {
	VAPIDPublic, VAPIDPrivate, Subject string
}

func (c APNsConfig) Configured() bool {
	return c.P8Key != "" && c.KeyID != "" && c.TeamID != "" && c.BundleID != ""
}
func (c FCMConfig) Configured() bool { return c.ServiceAccountJSON != "" }
func (c WebPushConfig) Configured() bool {
	return c.VAPIDPublic != "" && c.VAPIDPrivate != "" && c.Subject != ""
}

// Config loads provider config from the (encrypted) settings repo on demand.
type Config struct{ s settingsReader }

func NewConfig(s settingsReader) *Config { return &Config{s: s} }

func (c *Config) get(ctx context.Context, key string) string {
	v, err := c.s.Get(ctx, key)
	if err != nil {
		return ""
	}
	return v
}

func (c *Config) APNs(ctx context.Context) APNsConfig {
	return APNsConfig{
		P8Key:    c.get(ctx, "push.apns.p8_key"),
		KeyID:    c.get(ctx, "push.apns.key_id"),
		TeamID:   c.get(ctx, "push.apns.team_id"),
		BundleID: c.get(ctx, "push.apns.bundle_id"),
	}
}
func (c *Config) FCM(ctx context.Context) FCMConfig {
	return FCMConfig{ServiceAccountJSON: c.get(ctx, "push.fcm.service_account_json")}
}
func (c *Config) WebPush(ctx context.Context) WebPushConfig {
	return WebPushConfig{
		VAPIDPublic:  c.get(ctx, "push.webpush.vapid_public"),
		VAPIDPrivate: c.get(ctx, "push.webpush.vapid_private"),
		Subject:      c.get(ctx, "push.webpush.subject"),
	}
}

// Status reports per-transport configured booleans (no secrets).
type Status struct {
	APNs    bool `json:"apns"`
	FCM     bool `json:"fcm"`
	WebPush bool `json:"webpush"`
}

func (c *Config) Status(ctx context.Context) Status {
	return Status{
		APNs:    c.APNs(ctx).Configured(),
		FCM:     c.FCM(ctx).Configured(),
		WebPush: c.WebPush(ctx).Configured(),
	}
}
