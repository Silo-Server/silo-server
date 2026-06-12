import { useId, useMemo, useState } from "react";
import {
  Bell,
  BookOpen,
  Bot,
  Check,
  ChevronDown,
  ChevronRight,
  Copy,
  ExternalLink,
  Inbox,
  Loader2,
  Mail,
  Megaphone,
  MonitorSmartphone,
  Rss,
  Send,
  TriangleAlert,
  Webhook,
  Workflow,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { Link } from "react-router";
import { toast } from "sonner";
import { api } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { useServerNotificationChannels } from "@/hooks/queries/admin/serverNotificationChannels";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { cn } from "@/lib/utils";
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
  "notifications.discord.poster_mode",
  "notifications.server_channels_enabled",
  "notifications.server_channels.batch_seconds",
  "notifications.server_channels.mention_requesters",
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

function digestHourLabel(raw: string): string {
  const hour = Number.parseInt(raw, 10);
  const valid = Number.isInteger(hour) && hour >= 0 && hour <= 23;
  return `${String(valid ? hour : 8).padStart(2, "0")}:00`;
}

/** Small status pill shown next to a channel title while the card is collapsed. */
function Chip({
  tone = "neutral",
  children,
}: {
  tone?: "neutral" | "positive" | "warning";
  children: React.ReactNode;
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[11px] font-medium whitespace-nowrap",
        tone === "neutral" && "border-border/70 text-muted-foreground",
        tone === "positive" && "border-emerald-500/30 bg-emerald-500/10 text-emerald-500",
        tone === "warning" && "border-amber-500/30 bg-amber-500/10 text-amber-500",
      )}
    >
      {children}
    </span>
  );
}

function ZoneHeading({ title, description }: { title: string; description?: string }) {
  return (
    <div className="space-y-1 px-1">
      <h3 className="text-muted-foreground text-xs font-semibold tracking-[0.22em] uppercase">
        {title}
      </h3>
      {description && <p className="text-muted-foreground/80 text-xs">{description}</p>}
    </div>
  );
}

/** Sub-grouping label inside an expanded channel card. */
function SubsectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="text-muted-foreground/80 pt-4 pb-1 text-[11px] font-semibold tracking-wider uppercase">
      {children}
    </div>
  );
}

interface PipelineStageProps {
  icon: LucideIcon;
  title: string;
  description: string;
  /** Visually de-emphasize the stage when an upstream stage is switched off. */
  dimmed?: boolean;
  control: React.ReactNode;
}

function PipelineStage({ icon: Icon, title, description, dimmed, control }: PipelineStageProps) {
  return (
    <div className="flex items-start justify-between gap-3">
      <div className={cn("min-w-0 transition-opacity", dimmed && "opacity-50")}>
        <div className="flex items-center gap-2">
          <Icon className="text-muted-foreground h-4 w-4 shrink-0" />
          <span className="text-sm font-medium">{title}</span>
        </div>
        <p className="text-muted-foreground mt-1 text-xs leading-relaxed">{description}</p>
      </div>
      {control}
    </div>
  );
}

function PipelineArrow() {
  return (
    <div className="hidden items-center md:flex">
      <ChevronRight className="text-muted-foreground/50 h-4 w-4" />
    </div>
  );
}

interface ChannelCardProps {
  icon: LucideIcon;
  title: string;
  description: string;
  enabled: boolean;
  onEnabledChange: (enabled: boolean) => void;
  chips?: React.ReactNode;
  children?: React.ReactNode;
}

/**
 * One delivery channel: an always-visible header row (icon, title, status
 * chips, enable switch) with settings tucked behind an expandable body.
 * Settings stay editable while the channel is off so admins can configure
 * before enabling.
 */
