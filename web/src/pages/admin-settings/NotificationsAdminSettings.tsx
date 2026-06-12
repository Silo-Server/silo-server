import { useMemo, useState } from "react";
import { Check, Copy, ExternalLink, Loader2, Send, TriangleAlert } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/api/client";
import { Button } from "@/components/ui/button";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { FieldGroup } from "./FieldGroup";
import { SaveBar } from "./SaveBar";
import { SettingField } from "./SettingField";
import ServerNotificationChannels from "./ServerNotificationChannels";

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
  "notifications.discord_enabled",
  "notifications.discord.allow_per_episode",
  "notifications.discord.digest_hour",
  "notifications.server_channels_enabled",
  "notifications.server_channels.batch_seconds",
  "discord.client_id",
  "discord.client_secret",
  "discord.bot_token",
  "notifications.retention.read_days",
  "notifications.retention.unread_days",
  "notifications.retention.event_days",
];

interface DiscordTestResult {
  ok: boolean;
  duration_ms: number;
  message?: string;
}

/**
 * Invite link for adding the bot to a Discord server. Membership alone is
 * enough to DM, so no permissions are requested.
 */
function discordInviteUrl(clientId: string): string {
  return `https://discord.com/oauth2/authorize?client_id=${encodeURIComponent(clientId)}&scope=bot&permissions=0`;
}

function InviteBotRow({ clientId }: { clientId: string }) {
  const [copied, setCopied] = useState(false);
  const trimmed = clientId.trim();

  return (
    <div className="space-y-2 py-2">
      <div className="flex flex-wrap gap-1.5">
        <Button
          variant="outline"
          size="sm"
          disabled={!trimmed}
          onClick={() => window.open(discordInviteUrl(trimmed), "_blank", "noopener,noreferrer")}
        >
          <ExternalLink className="mr-1.5 h-3.5 w-3.5" />
          Invite bot to server
        </Button>
        <Button
          variant="outline"
          size="sm"
          disabled={!trimmed}
          onClick={() => {
            void navigator.clipboard.writeText(discordInviteUrl(trimmed));
            setCopied(true);
            toast.success("Invite link copied");
            window.setTimeout(() => setCopied(false), 2000);
          }}
        >
          {copied ? (
            <Check className="mr-1.5 h-3.5 w-3.5" />
          ) : (
            <Copy className="mr-1.5 h-3.5 w-3.5" />
          )}
          Copy link
        </Button>
      </div>
      <div className="text-muted-foreground text-xs">
        {trimmed
          ? "Anyone with Manage Server permission on your Discord server can open this link to add the bot. Users must be members of that server to receive DMs."
          : "Enter the Client ID above to generate the invite link."}
      </div>
    </div>
  );
}

