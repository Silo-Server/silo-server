import { useMemo } from "react";
import { TriangleAlert } from "lucide-react";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { FieldGroup } from "./FieldGroup";
import { SaveBar } from "./SaveBar";
import { SettingField } from "./SettingField";

const KEYS = [
  "notifications.release_events_enabled",
  "notifications.fanout_enabled",
  "notifications.ui_enabled",
  "notifications.webhooks_enabled",
  "notifications.web_push_enabled",
  "notifications.fanout.settle_seconds",
  "notifications.fanout.max_series_burst",
  "notifications.fanout.max_event_age_hours",
  "notifications.webhooks.max_per_profile",
  "notifications.webhooks.allow_private_destinations",
  "notifications.webhooks.deliveries_per_minute_per_profile",
  "notifications.email_enabled",
  "notifications.email.allow_per_episode",
  "notifications.email.digest_hour",
  "notifications.email.external_url",
  "notifications.retention.read_days",
  "notifications.retention.unread_days",
  "notifications.retention.event_days",
];

export default function NotificationsAdminSettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });

  if (form.isLoading) return <div>Loading...</div>;

  // Kill switches default to enabled when unset; the backend treats any
  // unrecognized value as the default, so an empty stored value means "on".
  const toggleValue = (key: string) => form.getValue(key) || "true";
  // Numeric settings fall back to their server-side defaults when unset;
  // surface the effective default instead of an empty input.
  const numberValue = (key: string, fallback: string) => form.getValue(key) || fallback;

  const allowPrivate =
    form.getValue("notifications.webhooks.allow_private_destinations") === "true";

  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">Notifications</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Operational controls for the notification system. All settings apply live — no restart
          needed. Per-profile preferences live in each user&apos;s own notification settings.
        </p>
      </div>

      <div className="flex-1 space-y-6">
        <FieldGroup label="Kill Switches">
          <SettingField
            label="Release Events"
            hint="Record new-episode availability during library scans. Off stops new notifications at the source."
            type="toggle"
            value={toggleValue("notifications.release_events_enabled")}
            onChange={(v) => form.setValue("notifications.release_events_enabled", v)}
          />
          <SettingField
            label="Fanout"
            hint="Deliver recorded events to interested profiles. Off pauses delivery; events queue until re-enabled or they expire."
            type="toggle"
            value={toggleValue("notifications.fanout_enabled")}
            onChange={(v) => form.setValue("notifications.fanout_enabled", v)}
          />
          <SettingField
            label="Notification UI"
            hint="Advertise the inbox and preferences to clients. Off hides the notification UI everywhere."
            type="toggle"
            value={toggleValue("notifications.ui_enabled")}
            onChange={(v) => form.setValue("notifications.ui_enabled", v)}
          />
          <SettingField
            label="Web Push"
            hint="Deliver browser push notifications to subscribed devices."
            type="toggle"
            value={toggleValue("notifications.web_push_enabled")}
            onChange={(v) => form.setValue("notifications.web_push_enabled", v)}
          />
        </FieldGroup>

        <FieldGroup label="Fanout Tuning">
          <SettingField
            label="Settle Delay (seconds)"
            hint="How long an event must sit before fanout claims it, so one scan's episodes batch together (default 30)"
            type="number"
            value={numberValue("notifications.fanout.settle_seconds", "30")}
            onChange={(v) => form.setValue("notifications.fanout.settle_seconds", v)}
          />
          <SettingField
            label="Max Series Burst"
            hint="Max notifications per series per batch; the rest are suppressed to avoid floods (default 3)"
            type="number"
            value={numberValue("notifications.fanout.max_series_burst", "3")}
            onChange={(v) => form.setValue("notifications.fanout.max_series_burst", v)}
          />
          <SettingField
            label="Max Event Age (hours)"
            hint="Events older than this are dropped instead of delivered late, e.g. after extended downtime (default 72)"
            type="number"
            value={numberValue("notifications.fanout.max_event_age_hours", "72")}
            onChange={(v) => form.setValue("notifications.fanout.max_event_age_hours", v)}
          />
        </FieldGroup>

        <FieldGroup label="Webhook Guards">
          <SettingField
            label="Outbound Webhooks"
            hint="Let users create personal webhooks (Discord, generic) that receive their notifications. Off by default; sends server-originated requests to user-chosen URLs."
            type="toggle"
            value={form.getValue("notifications.webhooks_enabled") || "false"}
            onChange={(v) => form.setValue("notifications.webhooks_enabled", v)}
          />
          <SettingField
            label="Max Webhooks Per Profile"
            hint="How many webhooks a single profile may create (default 10)"
            type="number"
            value={numberValue("notifications.webhooks.max_per_profile", "10")}
            onChange={(v) => form.setValue("notifications.webhooks.max_per_profile", v)}
          />
          <SettingField
            label="Deliveries Per Minute Per Profile"
            hint="Webhook delivery rate limit; over-limit notifications still reach the inbox (default 60)"
            type="number"
            value={numberValue("notifications.webhooks.deliveries_per_minute_per_profile", "60")}
            onChange={(v) =>
              form.setValue("notifications.webhooks.deliveries_per_minute_per_profile", v)
            }
          />
          <SettingField
            label="Allow Private Destinations"
            hint="Disables the SSRF guard so webhooks may target private and LAN addresses. Development only."
            type="toggle"
            value={form.getValue("notifications.webhooks.allow_private_destinations")}
            onChange={(v) => form.setValue("notifications.webhooks.allow_private_destinations", v)}
          />
          {allowPrivate && (
            <div className="flex items-start gap-2 py-3 text-xs text-amber-500">
              <TriangleAlert className="mt-0.5 h-4 w-4 flex-shrink-0" />
              <p>
                Private destinations are allowed: any user with webhook access can make this server
                send requests to internal network addresses. Leave this off outside development.
              </p>
            </div>
          )}
        </FieldGroup>

        <FieldGroup label="Email">
          <SettingField
            label="Email Notifications"
            hint="Deliver notifications by email to accounts that opt in. Requires SMTP to be configured on the Email page."
            type="toggle"
            value={toggleValue("notifications.email_enabled")}
            onChange={(v) => form.setValue("notifications.email_enabled", v)}
          />
          <SettingField
            label="Allow Per-Episode Email"
            hint="Let users choose an email per episode instead of the daily digest. Off coerces those accounts to the digest."
            type="toggle"
            value={toggleValue("notifications.email.allow_per_episode")}
            onChange={(v) => form.setValue("notifications.email.allow_per_episode", v)}
          />
          <SettingField
            label="Digest Hour"
            hint="Hour of day (0-23, server time) when daily digest emails go out (default 8)"
            type="number"
            value={numberValue("notifications.email.digest_hour", "8")}
            onChange={(v) => form.setValue("notifications.email.digest_hour", v)}
          />
          <SettingField
            label="External URL"
            hint="Public base URL of this server (e.g. https://silo.example.com) used for links inside emails. Empty sends emails without links."
            type="text"
            value={form.getValue("notifications.email.external_url")}
            onChange={(v) => form.setValue("notifications.email.external_url", v)}
          />
        </FieldGroup>

        <FieldGroup label="Retention">
          <SettingField
            label="Read Notifications (days)"
            hint="How long read inbox entries are kept (default 90)"
            type="number"
            value={numberValue("notifications.retention.read_days", "90")}
            onChange={(v) => form.setValue("notifications.retention.read_days", v)}
          />
          <SettingField
            label="Unread Notifications (days)"
            hint="How long unread inbox entries are kept (default 180)"
            type="number"
            value={numberValue("notifications.retention.unread_days", "180")}
            onChange={(v) => form.setValue("notifications.retention.unread_days", v)}
          />
          <SettingField
            label="Processed Events (days)"
            hint="How long processed release events are kept for debugging (default 30)"
            type="number"
            value={numberValue("notifications.retention.event_days", "30")}
            onChange={(v) => form.setValue("notifications.retention.event_days", v)}
          />
        </FieldGroup>
      </div>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={form.save}
        onDiscard={form.discard}
        isSaving={form.isSaving}
        restartRequired={form.restartRequired}
      />
    </div>
  );
}
