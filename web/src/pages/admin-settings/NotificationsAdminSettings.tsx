import { useId, useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
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
  KeyRound,
  Loader2,
  Mail,
  Megaphone,
  MonitorSmartphone,
  RadioTower,
  Rss,
  Send,
  TriangleAlert,
  Webhook,
  Workflow,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/api/client";
import { Button } from "@/components/ui/button";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { SecretField } from "@/components/settings/SecretField";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { SettingsSubheading } from "@/components/settings/SettingsSubheading";
import { adminKeys } from "@/hooks/queries/keys";
import { useServerNotificationChannels } from "@/hooks/queries/admin/serverNotificationChannels";
import { useUpdateServerSettings } from "@/hooks/queries/admin/settings";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { useReportUnsavedChanges } from "@/hooks/useUnsavedChanges";
import { emailReady } from "@/lib/emailReadiness";
import { copyTextToClipboard } from "@/lib/clipboard";
import { cn } from "@/lib/utils";
import { FieldGroup } from "./FieldGroup";
import { SaveBar } from "./SaveBar";
import { SettingField } from "./SettingField";
import ServerNotificationChannels from "./ServerNotificationChannels";

/** Batching and flood control; all advanced. */
const FANOUT_KEYS = [
  "notifications.fanout.settle_seconds",
  "notifications.fanout.max_series_burst",
  "notifications.fanout.max_event_age_hours",
];

/** Inbox and event cleanup; all advanced. */
const RETENTION_KEYS = [
  "notifications.retention.read_days",
  "notifications.retention.unread_days",
  "notifications.retention.event_days",
];

/** Personal webhook limits and the SSRF escape hatch; all advanced. */
const WEBHOOK_ADVANCED_KEYS = [
  "notifications.webhooks.max_per_profile",
  "notifications.webhooks.deliveries_per_minute_per_profile",
  "notifications.webhooks.allow_private_destinations",
];

/**
 * Outbound mail. The SMTP server used to be its own tab; it now lives in the
 * Email channel card because "email notifications don't arrive" is always an
 * SMTP question.
 */
const EMAIL_KEYS = [
  "email.enabled",
  "email.smtp_host",
  "email.smtp_port",
  "email.smtp_security",
  "email.smtp_username",
  "email.smtp_password",
  "email.from_address",
  "email.from_name",
];

/**
 * The Discord application. Saved by its own card inside the Discord channel
 * (a bot token has to be testable before it is committed), but listed here so
 * one form owns every value this page reads.
 */
const DISCORD_APP_KEYS = ["discord.client_id", "discord.client_secret", "discord.bot_token"];

const KEYS = [
  "notifications.release_events_enabled",
  "notifications.fanout_enabled",
  "notifications.ui_enabled",
  "notifications.webhooks_enabled",
  "notifications.web_push_enabled",
  "notifications.apple_push_delivery_enabled",
  "notifications.android_push_delivery_enabled",
  // Relay lifecycle fields are read for status but are never edited through
  // the shared settings form; credential endpoints replace them atomically.
  "notifications.push_relay_url",
  "notifications.push_relay_deployment_id",
  "notifications.push_relay_expires_at",
  "notifications.push_relay_key_prefix",
  "notifications.push_relay_reregistration_required",
  ...FANOUT_KEYS,
  ...WEBHOOK_ADVANCED_KEYS,
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
  ...RETENTION_KEYS,
  ...EMAIL_KEYS,
  ...DISCORD_APP_KEYS,
];

interface EmailTestResult {
  ok: boolean;
  duration_ms: number;
  message?: string;
}

interface AppleRelayRegisterResult {
  relay_url: string;
  deployment_id: string;
  key_prefix: string;
  api_key_configured: boolean;
  relay_request_id?: string;
  apns_topics?: string[];
  expires_at: string;
}

const DEFAULT_PUSH_RELAY_URL = "https://push.siloserver.org";

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

function ZoneHeading({ title }: { title: string }) {
  return (
    <h3 className="text-muted-foreground px-1 text-xs font-semibold tracking-[0.22em] uppercase">
      {title}
    </h3>
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

/** Sends a real message through the saved SMTP settings. */
function TestEmailRow() {
  const [recipient, setRecipient] = useState("");
  const [pending, setPending] = useState(false);
  const [result, setResult] = useState<EmailTestResult | null>(null);

  const sendTest = async () => {
    setPending(true);
    setResult(null);
    try {
      const response = await api<EmailTestResult>("/admin/email/test", {
        method: "POST",
        body: JSON.stringify({ to: recipient.trim() }),
      });
      setResult(response);
      if (response.ok) {
        toast.success("Test email sent");
      }
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Test request failed");
    } finally {
      setPending(false);
    }
  };

  return (
    <div className="space-y-2 py-3">
      <div className="flex max-w-md gap-2">
        <Input
          type="email"
          aria-label="Test email recipient"
          placeholder="you@example.com"
          value={recipient}
          onChange={(event) => setRecipient(event.target.value)}
        />
        <Button
          variant="outline"
          disabled={pending || !recipient.trim()}
          onClick={() => void sendTest()}
        >
          {pending ? (
            <Loader2 className="mr-1.5 h-4 w-4 animate-spin" />
          ) : (
            <Send className="mr-1.5 h-4 w-4" />
          )}
          Send test
        </Button>
      </div>
      {result && (
        <p className={`text-xs ${result.ok ? "text-emerald-500" : "text-amber-500"}`}>
          {result.ok
            ? `Delivered to the mail server in ${result.duration_ms}ms.`
            : result.message || "Test failed."}
        </p>
      )}
      <p className="text-muted-foreground text-xs">
        Save your changes first; the test uses the saved settings.
      </p>
    </div>
  );
}

function RegisterRelayRow({
  relayURL,
  deploymentID,
  keyPrefix,
  expiresAt,
  reregistrationRequired,
  urlEdited,
  onRegistered,
}: {
  relayURL: string;
  deploymentID: string;
  keyPrefix: string;
  expiresAt: string;
  reregistrationRequired: boolean;
  urlEdited: boolean;
  onRegistered: (submittedRelayURL: string) => void;
}) {
  const queryClient = useQueryClient();
  const [pending, setPending] = useState(false);
  const [confirmClear, setConfirmClear] = useState(false);
  const [result, setResult] = useState<AppleRelayRegisterResult | null>(null);

  const configured = deploymentID.trim() !== "";
  const actionLabel = reregistrationRequired
    ? "Re-register relay"
    : configured
      ? "Rotate credential"
      : "Register relay";
  const expiration = expiresAt ? new Date(expiresAt) : null;
  const expirationValid = expiration != null && !Number.isNaN(expiration.getTime());
  const renewalStatus = expirationValid
    ? expiration.getTime() <= Date.now()
      ? "Expired; Silo renews it before the next delivery."
      : `Expires ${expiration.toLocaleString()}; Silo renews automatically.`
    : configured
      ? "Expiration unknown; Silo refreshes the credential on its next renewal."
      : "No relay credential is registered.";

  const registerRelay = async () => {
    if (pending) return;
    setPending(true);
    setResult(null);
    try {
      const response = await api<AppleRelayRegisterResult>(
        "/admin/notifications/push/relay/register",
        {
          method: "POST",
          body: JSON.stringify({
            relay_url: relayURL,
          }),
        },
      );
      setResult(response);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: adminKeys.serverSettings() }),
        queryClient.invalidateQueries({
          queryKey: [...adminKeys.serverSettings(), "sensitive-status"] as const,
        }),
      ]);
      onRegistered(relayURL);
      toast.success("Push relay registered");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Relay registration failed");
    } finally {
      setPending(false);
    }
  };

  const clearRelay = async () => {
    if (pending) return;
    setPending(true);
    setResult(null);
    try {
      await api<void>("/admin/notifications/push/relay", { method: "DELETE" });
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: adminKeys.serverSettings() }),
        queryClient.invalidateQueries({
          queryKey: [...adminKeys.serverSettings(), "sensitive-status"] as const,
        }),
      ]);
      setConfirmClear(false);
      toast.success("Push relay credential cleared");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Failed to clear relay credential");
    } finally {
      setPending(false);
    }
  };

  return (
    <div className="space-y-3 py-3">
      <SettingField
        label="Deployment ID"
        description="Created for you when you register."
        type="text"
        value={deploymentID}
        onChange={() => {}}
        disabled
      />
      <div className="flex flex-wrap items-center gap-2 py-2">
        <Button variant="outline" size="sm" disabled={pending} onClick={() => void registerRelay()}>
          {pending ? (
            <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
          ) : (
            <KeyRound className="mr-1.5 h-3.5 w-3.5" />
          )}
          {actionLabel}
        </Button>
        {configured && (
          <Button
            variant="ghost"
            size="sm"
            disabled={pending}
            onClick={() => setConfirmClear(true)}
          >
            Clear credential
          </Button>
        )}
      </div>
      {reregistrationRequired && (
        <div className="text-xs text-amber-500">
          The relay credential was rejected or revoked; re-register to create a new deployment.
        </div>
      )}
      <div className="text-muted-foreground space-y-1 text-xs">
        {keyPrefix && <div>Credential: {keyPrefix}</div>}
        <div>{renewalStatus}</div>
      </div>
      {urlEdited && (
        <div className="text-muted-foreground text-xs">Register to apply the new relay URL.</div>
      )}
      {result && (
        <div className="text-xs text-emerald-500">
          Credential ready for {result.deployment_id}
          {result.key_prefix ? `, key ${result.key_prefix}` : ""}
          {result.relay_request_id ? `, relay ${result.relay_request_id}` : ""}
        </div>
      )}
      <AlertDialog open={confirmClear} onOpenChange={setConfirmClear}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Clear the push relay credential?</AlertDialogTitle>
            <AlertDialogDescription>
              Mobile push delivery stops until a relay is registered again.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={pending}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              disabled={pending}
              onClick={() => void clearRelay()}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {pending ? "Clearing..." : "Clear credential"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Discord application credentials
// ---------------------------------------------------------------------------

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

  // The copy can genuinely fail — a denied permission, or a browser that only
  // exposes the async clipboard on a secure origin, which a LAN server reached
  // over plain HTTP is not. Claiming success there sends the admin off to paste
  // an invite link they do not have.
  async function copyInviteLink() {
    try {
      await copyTextToClipboard(discordInviteUrl(trimmed));
      setCopied(true);
      toast.success("Invite link copied");
      window.setTimeout(() => setCopied(false), 2000);
    } catch {
      toast.error("Couldn't copy the invite link — select it and copy it manually");
    }
  }

  return (
    <div className="space-y-2 py-2">
      <div className="flex flex-wrap gap-1.5">
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={!trimmed}
          onClick={() => window.open(discordInviteUrl(trimmed), "_blank", "noopener,noreferrer")}
        >
          <ExternalLink className="mr-1.5 h-3.5 w-3.5" />
          Invite bot to server
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={!trimmed}
          onClick={() => void copyInviteLink()}
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
          ? "Users must be in that server to receive DMs."
          : "Enter the Client ID to generate the invite link."}
      </div>
    </div>
  );
}