function ChannelCard({
  icon: Icon,
  title,
  description,
  enabled,
  onEnabledChange,
  chips,
  children,
}: ChannelCardProps) {
  const [open, setOpen] = useState(false);
  const bodyId = useId();
  const expandable = children != null;

  const header = (
    <>
      <span
        className={cn(
          "flex h-10 w-10 shrink-0 items-center justify-center rounded-xl transition-colors",
          enabled ? "bg-primary/15 text-primary" : "bg-muted text-muted-foreground",
        )}
      >
        <Icon className="h-5 w-5" />
      </span>
      <span className="min-w-0 flex-1">
        <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <span className="text-sm font-medium">{title}</span>
          {chips}
        </span>
        <span className="text-muted-foreground mt-0.5 block text-xs leading-relaxed">
          {description}
        </span>
      </span>
    </>
  );

  return (
    <div className="surface-panel overflow-hidden rounded-2xl border-0">
      <div className="flex items-center gap-3 p-4 sm:px-5">
        {expandable ? (
          <button
            type="button"
            aria-expanded={open}
            aria-controls={bodyId}
            onClick={() => setOpen((current) => !current)}
            className="flex min-w-0 flex-1 cursor-pointer items-center gap-3 text-left"
          >
            {header}
            <ChevronDown
              className={cn(
                "text-muted-foreground h-4 w-4 shrink-0 transition-transform duration-200",
                open && "rotate-180",
              )}
            />
          </button>
        ) : (
          <div className="flex min-w-0 flex-1 items-center gap-3">{header}</div>
        )}
        <Switch
          checked={enabled}
          onCheckedChange={(value) => {
            onEnabledChange(value);
            // Enabling a channel usually means configuring it next.
            if (value && expandable) setOpen(true);
          }}
          aria-label={`Enable ${title}`}
        />
      </div>
      {expandable && open && (
        <div
          id={bodyId}
          className="border-border/60 animate-in fade-in-0 slide-in-from-top-1 border-t px-4 pt-1 pb-4 duration-200 sm:px-5"
        >
          {children}
        </div>
      )}
    </div>
  );
}