function TestDiscordRow({ unsaved }: { unsaved: boolean }) {
  const [pending, setPending] = useState(false);
  const [result, setResult] = useState<DiscordTestResult | null>(null);

  const runTest = async () => {
    setPending(true);
    setResult(null);
    try {
      const response = await api<DiscordTestResult>("/admin/notifications/discord/test", {
        method: "POST",
      });
      setResult(response);
      if (response.ok) {
        toast.success("Discord bot connected");
      }
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Test request failed");
    } finally {
      setPending(false);
    }
  };

  return (
    <div className="space-y-2 py-2">
      <Button
        variant="outline"
        size="sm"
        disabled={pending || unsaved}
        onClick={() => void runTest()}
      >
        {pending ? (
          <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
        ) : (
          <Send className="mr-1.5 h-3.5 w-3.5" />
        )}
        Test bot token
      </Button>
      {unsaved && (
        <div className="text-muted-foreground text-xs">
          Save your changes first — the test uses the saved credentials.
        </div>
      )}
      {result && (
        <div className={`text-xs ${result.ok ? "text-emerald-500" : "text-amber-500"}`}>
          {result.ok ? "Success" : "Failed"} ({result.duration_ms}ms)
          {result.message ? ` — ${result.message}` : ""}
        </div>
      )}
    </div>
  );
}

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
  // Discord is opt-in (default off); the rest of its settings only appear
  // once the bot is enabled.
  const discordEnabled = form.getValue("notifications.discord_enabled") === "true";
  // The test endpoint reads the SAVED credentials; testing with unsaved
  // edits in the form would silently test the old values.
  const discordCredentialsDirty = [
    "discord.client_id",
    "discord.client_secret",
    "discord.bot_token",
  ].some(form.isDirty);

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

        <FieldGroup label="Discord">
          <SettingField
            label="Discord Bot"
            hint="Let users link their Discord account and receive notifications as bot DMs. Off by default; while off, Discord notifications are hidden everywhere."
            type="toggle"
            value={form.getValue("notifications.discord_enabled") || "false"}
            onChange={(v) => form.setValue("notifications.discord_enabled", v)}
          />
          {discordEnabled && (
            <>
              <div className="text-muted-foreground space-y-1.5 py-2 text-xs leading-relaxed">
                <p>Set up at discord.com/developers/applications:</p>
                <ol className="list-decimal space-y-1.5 pl-4">
                  <li>Create an application (or open an existing one).</li>
                  <li>
                    OAuth2 page: copy the <strong>Client ID</strong>, reset and copy the{" "}
                    <strong>Client Secret</strong>, and under Redirects add
                    <code className="bg-muted mx-1 rounded px-1">
                      {"<public URL>"}/api/v1/notifications/discord/link/callback
                    </code>
                    using this server&apos;s public URL (SILO_PUBLIC_URL) — it must match exactly.
                  </li>
                  <li>
                    Bot page: reset and copy the <strong>Token</strong>. Leave all Privileged
                    Gateway Intents (Presence, Server Members, Message Content) <strong>off</strong>{" "}
                    — Silo never connects to the gateway; it only sends DMs.
                  </li>
                  <li>
                    Keep <strong>Requires OAuth2 Code Grant</strong> off, or the invite link below
                    won&apos;t work. Enable <strong>Public Bot</strong> only if someone other than
                    the application owner will be inviting it.
                  </li>
                  <li>
                    Paste the three credentials below, save, then use the invite button to add the
                    bot to your Discord server. It needs <strong>no role permissions</strong> —
                    membership alone lets it DM members, and users must share that server with the
                    bot to receive DMs.
                  </li>
                </ol>
              </div>
              <SettingField
                label="Allow Per-Episode DMs"
                hint="Let users choose a DM per episode instead of the daily digest. Off coerces those accounts to the digest."
                type="toggle"
                value={toggleValue("notifications.discord.allow_per_episode")}
                onChange={(v) => form.setValue("notifications.discord.allow_per_episode", v)}
              />
              <SettingField
                label="Digest Hour"
                hint="Hour of day (0-23, server time) when daily digest DMs go out (default 8)"
                type="number"
                value={numberValue("notifications.discord.digest_hour", "8")}
                onChange={(v) => form.setValue("notifications.discord.digest_hour", v)}
              />
              <SettingField
                label="Client ID"
                hint="The Discord application's OAuth2 client ID (used for account linking)"
                type="text"
                value={form.getValue("discord.client_id")}
                onChange={(v) => form.setValue("discord.client_id", v)}
              />
              <InviteBotRow clientId={form.getValue("discord.client_id")} />
              <SettingField
                label="Client Secret"
                hint="The Discord application's OAuth2 client secret"
                type="password"
                value={form.getValue("discord.client_secret")}
                sensitiveConfigured={form.sensitiveConfigured.includes("discord.client_secret")}
                onChange={(v) => form.setValue("discord.client_secret", v)}
              />
              <SettingField
                label="Bot Token"
                hint="The bot user's token (used to send DMs)"
                type="password"
                value={form.getValue("discord.bot_token")}
                sensitiveConfigured={form.sensitiveConfigured.includes("discord.bot_token")}
                onChange={(v) => form.setValue("discord.bot_token", v)}
              />
              <TestDiscordRow unsaved={discordCredentialsDirty} />
            </>
          )}
        </FieldGroup>

        <FieldGroup label="Server Channels">
          <SettingField
            label="Server Channels"
            hint="Admin-created broadcast destinations that post server-wide events (new content, request activity) to a webhook, e.g. a community Discord channel."
            type="toggle"
            value={toggleValue("notifications.server_channels_enabled")}
            onChange={(v) => form.setValue("notifications.server_channels_enabled", v)}
          />
          <SettingField
            label="Batch Window (seconds)"
            hint="How long new-content events collect before posting, so a season pack lands as one grouped message (default 300, minimum 120)"
            type="number"
            value={numberValue("notifications.server_channels.batch_seconds", "300")}
            onChange={(v) => form.setValue("notifications.server_channels.batch_seconds", v)}
          />
          <ServerNotificationChannels />
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