/**
 * The Discord application itself — client id, secret, bot token. It lives in
 * the Discord channel card because "why didn't my DM arrive" is always a
 * question about these three values, and it saves on its own so the bot token
 * can be tested before the page's other edits are committed.
 */
function DiscordAppCredentials({
  savedClientId,
  sensitiveConfigured,
  restartKeys,
}: {
  savedClientId: string;
  sensitiveConfigured: string[];
  restartKeys: ReturnType<typeof useRestartKeys>;
}) {
  const updateSettings = useUpdateServerSettings();
  // `null` follows the saved value; a draft is only pinned while the admin is
  // editing, so a refetch cannot overwrite typing in progress.
  const [clientIdDraft, setClientIdDraft] = useState<string | null>(null);
  const [clientSecret, setClientSecret] = useState("");
  const [botToken, setBotToken] = useState("");
  const [testResult, setTestResult] = useState<{ success: boolean; message: string } | null>(null);
  const [testing, setTesting] = useState(false);
  const [confirmClear, setConfirmClear] = useState(false);

  const clientId = clientIdDraft ?? savedClientId;
  const configuredKeys = new Set(sensitiveConfigured);
  const secretConfigured = configuredKeys.has("discord.client_secret");
  const tokenConfigured = configuredKeys.has("discord.bot_token");
  const ready = savedClientId.trim() !== "" && secretConfigured && tokenConfigured;
  const unsaved =
    clientId !== savedClientId || clientSecret.trim() !== "" || botToken.trim() !== "";
  // The card's drafts live outside useSettingsForm, so the navigation guard
  // and the reload prompt only see them through this report.
  useReportUnsavedChanges(unsaved);
  const anyStored = savedClientId.trim() !== "" || secretConfigured || tokenConfigured;

  async function save() {
    const updates: Record<string, string> = { "discord.client_id": clientId };
    if (clientSecret.trim() !== "") updates["discord.client_secret"] = clientSecret;
    if (botToken.trim() !== "") updates["discord.bot_token"] = botToken;
    try {
      await updateSettings.mutateAsync(updates);
      setClientIdDraft(null);
      setClientSecret("");
      setBotToken("");
      setTestResult(null);
      toast.success("Discord credentials saved");
    } catch {
      // The mutation surfaces the API error.
    }
  }

  async function clearAll() {
    try {
      await updateSettings.mutateAsync({
        "discord.client_id": "",
        "discord.client_secret": "",
        "discord.bot_token": "",
      });
      setClientIdDraft(null);
      setClientSecret("");
      setBotToken("");
      setTestResult(null);
      toast.success("Discord credentials cleared");
    } catch {
      // The mutation surfaces the API error.
    }
  }

  async function runTest() {
    setTesting(true);
    setTestResult(null);
    try {
      const response = await api<DiscordTestResult>("/admin/notifications/discord/test", {
        method: "POST",
      });
      setTestResult({
        success: response.ok,
        message: `${response.ok ? "Success" : "Failed"} (${response.duration_ms}ms)${
          response.message ? `: ${response.message}` : ""
        }`,
      });
    } catch (error) {
      setTestResult({
        success: false,
        message: error instanceof Error ? error.message : "Test request failed.",
      });
    } finally {
      setTesting(false);
    }
  }

  return (
    <>
      <SettingsSubheading>Application</SettingsSubheading>
      <DiscordSetupGuide />
      <div className="settings-field-list">
        <SettingField
          label="Client ID"
          value={clientId}
          onChange={setClientIdDraft}
          description="From the application's OAuth2 page."
          restartRequired={restartKeys.has("discord.client_id")}
        />
      </div>
      <InviteBotRow clientId={clientId} />
      <div className="settings-field-list">
        <SecretField
          label="Client secret"
          value={clientSecret}
          configured={secretConfigured}
          onChange={setClientSecret}
          hint="From the application's OAuth2 page."
          restartRequired={restartKeys.has("discord.client_secret")}
        />
        <SecretField
          label="Bot token"
          value={botToken}
          configured={tokenConfigured}
          onChange={setBotToken}
          hint="From the application's Bot page."
          restartRequired={restartKeys.has("discord.bot_token")}
        />
      </div>
      <div className="flex flex-wrap items-center gap-2 py-3">
        <Button
          type="button"
          size="sm"
          onClick={() => void save()}
          disabled={updateSettings.isPending}
        >
          {updateSettings.isPending ? "Saving..." : "Save"}
        </Button>
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() => void runTest()}
          disabled={testing || unsaved || !ready}
        >
          {testing ? "Testing..." : "Test bot token"}
        </Button>
        {anyStored && (
          <Button type="button" size="sm" variant="ghost" onClick={() => setConfirmClear(true)}>
            Clear credentials
          </Button>
        )}
      </div>
      {testResult && (
        <p
          role="status"
          aria-live="polite"
          className={cn(
            "pb-2 text-xs",
            testResult.success
              ? "text-green-600 dark:text-green-400"
              : "text-red-600 dark:text-red-400",
          )}
        >
          {testResult.message}
        </p>
      )}
      {unsaved && (
        <p className="text-muted-foreground pb-2 text-xs">
          Save first; the test uses the stored credentials.
        </p>
      )}
      <AlertDialog open={confirmClear} onOpenChange={setConfirmClear}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Clear Discord app credentials?</AlertDialogTitle>
            <AlertDialogDescription>
              Account linking and Discord direct messages stop working immediately.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              onClick={() => {
                void clearAll();
                setConfirmClear(false);
              }}
            >
              Clear
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function MobilePushPrivacyDisclosure() {
  return (
    <div className="space-y-2 py-3">
      <div className="text-sm font-medium">Privacy disclosure</div>
      <div className="text-muted-foreground space-y-2 text-xs leading-relaxed">
        <p>
          If you enable push notifications, your Silo Server sends a content-free request to Silo's
          push relay so Silo can deliver notifications through Apple Push Notification service or
          Firebase Cloud Messaging.
        </p>
        <p>
          The relay does not receive notification titles, message bodies, media names, user names,
          profile names, or your server URL. It does process technical metadata needed to deliver
          and operate the service, including an opaque deployment identifier, push delivery timing,
          request status, app topic, the IP address your self-hosted Silo Server uses to contact the
          relay, and a hashed device push token. Apple or Google may also process standard push
          delivery metadata for their platform.
        </p>
        <p>
          Push notifications are generic; the app fetches private content directly from your Silo
          Server after receiving the push.
        </p>
      </div>
    </div>
  );
}

export default function NotificationsAdminSettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
  const restartKeys = useRestartKeys();
  const { data: serverChannels } = useServerNotificationChannels();
  // Local draft for the relay URL; null means "show the saved value".
  const [pushRelayURLDraft, setPushRelayURLDraft] = useState<string | null>(null);
  // The relay URL draft lives outside useSettingsForm (only registration
  // persists it), so the navigation guard and reload prompt only see it
  // through this report. Above the loading return: hooks must be unconditional.
  useReportUnsavedChanges(
    pushRelayURLDraft !== null &&
      pushRelayURLDraft !==
        (form.getValue("notifications.push_relay_url") || DEFAULT_PUSH_RELAY_URL),
  );

  if (form.isLoading) {
    return (
      <div className="space-y-6">
        <Skeleton className="h-7 w-44" />
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
  const needsRestart = (key: string) => restartKeys.has(key);
  const allRestart = (keys: string[]) => keys.every((key) => restartKeys.has(key));
  const anyDirty = (keys: string[]) => keys.some(form.isDirty);

  const releaseEventsOn = isOn("notifications.release_events_enabled");
  const fanoutOn = isOn("notifications.fanout_enabled");
  const uiOn = isOn("notifications.ui_enabled");
  const webPushOn = isOn("notifications.web_push_enabled");
  const emailOn = isOn("notifications.email_enabled");
  const serverChannelsOn = isOn("notifications.server_channels_enabled");
  // Mobile push, Discord, and personal webhooks are opt-in (default off).
  const applePushOn = form.getValue("notifications.apple_push_delivery_enabled") === "true";
  const androidPushOn = form.getValue("notifications.android_push_delivery_enabled") === "true";
  const mobilePushOn = applePushOn || androidPushOn;
  const discordOn = form.getValue("notifications.discord_enabled") === "true";
  const webhooksOn = form.getValue("notifications.webhooks_enabled") === "true";

  // The relay URL is not part of the settings form: the server only persists
  // it through the registration endpoint, alongside the credentials it mints.
  const savedPushRelayURL = form.getValue("notifications.push_relay_url") || DEFAULT_PUSH_RELAY_URL;
  const pushRelayURL = pushRelayURLDraft ?? savedPushRelayURL;
  const pushRelayURLEdited = pushRelayURL !== savedPushRelayURL;
  const pushRelayDeploymentID = form.getValue("notifications.push_relay_deployment_id");
  const pushRelayKeyPrefix = form.getValue("notifications.push_relay_key_prefix");
  const pushRelayExpiresAt = form.getValue("notifications.push_relay_expires_at");
  const pushRelayReregistrationRequired =
    form.getValue("notifications.push_relay_reregistration_required") === "true";
  const pushRelayAPIKeyReady = form.sensitiveConfigured.includes(
    "notifications.push_relay_api_key",
  );
  const allowPrivate =
    form.getValue("notifications.webhooks.allow_private_destinations") === "true";

  // Mail readiness mirrors the server's rule (shared with the overview tile):
  // the outbound switch, a server, AND a sender address — legacy rows and
  // single-key writes can store enabled-without-sender, which cannot send.
  const mailReady = emailReady(
    form.getValue("email.enabled") === "true",
    form.getValue("email.smtp_host"),
    form.getValue("email.from_address"),
  );
  // The Discord application is configured in the Discord channel card below,
  // next to the delivery settings it gates. "Configured" mirrors the server's
  // own rule (DiscordConfigured): client id, client secret, AND bot token —
  // a partial save (say, only the bot token) must not read as connected.
  const discordAppConfigured =
    form.getValue("discord.client_id").trim() !== "" &&
    form.sensitiveConfigured.includes("discord.client_secret") &&
    form.sensitiveConfigured.includes("discord.bot_token");

  const channelStates = [
    uiOn,
    webPushOn,
    mobilePushOn,
    emailOn,
    discordOn,
    webhooksOn,
    serverChannelsOn,
  ];
  const enabledChannelCount = channelStates.filter(Boolean).length;

  const failingServerChannels = (serverChannels ?? []).filter(
    (channel) =>
      channel.last_failure_at != null &&
      (channel.last_success_at == null || channel.last_failure_at > channel.last_success_at),
  ).length;

  const pausedMessage = !releaseEventsOn
    ? "New content is not being recorded, so nothing can be sent."
    : !fanoutOn
      ? "Sending is paused; new content waits in the queue."
      : null;

  return (
    <div className="flex h-full flex-col">
      <SettingsPageHeader className="mb-6" title="Notifications" />

      <div className="flex-1 space-y-5">
        {/* ── Pipeline: the master switches, framed as the flow they gate ── */}
        <div className="surface-panel rounded-2xl border-0 p-4 sm:p-5">
          <div className="text-muted-foreground mb-4 text-xs font-semibold tracking-[0.22em] uppercase">
            Pipeline
          </div>
          <div className="grid gap-4 md:grid-cols-[1fr_auto_1fr_auto_1fr] md:gap-3">
            <PipelineStage
              icon={Rss}
              title="Notice new content"
              description="Record new items during library scans."
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
              title="Work out who wants it"
              description="Match each item against everyone's preferences."
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
              title="Send it"
              description="Hand queued messages to the channels below."
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
          <ZoneHeading title="Delivery Channels" />

          <ChannelCard
            icon={Inbox}
            title="In-App"
            description="Notification inbox in the web, mobile, and TV apps."
            enabled={uiOn}
            onEnabledChange={setToggle("notifications.ui_enabled")}
          />

          <ChannelCard
            icon={MonitorSmartphone}
            title="Web Push"
            description="Browser push to subscribed devices."
            enabled={webPushOn}
            onEnabledChange={setToggle("notifications.web_push_enabled")}
          />

          <ChannelCard
            icon={RadioTower}
            title="Silo Push Relay"
            description="Mobile push through Silo's relay, delivered by APNs or FCM."
            enabled={mobilePushOn}
            onEnabledChange={(enabled) => {
              form.setValue("notifications.apple_push_delivery_enabled", String(enabled));
              form.setValue("notifications.android_push_delivery_enabled", String(enabled));
            }}
            chips={
              pushRelayReregistrationRequired ? (
                <Chip tone="warning">Re-registration required</Chip>
              ) : pushRelayAPIKeyReady ? (
                <Chip tone="positive">Relay configured</Chip>
              ) : (
                <Chip tone={mobilePushOn ? "warning" : "neutral"}>Relay registration required</Chip>
              )
            }
          >
            <div className="settings-field-list">
              <MobilePushPrivacyDisclosure />
              <SettingField
                label="Apple Push (APNs)"
                type="toggle"
                value={String(applePushOn)}
                onChange={(value) =>
                  form.setValue("notifications.apple_push_delivery_enabled", value)
                }
                restartRequired={needsRestart("notifications.apple_push_delivery_enabled")}
              />
              <SettingField
                label="Android Push (FCM)"
                type="toggle"
                value={String(androidPushOn)}
                onChange={(value) =>
                  form.setValue("notifications.android_push_delivery_enabled", value)
                }
                restartRequired={needsRestart("notifications.android_push_delivery_enabled")}
              />
              <SettingField
                label="Relay URL"
                description="Saved when you register below."
                type="text"
                value={pushRelayURL}
                onChange={(v) => setPushRelayURLDraft(v)}
                restartRequired={needsRestart("notifications.push_relay_url")}
              />
              <RegisterRelayRow
                relayURL={pushRelayURL}
                deploymentID={pushRelayDeploymentID}
                keyPrefix={pushRelayKeyPrefix}
                expiresAt={pushRelayExpiresAt}
                reregistrationRequired={pushRelayReregistrationRequired}
                urlEdited={pushRelayURLEdited}
                onRegistered={(submittedRelayURL) =>
                  setPushRelayURLDraft((currentDraft) => {
                    const currentURL = currentDraft ?? savedPushRelayURL;
                    return currentURL === submittedRelayURL ? null : currentDraft;
                  })
                }
              />
            </div>
          </ChannelCard>

          <ChannelCard
            icon={Mail}
            title="Email"
            description="Daily summary or a message per episode, for people who opt in."
            enabled={emailOn}
            onEnabledChange={setToggle("notifications.email_enabled")}
            chips={
              <>
                {mailReady ? (
                  <Chip tone="positive">Mail server set up</Chip>
                ) : (
                  <Chip tone={emailOn ? "warning" : "neutral"}>Mail server not set up</Chip>
                )}
                <Chip>
                  Summary at {digestHourLabel(form.getValue("notifications.email.digest_hour"))}
                </Chip>
              </>
            }
          >
            <SettingsSubheading>Mail Server</SettingsSubheading>
            <div className="settings-field-list">
              <SettingField
                label="Send email from this server"
                description="Covers every email Silo sends, not just notifications."
                type="toggle"
                value={form.getValue("email.enabled")}
                onChange={(v) => form.setValue("email.enabled", v)}
                restartRequired={needsRestart("email.enabled")}
              />
              <SettingField
                label="From address"
                hint="silo@example.com"
                value={form.getValue("email.from_address")}
                onChange={(v) => form.setValue("email.from_address", v)}
                restartRequired={needsRestart("email.from_address")}
              />
              <SettingField
                label="From name"
                hint="Silo"
                value={form.getValue("email.from_name")}
                onChange={(v) => form.setValue("email.from_name", v)}
                restartRequired={needsRestart("email.from_name")}
              />
              <SettingField
                label="Mail server address"
                hint="smtp.example.com"
                value={form.getValue("email.smtp_host")}
                onChange={(v) => form.setValue("email.smtp_host", v)}
                restartRequired={needsRestart("email.smtp_host")}
              />
              <SettingField
                label="Port"
                description="587 for STARTTLS (typical), 465 for implicit TLS"
                type="number"
                value={form.getValue("email.smtp_port")}
                onChange={(v) => form.setValue("email.smtp_port", v)}
                restartRequired={needsRestart("email.smtp_port")}
              />
              <SettingField
                label="Encryption"
                description="Use whatever your mail provider documents."
                type="select"
                options={[
                  { value: "starttls", label: "STARTTLS" },
                  { value: "tls", label: "TLS (implicit)" },
                  { value: "none", label: "None (insecure)" },
                ]}
                value={form.getValue("email.smtp_security") || "starttls"}
                onChange={(v) => form.setValue("email.smtp_security", v)}
                restartRequired={needsRestart("email.smtp_security")}
              />
              <SettingField
                label="Username"
                description="Leave empty if the mail server needs no sign-in."
                value={form.getValue("email.smtp_username")}
                onChange={(v) => form.setValue("email.smtp_username", v)}
                restartRequired={needsRestart("email.smtp_username")}
              />
              <SecretField
                label="Password"
                value={form.getValue("email.smtp_password")}
                configured={form.sensitiveConfigured.includes("email.smtp_password")}
                onKeep={() => form.resetValue("email.smtp_password")}
                // The username above can be emptied for a relay that needs no
                // sign-in; without this the password could not follow it.
                onClear={() => form.setValue("email.smtp_password", "")}
                cleared={form.isClearStaged("email.smtp_password")}
                onChange={(v) => form.setValue("email.smtp_password", v)}
                restartRequired={needsRestart("email.smtp_password")}
              />
              <TestEmailRow />
            </div>
            <SettingsSubheading>Delivery</SettingsSubheading>
            <div className="settings-field-list">
              <SettingField
                label="Let people pick an email per episode"
                description="Off sends them the daily summary instead."
                type="toggle"
                value={toggleValue("notifications.email.allow_per_episode")}
                onChange={(v) => form.setValue("notifications.email.allow_per_episode", v)}
                restartRequired={needsRestart("notifications.email.allow_per_episode")}
              />
              <SettingField
                label="Daily summary hour"
                description="In the server's own time zone."
                unit="0-23"
                type="number"
                value={numberValue("notifications.email.digest_hour", "8")}
                onChange={(v) => form.setValue("notifications.email.digest_hour", v)}
                restartRequired={needsRestart("notifications.email.digest_hour")}
              />
              <SettingField
                label="Public URL"
                description="Used for links inside emails; leave empty to omit them."
                type="text"
                value={form.getValue("notifications.email.external_url")}
                onChange={(v) => form.setValue("notifications.email.external_url", v)}
                restartRequired={needsRestart("notifications.email.external_url")}
              />
            </div>
          </ChannelCard>

          <ChannelCard
            icon={Bot}
            title="Discord"
            description="Direct messages from your Discord bot to linked accounts."
            enabled={discordOn}
            onEnabledChange={setToggle("notifications.discord_enabled")}
            chips={
              discordAppConfigured ? (
                <Chip tone="positive">Discord app connected</Chip>
              ) : (
                <Chip tone={discordOn ? "warning" : "neutral"}>Discord app not connected</Chip>
              )
            }
          >
            <DiscordAppCredentials
              savedClientId={form.getValue("discord.client_id")}
              sensitiveConfigured={form.sensitiveConfigured}
              restartKeys={restartKeys}
            />
            <SettingsSubheading>Delivery</SettingsSubheading>
            <div className="settings-field-list">
              <SettingField
                label="Let people pick a DM per episode"
                description="Off sends them the daily summary instead."
                type="toggle"
                value={toggleValue("notifications.discord.allow_per_episode")}
                onChange={(v) => form.setValue("notifications.discord.allow_per_episode", v)}
                restartRequired={needsRestart("notifications.discord.allow_per_episode")}
              />
              <SettingField
                label="Daily summary hour"
                description="In the server's own time zone."
                unit="0-23"
                type="number"
                value={numberValue("notifications.discord.digest_hour", "8")}
                onChange={(v) => form.setValue("notifications.discord.digest_hour", v)}
                restartRequired={needsRestart("notifications.discord.digest_hour")}
              />
            </div>
            <SettingsSubheading>Appearance</SettingsSubheading>
            <SettingField
              label="Artwork"
              description="Server images reveal your server URL to anyone who sees the message."
              type="select"
              value={form.getValue("notifications.discord.poster_mode") || "provider"}
              options={[
                { value: "provider", label: "Provider images only" },
                { value: "server", label: "Provider and server images" },
                { value: "off", label: "No artwork" },
              ]}
              onChange={(v) => form.setValue("notifications.discord.poster_mode", v)}
              restartRequired={needsRestart("notifications.discord.poster_mode")}
            />
          </ChannelCard>

          <ChannelCard
            icon={Webhook}
            title="Personal Webhooks"
            description="Webhooks people create for themselves, Discord or generic."
            enabled={webhooksOn}
            onEnabledChange={setToggle("notifications.webhooks_enabled")}
            chips={
              allowPrivate ? <Chip tone="warning">Private destinations allowed</Chip> : undefined
            }
          >
            <AdvancedSection
              id="notifications.webhooks"
              count={WEBHOOK_ADVANCED_KEYS.length}
              forceOpen={anyDirty(WEBHOOK_ADVANCED_KEYS)}
            >
              <SettingField
                label="Webhooks per person"
                type="number"
                value={numberValue("notifications.webhooks.max_per_profile", "10")}
                onChange={(v) => form.setValue("notifications.webhooks.max_per_profile", v)}
                restartRequired={needsRestart("notifications.webhooks.max_per_profile")}
              />
              <SettingField
                label="Deliveries per person"
                description="Calls over the limit are dropped; the inbox notification still arrives."
                unit="per minute"
                type="number"
                value={numberValue(
                  "notifications.webhooks.deliveries_per_minute_per_profile",
                  "60",
                )}
                onChange={(v) =>
                  form.setValue("notifications.webhooks.deliveries_per_minute_per_profile", v)
                }
                restartRequired={needsRestart(
                  "notifications.webhooks.deliveries_per_minute_per_profile",
                )}
              />
              <SettingField
                label="Allow webhooks to private addresses"
                description="Allows LAN and localhost destinations; development only."
                type="toggle"
                value={form.getValue("notifications.webhooks.allow_private_destinations")}
                onChange={(v) =>
                  form.setValue("notifications.webhooks.allow_private_destinations", v)
                }
                restartRequired={needsRestart("notifications.webhooks.allow_private_destinations")}
              />
              {allowPrivate && (
                <div className="flex items-start gap-2 py-3 text-xs text-amber-500">
                  <TriangleAlert className="mt-0.5 h-4 w-4 flex-shrink-0" />
                  <p>
                    Any user with webhook access can make this server send requests to internal
                    network addresses.
                  </p>
                </div>
              )}
            </AdvancedSection>
          </ChannelCard>

          <ChannelCard
            icon={Megaphone}
            title="Server Channels"
            description="Server-wide announcements posted to a shared destination."
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
            <div className="settings-field-list">
              <SettingField
                label="Batch window"
                description="New content waits this long so a season posts as one message."
                unit="seconds"
                type="number"
                value={numberValue("notifications.server_channels.batch_seconds", "300")}
                onChange={(v) => form.setValue("notifications.server_channels.batch_seconds", v)}
                restartRequired={needsRestart("notifications.server_channels.batch_seconds")}
              />
              <SettingField
                label="Mention the requester on Discord"
                description="Unlinked accounts show their Silo username instead."
                type="toggle"
                value={form.getValue("notifications.server_channels.mention_requesters")}
                onChange={(v) =>
                  form.setValue("notifications.server_channels.mention_requesters", v)
                }
                restartRequired={needsRestart("notifications.server_channels.mention_requesters")}
              />
              <div className="pt-3">
                <ServerNotificationChannels />
              </div>
            </div>
          </ChannelCard>
        </section>

        {/* ── Tuning: everything here has a working default ── */}
        <section className="space-y-3">
          <ZoneHeading title="Tuning" />
          {/* Full width, not a two-up grid: the settings column is clamped to
              max-w-3xl, so side-by-side groups left each row with a ~140px
              label column beside its control and every description wrapped to
              six lines. */}
          <div className="space-y-3">
            <FieldGroup label="Grouping and flood control" restartAll={allRestart(FANOUT_KEYS)}>
              <AdvancedSection
                id="notifications.fanout"
                count={FANOUT_KEYS.length}
                forceOpen={anyDirty(FANOUT_KEYS)}
              >
                <SettingField
                  label="Settle window"
                  description="Items that finish scanning together arrive as one notification."
                  unit="seconds"
                  type="number"
                  value={numberValue("notifications.fanout.settle_seconds", "30")}
                  onChange={(v) => form.setValue("notifications.fanout.settle_seconds", v)}
                  restartRequired={needsRestart("notifications.fanout.settle_seconds")}
                />
                <SettingField
                  label="Max messages per show"
                  description="Anything past this is skipped for that batch."
                  type="number"
                  value={numberValue("notifications.fanout.max_series_burst", "3")}
                  onChange={(v) => form.setValue("notifications.fanout.max_series_burst", v)}
                  restartRequired={needsRestart("notifications.fanout.max_series_burst")}
                />
                <SettingField
                  label="Max content age"
                  description="Older items are dropped instead of arriving as stale news."
                  unit="hours"
                  type="number"
                  value={numberValue("notifications.fanout.max_event_age_hours", "72")}
                  onChange={(v) => form.setValue("notifications.fanout.max_event_age_hours", v)}
                  restartRequired={needsRestart("notifications.fanout.max_event_age_hours")}
                />
              </AdvancedSection>
            </FieldGroup>

            <FieldGroup label="Retention" restartAll={allRestart(RETENTION_KEYS)}>
              <AdvancedSection
                id="notifications.retention"
                count={RETENTION_KEYS.length}
                forceOpen={anyDirty(RETENTION_KEYS)}
              >
                <SettingField
                  label="Read notifications"
                  unit="days"
                  type="number"
                  value={numberValue("notifications.retention.read_days", "90")}
                  onChange={(v) => form.setValue("notifications.retention.read_days", v)}
                  restartRequired={needsRestart("notifications.retention.read_days")}
                />
                <SettingField
                  label="Unread notifications"
                  unit="days"
                  type="number"
                  value={numberValue("notifications.retention.unread_days", "180")}
                  onChange={(v) => form.setValue("notifications.retention.unread_days", v)}
                  restartRequired={needsRestart("notifications.retention.unread_days")}
                />
                <SettingField
                  label="Sent history"
                  description="Record of what Silo already notified about."
                  unit="days"
                  type="number"
                  value={numberValue("notifications.retention.event_days", "30")}
                  onChange={(v) => form.setValue("notifications.retention.event_days", v)}
                  restartRequired={needsRestart("notifications.retention.event_days")}
                />
              </AdvancedSection>
            </FieldGroup>
          </div>
        </section>
      </div>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={form.save}
        onDiscard={form.discard}
        isSaving={form.isSaving}
      />
    </div>
  );
}