function DiscordSetupGuide() {
  const [open, setOpen] = useState(false);
  const guideId = useId();

  return (
    <div className="py-2">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={guideId}
        onClick={() => setOpen((current) => !current)}
        className="text-muted-foreground hover:text-foreground inline-flex cursor-pointer items-center gap-1.5 text-xs font-medium transition-colors"
      >
        <BookOpen className="h-3.5 w-3.5" />
        {open ? "Hide setup guide" : "Show setup guide"}
        <ChevronDown
          className={cn("h-3.5 w-3.5 transition-transform duration-200", open && "rotate-180")}
        />
      </button>
      {open && (
        <div
          id={guideId}
          className="text-muted-foreground animate-in fade-in-0 mt-3 space-y-1.5 text-xs leading-relaxed duration-200"
        >
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
              Bot page: reset and copy the <strong>Token</strong>. Leave all Privileged Gateway
              Intents (Presence, Server Members, Message Content) <strong>off</strong> — Silo never
              connects to the gateway; it only sends DMs.
            </li>
            <li>
              Keep <strong>Requires OAuth2 Code Grant</strong> off, or the invite link below
              won&apos;t work. Enable <strong>Public Bot</strong> only if someone other than the
              application owner will be inviting it.
            </li>
            <li>
              Paste the three credentials below, save, then use the invite button to add the bot to
              your Discord server. It needs <strong>no role permissions</strong> — membership alone
              lets it DM members, and users must share that server with the bot to receive DMs.
            </li>
          </ol>
        </div>
      )}
    </div>
  );
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
  const { data: serverChannels } = useServerNotificationChannels();

  if (form.isLoading) {
    return (
      <div className="space-y-6">
        <div className="space-y-2">
          <Skeleton className="h-7 w-44" />
          <Skeleton className="h-4 w-full max-w-xl" />
        </div>
        <Skeleton className="h-36 w-full rounded-2xl" />
        <div className="space-y-3">
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-[72px] w-full rounded-2xl" />
          ))}
        </div>
      </div>
    );
  }

  // Kill switches default to enabled when unset; the backend treats any
  // unrecognized value as the default, so an empty stored value means "on".
  const toggleValue = (key: string) => form.getValue(key) || "true";
  const isOn = (key: string) => toggleValue(key) === "true";
  const setToggle = (key: string) => (value: boolean) =>
    form.setValue(key, value ? "true" : "false");
  // Numeric settings fall back to their server-side defaults when unset;
  // surface the effective default instead of an empty input.
  const numberValue = (key: string, fallback: string) => form.getValue(key) || fallback;

  const releaseEventsOn = isOn("notifications.release_events_enabled");
  const fanoutOn = isOn("notifications.fanout_enabled");
  const uiOn = isOn("notifications.ui_enabled");
  const webPushOn = isOn("notifications.web_push_enabled");
  const emailOn = isOn("notifications.email_enabled");
  const serverChannelsOn = isOn("notifications.server_channels_enabled");
  // Discord and personal webhooks are opt-in (default off).
  const discordOn = form.getValue("notifications.discord_enabled") === "true";
  const webhooksOn = form.getValue("notifications.webhooks_enabled") === "true";

  const allowPrivate =
    form.getValue("notifications.webhooks.allow_private_destinations") === "true";
  // The test endpoint reads the SAVED credentials; testing with unsaved
  // edits in the form would silently test the old values.
  const discordCredentialsDirty = [
    "discord.client_id",
    "discord.client_secret",
    "discord.bot_token",
  ].some(form.isDirty);
  const discordCredentialsReady =
    form.getValue("discord.client_id").trim() !== "" &&
    ["discord.client_secret", "discord.bot_token"].every(
      (key) => form.getValue(key) !== "" || form.sensitiveConfigured.includes(key),
    );

  const channelStates = [uiOn, webPushOn, emailOn, discordOn, webhooksOn, serverChannelsOn];
  const enabledChannelCount = channelStates.filter(Boolean).length;

  const failingServerChannels = (serverChannels ?? []).filter(
    (channel) =>
      channel.last_failure_at != null &&
      (channel.last_success_at == null || channel.last_failure_at > channel.last_success_at),
  ).length;

  const pausedMessage = !releaseEventsOn
    ? "Release events are off — nothing new is recorded, so no notifications are produced."
    : !fanoutOn
      ? "Fanout is paused — events queue until it is re-enabled or they expire."
      : null;

  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">Notifications</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Operational controls for the notification system. All settings apply live — no restart
          needed. Per-profile preferences live in each user&apos;s own notification settings.
        </p>
      </div>

      <div className="flex-1 space-y-7">
        {/* ── Pipeline: the master switches, framed as the flow they gate ── */}
        <div className="surface-panel rounded-2xl border-0 p-4 sm:p-5">
          <div className="text-muted-foreground mb-4 text-xs font-semibold tracking-[0.22em] uppercase">
            Pipeline
          </div>
          <div className="grid gap-4 md:grid-cols-[1fr_auto_1fr_auto_1fr] md:gap-3">
            <PipelineStage
              icon={Rss}
              title="Record events"
              description="Log new-content availability during library scans. Off stops notifications at the source."
              control={
                <Switch
                  checked={releaseEventsOn}
                  onCheckedChange={setToggle("notifications.release_events_enabled")}
                  aria-label="Enable release events"
                />
              }
            />
            <PipelineArrow />
            <PipelineStage
              icon={Workflow}
              title="Fan out"
              description="Match recorded events to interested profiles and queue deliveries."
              dimmed={!releaseEventsOn}
              control={
                <Switch
                  checked={fanoutOn}
                  onCheckedChange={setToggle("notifications.fanout_enabled")}
                  aria-label="Enable fanout"
                />
              }
            />
            <PipelineArrow />
            <PipelineStage
              icon={Bell}
              title="Deliver"
              description="Hand off to the delivery channels below."
              dimmed={!releaseEventsOn || !fanoutOn}
              control={
                <Chip>
                  {enabledChannelCount}/{channelStates.length} channels on
                </Chip>
              }
            />
          </div>
          {pausedMessage && (
            <div className="mt-4 flex items-start gap-2 text-xs text-amber-500">
              <TriangleAlert className="mt-0.5 h-4 w-4 shrink-0" />
              <p>{pausedMessage}</p>
            </div>
          )}
        </div>

        {/* ── Delivery channels ── */}
        <section className="space-y-3">
          <ZoneHeading
            title="Delivery Channels"
            description="Where notifications go once fanout queues them. Channels can be configured while switched off."
          />

          <ChannelCard
            icon={Inbox}
            title="In-App"
            description="Advertise the notification inbox and preferences to web and client apps."
            enabled={uiOn}
            onEnabledChange={setToggle("notifications.ui_enabled")}
          />

          <ChannelCard
            icon={MonitorSmartphone}
            title="Web Push"
            description="Browser push notifications to subscribed devices."
            enabled={webPushOn}
            onEnabledChange={setToggle("notifications.web_push_enabled")}
          />

          <ChannelCard
            icon={Mail}
            title="Email"
            description="Notifications by email for accounts that opt in, as a daily digest or per episode."
            enabled={emailOn}
            onEnabledChange={setToggle("notifications.email_enabled")}
            chips={
              <Chip>
                Digest at {digestHourLabel(form.getValue("notifications.email.digest_hour"))}
              </Chip>
            }
          >
            <div className="text-muted-foreground border-border/60 border-b py-3 text-xs">
              Requires working SMTP — configure it on the{" "}
              <Link
                to="?tab=email"
                className="text-foreground font-medium underline-offset-2 hover:underline"
              >
                Email settings page
              </Link>
              .
            </div>
            <div className="divide-border divide-y">
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
            </div>
          </ChannelCard>

          <ChannelCard
            icon={Bot}
            title="Discord"
            description="Bot DMs for linked accounts, plus appearance for every Discord delivery surface."
            enabled={discordOn}
            onEnabledChange={setToggle("notifications.discord_enabled")}
            chips={
              discordCredentialsReady ? (
                <Chip tone="positive">Credentials configured</Chip>
              ) : (
                <Chip tone={discordOn ? "warning" : "neutral"}>Setup required</Chip>
              )
            }
          >
            <SubsectionLabel>Bot Setup</SubsectionLabel>
            <DiscordSetupGuide />
            <div className="divide-border divide-y">
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
            </div>
            <SubsectionLabel>Delivery</SubsectionLabel>
            <div className="divide-border divide-y">
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
            </div>
            <SubsectionLabel>Appearance — All Discord Surfaces</SubsectionLabel>
            <SettingField
              label="Embed Posters"
              hint="Artwork in outgoing Discord messages (personal webhooks, bot DMs, server channels). Provider CDNs serve posters from TMDB/TVDB and never reveal this server. Server storage additionally serves locally cached posters from this server's image storage — its URL becomes visible to message recipients and must be reachable from the internet."
              type="select"
              value={form.getValue("notifications.discord.poster_mode") || "provider"}
              options={[
                { value: "provider", label: "Provider CDNs only (default)" },
                { value: "server", label: "Provider CDNs + server storage" },
                { value: "off", label: "No images" },
              ]}
              onChange={(v) => form.setValue("notifications.discord.poster_mode", v)}
            />
          </ChannelCard>

          <ChannelCard
            icon={Webhook}
            title="Personal Webhooks"
            description="User-created webhooks (Discord or generic) that receive their personal notifications — the server sends requests to user-chosen URLs."
            enabled={webhooksOn}
            onEnabledChange={setToggle("notifications.webhooks_enabled")}
            chips={
              allowPrivate ? <Chip tone="warning">Private destinations allowed</Chip> : undefined
            }
          >
            <div className="divide-border divide-y">
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
                value={numberValue(
                  "notifications.webhooks.deliveries_per_minute_per_profile",
                  "60",
                )}
                onChange={(v) =>
                  form.setValue("notifications.webhooks.deliveries_per_minute_per_profile", v)
                }
              />
              <SettingField
                label="Allow Private Destinations"
                hint="Disables the SSRF guard so webhooks may target private and LAN addresses. Development only."
                type="toggle"
                value={form.getValue("notifications.webhooks.allow_private_destinations")}
                onChange={(v) =>
                  form.setValue("notifications.webhooks.allow_private_destinations", v)
                }
              />
              {allowPrivate && (
                <div className="flex items-start gap-2 py-3 text-xs text-amber-500">
                  <TriangleAlert className="mt-0.5 h-4 w-4 flex-shrink-0" />
                  <p>
                    Private destinations are allowed: any user with webhook access can make this
                    server send requests to internal network addresses. Leave this off outside
                    development.
                  </p>
                </div>
              )}
            </div>
          </ChannelCard>

          <ChannelCard
            icon={Megaphone}
            title="Server Channels"
            description="Admin-created broadcasts that post server-wide events (new content, request activity) to shared destinations, like a community Discord channel."
            enabled={serverChannelsOn}
            onEnabledChange={setToggle("notifications.server_channels_enabled")}
            chips={
              <>
                {serverChannels != null && (
                  <Chip>
                    {serverChannels.length} destination{serverChannels.length === 1 ? "" : "s"}
                  </Chip>
                )}
                {failingServerChannels > 0 && (
                  <Chip tone="warning">{failingServerChannels} failing</Chip>
                )}
              </>
            }
          >
            <div className="divide-border divide-y">
              <SettingField
                label="Batch Window (seconds)"
                hint="How long new-content events collect before posting, so a season pack lands as one grouped message (default 300, minimum 120)"
                type="number"
                value={numberValue("notifications.server_channels.batch_seconds", "300")}
                onChange={(v) => form.setValue("notifications.server_channels.batch_seconds", v)}
              />
              <SettingField
                label="Mention Requesters on Discord"
                hint="Request posts to Discord destinations @mention the requesting user when their account has linked Discord (in user notification settings). Unlinked accounts show their Silo username without a mention."
                type="toggle"
                value={form.getValue("notifications.server_channels.mention_requesters")}
                onChange={(v) =>
                  form.setValue("notifications.server_channels.mention_requesters", v)
                }
              />
              <div className="pt-3">
                <ServerNotificationChannels />
              </div>
            </div>
          </ChannelCard>
        </section>

        {/* ── Advanced ── */}
        <section className="space-y-3">
          <ZoneHeading
            title="Advanced"
            description="Batching, flood control, and cleanup. The defaults work well for most servers."
          />
          <div className="grid gap-3 xl:grid-cols-2 xl:gap-6">
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
        </section>
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
