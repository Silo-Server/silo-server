import { useId, useMemo, useRef, useState, type ReactNode } from "react";
import { MobilePushPrivacyDisclosure } from "@/components/notifications/MobilePushPrivacyDisclosure";
import { useQueryClient } from "@tanstack/react-query";
import {
  Bot,
  Check,
  ChevronDown,
  Copy,
  ExternalLink,
  Inbox,
  KeyRound,
  Loader2,
  Mail,
  Megaphone,
  MonitorSmartphone,
  RadioTower,
  TriangleAlert,
  Webhook,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { Link } from "react-router";
import { toast } from "sonner";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { testNotificationDiscord } from "@/api/v2/notificationDiscord";
import { notificationScope } from "@/api/v2/notifications";
import {
  registerNotificationRelay,
  clearNotificationRelay,
  type NotificationRelayRegistration,
} from "@/api/v2/notificationRelay";
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
import { SaveBar } from "@/components/SaveBar";
import { SettingField, SettingFieldStatus } from "./SettingField";
import ServerNotificationChannels from "./ServerNotificationChannels";
import { formatLocale } from "@/lib/appLocale";
import { useT, type Translate } from "@/i18n";

/**
 * Operator kill switches for the whole pipeline. Both default on and only
 * come off to stop a flood or chase a problem, so they sit under Advanced.
 */
const PIPELINE_KEYS = ["notifications.release_events_enabled", "notifications.fanout_enabled"];

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

/** The page's one foot-of-page Advanced area. */
const PROCESSING_KEYS = [...PIPELINE_KEYS, ...FANOUT_KEYS, ...RETENTION_KEYS];

/** Personal webhook limits and the SSRF escape hatch; all advanced. */
const WEBHOOK_ADVANCED_KEYS = [
  "notifications.webhooks.max_per_profile",
  "notifications.webhooks.deliveries_per_minute_per_profile",
  "notifications.webhooks.allow_private_destinations",
];

const PUSH_DELIVERY_KEYS = [
  "notifications.apple_push_delivery_enabled",
  "notifications.android_push_delivery_enabled",
];

/**
 * Rows in the relay card's Advanced section: the per-platform switches (the
 * card's own switch flips both), the relay URL, and the deployment ID. Only the
 * switches go through the settings form.
 */
const PUSH_RELAY_ADVANCED_KEYS = [
  ...PUSH_DELIVERY_KEYS,
  "notifications.push_relay_url",
  "notifications.push_relay_deployment_id",
];

const EMAIL_CHANNEL_ADVANCED_KEYS = ["notifications.email.digest_hour"];

const DISCORD_ADVANCED_KEYS = [
  "notifications.discord.poster_mode",
  "notifications.discord.digest_hour",
];

const SERVER_CHANNELS_ADVANCED_KEYS = [
  "notifications.server_channels.batch_seconds",
  "notifications.server_channels.mention_requesters",
];

/**
 * Read for the Email channel's status line only. The mail server itself is
 * edited on the Email page, since invitations and password resets send
 * through it too.
 */
const MAIL_STATUS_KEYS = ["email.enabled", "email.smtp_host", "email.from_address"];

/**
 * The Discord application. Saved by its own card inside the Discord channel
 * (a bot token has to be testable before it is committed), but listed here so
 * one form owns every value this page reads.
 */
const DISCORD_APP_KEYS = ["discord.client_id", "discord.client_secret", "discord.bot_token"];

const KEYS = [
  ...PIPELINE_KEYS,
  "notifications.ui_enabled",
  "notifications.webhooks_enabled",
  "notifications.web_push_enabled",
  ...PUSH_DELIVERY_KEYS,
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
  ...EMAIL_CHANNEL_ADVANCED_KEYS,
  "notifications.discord_enabled",
  "notifications.discord.allow_per_episode",
  ...DISCORD_ADVANCED_KEYS,
  "notifications.server_channels_enabled",
  ...SERVER_CHANNELS_ADVANCED_KEYS,
  ...RETENTION_KEYS,
  ...MAIL_STATUS_KEYS,
  ...DISCORD_APP_KEYS,
];

const DEFAULT_PUSH_RELAY_URL = "https://push.siloserver.org";

/** Product and service names; they read the same in every language. */
const PUSH_RELAY_TITLE = "Silo Push Relay";
const APPLE_PUSH_LABEL = "Apple Push (APNs)";
const ANDROID_PUSH_LABEL = "Android Push (FCM)";
const DISCORD_TITLE = "Discord";

const DISCORD_DEVELOPER_PORTAL = "https://discord.com/developers/applications";
const DISCORD_CALLBACK_PATH = "/api/v2/notifications/discord/link/callback";

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
        "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium whitespace-nowrap",
        tone === "neutral" && "border-border/70 text-muted-foreground",
        tone === "positive" && "border-emerald-500/30 bg-emerald-500/10 text-emerald-500",
        tone === "warning" && "border-amber-500/30 bg-amber-500/10 text-amber-500",
      )}
    >
      {children}
    </span>
  );
}

/**
 * A labelled cluster inside an Advanced disclosure. One element per cluster
 * means the disclosure's child rule draws a single hairline between clusters,
 * with the rows keeping their own inside each.
 */
function AdvancedCluster({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <SettingsSubheading>{label}</SettingsSubheading>
      {children}
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
 * One delivery channel as a settings row: icon, title, status chips and the
 * enable switch, with the channel's setup in an expandable body below it.
 * Settings stay editable while the channel is off so admins can configure
 * before enabling.
 *
 * The body is the channel's main content (credentials, destinations, the
 * privacy disclosure), not an Advanced tier, so it is this card's own
 * expand/collapse; tuning inside it sits behind an `AdvancedSection`.
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
  const t = useT();
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
    <div>
      <div className="flex items-center gap-3 py-3.5">
        {expandable ? (
          <button
            type="button"
            aria-expanded={open}
            aria-controls={bodyId}
            onClick={() => setOpen((current) => !current)}
            className="flex min-w-0 flex-1 cursor-pointer items-center gap-3 text-start"
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
          aria-label={t("web.admin.settings.notifications.enableChannel", { channel: title })}
        />
      </div>
      {expandable && open && (
        <div
          id={bodyId}
          className="animate-in fade-in-0 slide-in-from-top-1 ps-[3.25rem] pb-4 duration-200"
        >
          {children}
        </div>
      )}
    </div>
  );
}

/** The registration result line; the key prefix and relay request ID are optional. */
function relayReadyLabel(t: Translate, result: NotificationRelayRegistration): string {
  const deployment = result.deployment_id;
  const key = result.key_prefix;
  const request = result.relay_request_id;
  if (key && request)
    return t("web.admin.settings.notifications.relay.readyKeyRelay", { deployment, key, request });
  if (key) return t("web.admin.settings.notifications.relay.readyKey", { deployment, key });
  if (request)
    return t("web.admin.settings.notifications.relay.readyRelay", { deployment, request });
  return t("web.admin.settings.notifications.relay.ready", { deployment });
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
  const t = useT();
  const queryClient = useQueryClient();
  const authority = captureProfileRequestContext();
  const inFlight = useRef(false);
  const [pending, setPending] = useState(false);
  const [confirmClear, setConfirmClear] = useState(false);
  const [result, setResult] = useState<NotificationRelayRegistration | null>(null);

  const configured = deploymentID.trim() !== "";
  const actionLabel = reregistrationRequired
    ? t("web.admin.settings.notifications.relay.reregister")
    : configured
      ? t("web.admin.settings.notifications.relay.rotate")
      : t("web.admin.settings.notifications.relay.register");
  const expiration = expiresAt ? new Date(expiresAt) : null;
  const expirationValid = expiration != null && !Number.isNaN(expiration.getTime());
  const renewalStatus = expirationValid
    ? expiration.getTime() <= Date.now()
      ? t("web.admin.settings.notifications.relay.expired")
      : t("web.admin.settings.notifications.relay.expires", {
          date: expiration.toLocaleString(formatLocale()),
        })
    : configured
      ? t("web.admin.settings.notifications.relay.expirationUnknown")
      : reregistrationRequired
        ? t("web.admin.settings.notifications.relay.noCredential")
        : t("web.admin.settings.notifications.relay.notRegistered");

  const registerRelay = async () => {
    if (inFlight.current) return;
    inFlight.current = true;
    setPending(true);
    setResult(null);
    try {
      if (!authority) throw new StaleApiRequestContextError();
      const response = await registerNotificationRelay(relayURL, authority);
      setResult(response);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: adminKeys.serverSettings() }),
        queryClient.invalidateQueries({
          queryKey: [...adminKeys.serverSettings(), "sensitive-status"] as const,
        }),
      ]);
      if (!isCapturedProfileAuthorityActive(authority)) return;
      onRegistered(relayURL);
      toast.success(t("web.admin.settings.notifications.relay.registered"));
    } catch (error) {
      if (authority && isCapturedProfileAuthorityActive(authority))
        toast.error(
          error instanceof Error
            ? error.message
            : t("web.admin.settings.notifications.relay.registerFailed"),
        );
    } finally {
      inFlight.current = false;
      setPending(false);
    }
  };

  const clearRelay = async () => {
    if (inFlight.current) return;
    inFlight.current = true;
    setPending(true);
    setResult(null);
    try {
      if (!authority) throw new StaleApiRequestContextError();
      await clearNotificationRelay(authority);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: adminKeys.serverSettings() }),
        queryClient.invalidateQueries({
          queryKey: [...adminKeys.serverSettings(), "sensitive-status"] as const,
        }),
      ]);
      if (!isCapturedProfileAuthorityActive(authority)) return;
      setConfirmClear(false);
      toast.success(t("web.admin.settings.notifications.relay.cleared"));
    } catch (error) {
      if (authority && isCapturedProfileAuthorityActive(authority))
        toast.error(
          error instanceof Error
            ? error.message
            : t("web.admin.settings.notifications.relay.clearFailed"),
        );
    } finally {
      inFlight.current = false;
      setPending(false);
    }
  };

  return (
    <div className="space-y-3 py-3">
      <SettingField
        label={t("web.admin.settings.notifications.relay.deploymentId")}
        description={t("web.admin.settings.notifications.relay.deploymentIdDescription")}
        type="text"
        value={deploymentID}
        onChange={() => {}}
        disabled
      />
      <div className="flex flex-wrap items-center gap-2 py-2">
        <Button variant="outline" size="sm" disabled={pending} onClick={() => void registerRelay()}>
          {pending ? (
            <Loader2 className="me-1.5 h-3.5 w-3.5 animate-spin" />
          ) : (
            <KeyRound className="me-1.5 h-3.5 w-3.5" />
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
            {t("web.admin.settings.notifications.relay.clearCredential")}
          </Button>
        )}
      </div>
      {reregistrationRequired && (
        <div className="text-xs text-amber-500">
          {t("web.admin.settings.notifications.relay.reregistrationNote")}
        </div>
      )}
      <div className="text-muted-foreground space-y-1 text-xs">
        {keyPrefix && (
          <div>{t("web.admin.settings.notifications.relay.credential", { prefix: keyPrefix })}</div>
        )}
        <div>{renewalStatus}</div>
      </div>
      {urlEdited && (
        <div className="text-muted-foreground text-xs">
          {t("web.admin.settings.notifications.relay.applyUrl")}
        </div>
      )}
      {result && <div className="text-xs text-emerald-500">{relayReadyLabel(t, result)}</div>}
      <AlertDialog open={confirmClear} onOpenChange={setConfirmClear}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("web.admin.settings.notifications.relay.clearTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("web.admin.settings.notifications.relay.clearDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={pending}>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              disabled={pending}
              onClick={() => void clearRelay()}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {pending
                ? t("web.admin.settings.notifications.relay.clearing")
                : t("web.admin.settings.notifications.relay.clearConfirm")}
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

/**
 * Invite link for adding the bot to a Discord server. Membership alone is
 * enough to DM, so no permissions are requested.
 */
function discordInviteUrl(clientId: string): string {
  return `https://discord.com/oauth2/authorize?client_id=${encodeURIComponent(clientId)}&scope=bot&permissions=0`;
}

function DiscordSetupGuide() {
  const t = useT();
  const b = (chunks: ReactNode) => <strong>{chunks}</strong>;
  return (
    <AdvancedSection
      id="notifications.discord.setup-guide"
      title={t("web.admin.settings.notifications.discord.setupGuide")}
    >
      <div className="text-muted-foreground space-y-1.5 py-3 text-xs leading-relaxed">
        <p>
          {t("web.admin.settings.notifications.discord.guideIntro", {
            site: DISCORD_DEVELOPER_PORTAL.replace("https://", ""),
            link: (chunks) => (
              <a
                href={DISCORD_DEVELOPER_PORTAL}
                target="_blank"
                rel="noopener noreferrer"
                className="underline"
              >
                {chunks}
              </a>
            ),
          })}
        </p>
        <ol className="list-decimal space-y-1.5 ps-4">
          <li>{t("web.admin.settings.notifications.discord.guideCreate")}</li>
          <li>
            {t("web.admin.settings.notifications.discord.guideOAuth", {
              b,
              callback: `<${t("web.admin.settings.notifications.discord.guidePublicUrl")}>${DISCORD_CALLBACK_PATH}`,
              code: (chunks) => <code className="bg-muted mx-1 rounded px-1">{chunks}</code>,
            })}
          </li>
          <li>{t("web.admin.settings.notifications.discord.guideBot", { b })}</li>
          <li>{t("web.admin.settings.notifications.discord.guideGrant", { b })}</li>
          <li>{t("web.admin.settings.notifications.discord.guideInvite", { b })}</li>
        </ol>
      </div>
    </AdvancedSection>
  );
}

function InviteBotRow({ clientId }: { clientId: string }) {
  const t = useT();
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
      toast.success(t("web.admin.settings.notifications.discord.inviteCopied"));
      window.setTimeout(() => setCopied(false), 2000);
    } catch {
      toast.error(t("web.admin.settings.notifications.discord.inviteCopyFailed"));
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
          <ExternalLink className="me-1.5 h-3.5 w-3.5" />
          {t("web.admin.settings.notifications.discord.invite")}
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={!trimmed}
          onClick={() => void copyInviteLink()}
        >
          {copied ? (
            <Check className="me-1.5 h-3.5 w-3.5" />
          ) : (
            <Copy className="me-1.5 h-3.5 w-3.5" />
          )}
          {t("web.admin.settings.notifications.discord.copyLink")}
        </Button>
      </div>
      <div className="text-muted-foreground text-xs">
        {trimmed
          ? t("web.admin.settings.notifications.discord.mustBeInServer")
          : t("web.admin.settings.notifications.discord.enterClientId")}
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
  const t = useT();
  const authority = captureProfileRequestContext();
  const testInFlight = useRef(false);
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
      toast.success(t("web.admin.settings.notifications.discord.saved"));
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
      toast.success(t("web.admin.settings.notifications.discord.cleared"));
    } catch {
      // The mutation surfaces the API error.
    }
  }

  async function runTest() {
    if (testInFlight.current) return;
    testInFlight.current = true;
    setTesting(true);
    setTestResult(null);
    try {
      if (!authority) throw new StaleApiRequestContextError();
      const response = await testNotificationDiscord(authority);
      const values = {
        outcome: response.ok ? "ok" : "failed",
        duration_ms: String(response.duration_ms),
      };
      setTestResult({
        success: response.ok,
        message: response.message
          ? t("web.admin.settings.notifications.discord.testResultWithMessage", {
              ...values,
              message: response.message,
            })
          : t("web.admin.settings.notifications.discord.testResult", values),
      });
    } catch (error) {
      if (!authority || !isCapturedProfileAuthorityActive(authority)) return;
      setTestResult({
        success: false,
        message:
          error instanceof Error
            ? error.message
            : t("web.admin.settings.notifications.discord.testFailed"),
      });
    } finally {
      testInFlight.current = false;
      setTesting(false);
    }
  }

  return (
    <>
      <SettingsSubheading>
        {t("web.admin.settings.notifications.discord.application")}
      </SettingsSubheading>
      <DiscordSetupGuide />
      <div className="settings-field-list">
        <SettingField
          label={t("web.admin.settings.notifications.discord.clientId")}
          value={clientId}
          onChange={setClientIdDraft}
          description={t("web.admin.settings.notifications.discord.fromOAuth2Page")}
          restartRequired={restartKeys.has("discord.client_id")}
        />
      </div>
      <InviteBotRow clientId={clientId} />
      <div className="settings-field-list">
        <SecretField
          label={t("web.admin.settings.notifications.discord.clientSecret")}
          value={clientSecret}
          configured={secretConfigured}
          onChange={setClientSecret}
          hint={t("web.admin.settings.notifications.discord.fromOAuth2Page")}
          restartRequired={restartKeys.has("discord.client_secret")}
        />
        <SecretField
          label={t("web.admin.settings.notifications.discord.botToken")}
          value={botToken}
          configured={tokenConfigured}
          onChange={setBotToken}
          hint={t("web.admin.settings.notifications.discord.fromBotPage")}
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
          {updateSettings.isPending
            ? t("web.admin.settings.notifications.discord.saving")
            : t("common.save")}
        </Button>
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() => void runTest()}
          disabled={testing || unsaved || !ready}
        >
          {testing
            ? t("web.admin.settings.notifications.discord.testing")
            : t("web.admin.settings.notifications.discord.testToken")}
        </Button>
        {anyStored && (
          <Button type="button" size="sm" variant="ghost" onClick={() => setConfirmClear(true)}>
            {t("web.admin.settings.notifications.discord.clearCredentials")}
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
          {t("web.admin.settings.notifications.discord.saveFirst")}
        </p>
      )}
      <AlertDialog open={confirmClear} onOpenChange={setConfirmClear}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("web.admin.settings.notifications.discord.clearTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("web.admin.settings.notifications.discord.clearDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              onClick={() => {
                void clearAll();
                setConfirmClear(false);
              }}
            >
              {t("web.admin.settings.notifications.discord.clearConfirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

export default function NotificationsAdminSettings() {
  const t = useT();
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
        <div className="space-y-3">
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-[72px] w-full rounded-2xl" />
          ))}
        </div>
      </div>
    );
  }

  // Without a settings snapshot every value reads as empty, which would show
  // the default-on toggles (mobile push included) as off. Do not render
  // controls against a guess.
  if (form.loadError || !form.loaded) {
    return (
      <div className="border-destructive/30 bg-destructive/5 rounded-2xl border px-4 py-3.5 text-sm">
        <p className="font-medium">{t("web.admin.settings.notifications.loadFailedTitle")}</p>
        <p className="text-muted-foreground mt-0.5 text-xs">
          {t("web.admin.settings.notifications.loadFailedDescription")}
        </p>
        <Button
          type="button"
          variant="outline"
          className="mt-3"
          onClick={() => window.location.reload()}
        >
          {t("web.admin.settings.notifications.reload")}
        </Button>
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
  // Mobile push defaults on (the effective snapshot carries the default);
  // Discord and personal webhooks are opt-in.
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

  // Mail readiness mirrors the server's rule (shared with the Dashboard's
  // attention notice): the outbound switch, a server, AND a sender address —
  // legacy rows and single-key writes can store enabled-without-sender, which
  // cannot send.
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

  const failingServerChannels = (serverChannels ?? []).filter(
    (channel) =>
      channel.last_failure_at != null &&
      (channel.last_success_at == null || channel.last_failure_at > channel.last_success_at),
  ).length;

  // The kill switches live under Advanced, so a pipeline someone switched off
  // has to say so up here: every channel below is idle while it is.
  const pausedMessage = !releaseEventsOn
    ? t("web.admin.settings.notifications.pausedRecording")
    : !fanoutOn
      ? t("web.admin.settings.notifications.pausedSending")
      : null;

  return (
    <div className="flex h-full flex-col">
      <SettingsPageHeader title={t("web.admin.settings.notifications.title")} />

      <div className="flex-1 space-y-5">
        {pausedMessage && (
          <div
            role="status"
            className="flex items-start gap-2 rounded-2xl border border-amber-500/25 bg-amber-500/5 px-4 py-3 text-xs text-amber-600 dark:text-amber-400"
          >
            <TriangleAlert className="mt-0.5 h-4 w-4 shrink-0" />
            <p>{pausedMessage}</p>
          </div>
        )}

        {/* ── Delivery channels ── */}
        <FieldGroup
          label={t("web.admin.settings.notifications.delivery")}
          description={t("web.admin.settings.notifications.deliveryDescription")}
        >
          <ChannelCard
            icon={Inbox}
            title={t("web.admin.settings.notifications.inApp.title")}
            description={t("web.admin.settings.notifications.inApp.description")}
            enabled={uiOn}
            onEnabledChange={setToggle("notifications.ui_enabled")}
          />

          <ChannelCard
            icon={MonitorSmartphone}
            title={t("web.admin.settings.notifications.webPush.title")}
            description={t("web.admin.settings.notifications.webPush.description")}
            enabled={webPushOn}
            onEnabledChange={setToggle("notifications.web_push_enabled")}
          />

          <ChannelCard
            icon={RadioTower}
            title={PUSH_RELAY_TITLE}
            description={t("web.admin.settings.notifications.relay.description")}
            enabled={mobilePushOn}
            onEnabledChange={(enabled) => {
              form.setValue("notifications.apple_push_delivery_enabled", String(enabled));
              form.setValue("notifications.android_push_delivery_enabled", String(enabled));
            }}
            chips={
              pushRelayReregistrationRequired ? (
                <Chip tone="warning">
                  {t("web.admin.settings.notifications.relay.chipReregister")}
                </Chip>
              ) : pushRelayAPIKeyReady ? (
                <Chip tone="positive">
                  {t("web.admin.settings.notifications.relay.chipConfigured")}
                </Chip>
              ) : (
                // A server that never registered does so on its first send
                // (internal/notifications/push_sender.go), so a fresh install
                // has nothing to fix here.
                <Chip>{t("web.admin.settings.notifications.relay.chipFirstSend")}</Chip>
              )
            }
          >
            <div className="settings-field-list">
              <MobilePushPrivacyDisclosure />
              <AdvancedSection
                id="notifications.push-relay"
                count={PUSH_RELAY_ADVANCED_KEYS.length}
                forceOpen={
                  anyDirty(PUSH_DELIVERY_KEYS) ||
                  pushRelayURLEdited ||
                  pushRelayReregistrationRequired
                }
              >
                <SettingField
                  label={APPLE_PUSH_LABEL}
                  type="toggle"
                  value={String(applePushOn)}
                  onChange={(value) =>
                    form.setValue("notifications.apple_push_delivery_enabled", value)
                  }
                  restartRequired={needsRestart("notifications.apple_push_delivery_enabled")}
                />
                <SettingField
                  label={ANDROID_PUSH_LABEL}
                  type="toggle"
                  value={String(androidPushOn)}
                  onChange={(value) =>
                    form.setValue("notifications.android_push_delivery_enabled", value)
                  }
                  restartRequired={needsRestart("notifications.android_push_delivery_enabled")}
                />
                <SettingField
                  label={t("web.admin.settings.notifications.relay.url")}
                  description={t("web.admin.settings.notifications.relay.urlDescription")}
                  type="text"
                  value={pushRelayURL}
                  onChange={(v) => setPushRelayURLDraft(v)}
                  restartRequired={needsRestart("notifications.push_relay_url")}
                />
                <RegisterRelayRow
                  key={notificationScope()}
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
              </AdvancedSection>
            </div>
          </ChannelCard>

          <ChannelCard
            icon={Mail}
            title={t("web.admin.settings.notifications.email.title")}
            description={t("web.admin.settings.notifications.email.description")}
            enabled={emailOn}
            onEnabledChange={setToggle("notifications.email_enabled")}
            chips={
              <>
                {/* Neutral either way: a server that runs without mail on
                    purpose is not broken, and the Dashboard lists what is. */}
                <Chip tone={mailReady ? "positive" : "neutral"}>
                  {mailReady
                    ? t("web.admin.settings.notifications.email.mailReady")
                    : t("web.admin.settings.notifications.email.mailMissing")}
                </Chip>
                <Chip>
                  {t("web.admin.settings.notifications.email.summaryAt", {
                    time: digestHourLabel(form.getValue("notifications.email.digest_hour")),
                  })}
                </Chip>
              </>
            }
          >
            <div className="settings-field-list">
              <div className="py-3.5">
                <SettingFieldStatus tone={mailReady ? "ok" : "muted"}>
                  <span>
                    {t(
                      mailReady
                        ? "web.admin.settings.notifications.email.mailReadyStatus"
                        : "web.admin.settings.notifications.email.mailMissingStatus",
                      {
                        link: (chunks) => (
                          <Link
                            to="/admin/settings/email"
                            className="text-foreground font-medium underline underline-offset-2"
                          >
                            {chunks}
                          </Link>
                        ),
                      },
                    )}
                  </span>
                </SettingFieldStatus>
              </div>
              <SettingField
                label={t("web.admin.settings.notifications.email.perEpisode")}
                description={t("web.admin.settings.notifications.perEpisodeOffDescription")}
                type="toggle"
                value={toggleValue("notifications.email.allow_per_episode")}
                onChange={(v) => form.setValue("notifications.email.allow_per_episode", v)}
                restartRequired={needsRestart("notifications.email.allow_per_episode")}
              />
              <AdvancedSection
                id="notifications.email"
                count={EMAIL_CHANNEL_ADVANCED_KEYS.length}
                forceOpen={anyDirty(EMAIL_CHANNEL_ADVANCED_KEYS)}
              >
                <SettingField
                  label={t("web.admin.settings.notifications.digestHour")}
                  description={t("web.admin.settings.notifications.digestHourDescription")}
                  unit="0-23"
                  type="number"
                  value={numberValue("notifications.email.digest_hour", "8")}
                  onChange={(v) => form.setValue("notifications.email.digest_hour", v)}
                  restartRequired={needsRestart("notifications.email.digest_hour")}
                />
              </AdvancedSection>
            </div>
          </ChannelCard>

          <ChannelCard
            icon={Bot}
            title={DISCORD_TITLE}
            description={t("web.admin.settings.notifications.discord.description")}
            enabled={discordOn}
            onEnabledChange={setToggle("notifications.discord_enabled")}
            chips={
              discordAppConfigured ? (
                <Chip tone="positive">
                  {t("web.admin.settings.notifications.discord.appConnected")}
                </Chip>
              ) : (
                <Chip tone={discordOn ? "warning" : "neutral"}>
                  {t("web.admin.settings.notifications.discord.appNotConnected")}
                </Chip>
              )
            }
          >
            <DiscordAppCredentials
              key={notificationScope(captureProfileRequestContext())}
              savedClientId={form.getValue("discord.client_id")}
              sensitiveConfigured={form.sensitiveConfigured}
              restartKeys={restartKeys}
            />
            <SettingsSubheading>
              {t("web.admin.settings.notifications.discord.delivery")}
            </SettingsSubheading>
            <div className="settings-field-list">
              <SettingField
                label={t("web.admin.settings.notifications.discord.perEpisode")}
                description={t("web.admin.settings.notifications.perEpisodeOffDescription")}
                type="toggle"
                value={toggleValue("notifications.discord.allow_per_episode")}
                onChange={(v) => form.setValue("notifications.discord.allow_per_episode", v)}
                restartRequired={needsRestart("notifications.discord.allow_per_episode")}
              />
              <AdvancedSection
                id="notifications.discord"
                count={DISCORD_ADVANCED_KEYS.length}
                forceOpen={anyDirty(DISCORD_ADVANCED_KEYS)}
              >
                <SettingField
                  label={t("web.admin.settings.notifications.discord.artwork")}
                  description={t("web.admin.settings.notifications.discord.artworkDescription")}
                  type="select"
                  value={form.getValue("notifications.discord.poster_mode") || "provider"}
                  options={[
                    {
                      value: "provider",
                      label: t("web.admin.settings.notifications.discord.artworkProvider"),
                    },
                    {
                      value: "server",
                      label: t("web.admin.settings.notifications.discord.artworkServer"),
                    },
                    {
                      value: "off",
                      label: t("web.admin.settings.notifications.discord.artworkOff"),
                    },
                  ]}
                  onChange={(v) => form.setValue("notifications.discord.poster_mode", v)}
                  restartRequired={needsRestart("notifications.discord.poster_mode")}
                />
                <SettingField
                  label={t("web.admin.settings.notifications.digestHour")}
                  description={t("web.admin.settings.notifications.digestHourDescription")}
                  unit="0-23"
                  type="number"
                  value={numberValue("notifications.discord.digest_hour", "8")}
                  onChange={(v) => form.setValue("notifications.discord.digest_hour", v)}
                  restartRequired={needsRestart("notifications.discord.digest_hour")}
                />
              </AdvancedSection>
            </div>
          </ChannelCard>

          <ChannelCard
            icon={Webhook}
            title={t("web.admin.settings.notifications.webhooks.title")}
            description={t("web.admin.settings.notifications.webhooks.description")}
            enabled={webhooksOn}
            onEnabledChange={setToggle("notifications.webhooks_enabled")}
            chips={
              allowPrivate ? (
                <Chip tone="warning">
                  {t("web.admin.settings.notifications.webhooks.privateAllowed")}
                </Chip>
              ) : undefined
            }
          >
            <AdvancedSection
              id="notifications.webhooks"
              count={WEBHOOK_ADVANCED_KEYS.length}
              forceOpen={anyDirty(WEBHOOK_ADVANCED_KEYS)}
            >
              <SettingField
                label={t("web.admin.settings.notifications.webhooks.perPerson")}
                type="number"
                value={numberValue("notifications.webhooks.max_per_profile", "10")}
                onChange={(v) => form.setValue("notifications.webhooks.max_per_profile", v)}
                restartRequired={needsRestart("notifications.webhooks.max_per_profile")}
              />
              <SettingField
                label={t("web.admin.settings.notifications.webhooks.deliveries")}
                // The limiter is in memory on each process
                // (internal/notifications/webhook_sender.go), so a cluster
                // allows this many per process, not per server.
                description={t("web.admin.settings.notifications.webhooks.deliveriesDescription")}
                unit={t("web.admin.settings.notifications.webhooks.perMinute")}
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
                label={t("web.admin.settings.notifications.webhooks.allowPrivate")}
                description={t("web.admin.settings.notifications.webhooks.allowPrivateDescription")}
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
                  <p>{t("web.admin.settings.notifications.webhooks.allowPrivateWarning")}</p>
                </div>
              )}
            </AdvancedSection>
          </ChannelCard>

          <ChannelCard
            icon={Megaphone}
            title={t("web.admin.settings.notifications.serverChannelsCard.title")}
            description={t("web.admin.settings.notifications.serverChannelsCard.description")}
            enabled={serverChannelsOn}
            onEnabledChange={setToggle("notifications.server_channels_enabled")}
            chips={
              <>
                {serverChannels != null && (
                  <Chip>
                    {t("web.admin.settings.notifications.serverChannelsCard.destinations", {
                      count: serverChannels.length,
                      count_text: serverChannels.length,
                    })}
                  </Chip>
                )}
                {failingServerChannels > 0 && (
                  <Chip tone="warning">
                    {t("web.admin.settings.notifications.serverChannelsCard.failing", {
                      count: failingServerChannels,
                      count_text: failingServerChannels,
                    })}
                  </Chip>
                )}
              </>
            }
          >
            <div className="settings-field-list">
              <div className="py-3">
                <ServerNotificationChannels />
              </div>
              <AdvancedSection
                id="notifications.server-channels"
                count={SERVER_CHANNELS_ADVANCED_KEYS.length}
                forceOpen={anyDirty(SERVER_CHANNELS_ADVANCED_KEYS)}
              >
                <SettingField
                  label={t("web.admin.settings.notifications.serverChannelsCard.batchWindow")}
                  description={t(
                    "web.admin.settings.notifications.serverChannelsCard.batchWindowDescription",
                  )}
                  unit={t("web.admin.settings.notifications.unit.seconds")}
                  type="number"
                  value={numberValue("notifications.server_channels.batch_seconds", "300")}
                  onChange={(v) => form.setValue("notifications.server_channels.batch_seconds", v)}
                  restartRequired={needsRestart("notifications.server_channels.batch_seconds")}
                />
                <SettingField
                  label={t("web.admin.settings.notifications.serverChannelsCard.mentionRequester")}
                  description={t(
                    "web.admin.settings.notifications.serverChannelsCard.mentionRequesterDescription",
                  )}
                  type="toggle"
                  value={form.getValue("notifications.server_channels.mention_requesters")}
                  onChange={(v) =>
                    form.setValue("notifications.server_channels.mention_requesters", v)
                  }
                  restartRequired={needsRestart("notifications.server_channels.mention_requesters")}
                />
              </AdvancedSection>
            </div>
          </ChannelCard>
        </FieldGroup>

        {/* ── Processing: everything here has a working default ── */}
        <FieldGroup
          label={t("web.admin.settings.notifications.processing.label")}
          description={t("web.admin.settings.notifications.processing.description")}
          restartAll={allRestart(PROCESSING_KEYS)}
          dirty={anyDirty(PROCESSING_KEYS)}
        >
          <AdvancedSection
            id="notifications.processing"
            count={PROCESSING_KEYS.length}
            // A switched-off pipeline is the one thing in here that stops every
            // channel, so it does not stay folded away.
            forceOpen={anyDirty(PROCESSING_KEYS) || pausedMessage !== null}
          >
            <AdvancedCluster label={t("web.admin.settings.notifications.processing.pipeline")}>
              <SettingField
                label={t("web.admin.settings.notifications.processing.noticeNew")}
                description={t("web.admin.settings.notifications.processing.noticeNewDescription")}
                type="toggle"
                value={toggleValue("notifications.release_events_enabled")}
                onChange={(v) => form.setValue("notifications.release_events_enabled", v)}
                restartRequired={needsRestart("notifications.release_events_enabled")}
              />
              <SettingField
                label={t("web.admin.settings.notifications.processing.fanout")}
                description={t("web.admin.settings.notifications.processing.fanoutDescription")}
                type="toggle"
                value={toggleValue("notifications.fanout_enabled")}
                onChange={(v) => form.setValue("notifications.fanout_enabled", v)}
                restartRequired={needsRestart("notifications.fanout_enabled")}
              />
            </AdvancedCluster>
            <AdvancedCluster label={t("web.admin.settings.notifications.processing.grouping")}>
              <SettingField
                label={t("web.admin.settings.notifications.processing.settle")}
                description={t("web.admin.settings.notifications.processing.settleDescription")}
                unit={t("web.admin.settings.notifications.unit.seconds")}
                type="number"
                value={numberValue("notifications.fanout.settle_seconds", "30")}
                onChange={(v) => form.setValue("notifications.fanout.settle_seconds", v)}
                restartRequired={needsRestart("notifications.fanout.settle_seconds")}
              />
              <SettingField
                label={t("web.admin.settings.notifications.processing.maxPerShow")}
                description={t("web.admin.settings.notifications.processing.maxPerShowDescription")}
                type="number"
                value={numberValue("notifications.fanout.max_series_burst", "3")}
                onChange={(v) => form.setValue("notifications.fanout.max_series_burst", v)}
                restartRequired={needsRestart("notifications.fanout.max_series_burst")}
              />
              <SettingField
                label={t("web.admin.settings.notifications.processing.maxAge")}
                description={t("web.admin.settings.notifications.processing.maxAgeDescription")}
                unit={t("web.admin.settings.notifications.unit.hours")}
                type="number"
                value={numberValue("notifications.fanout.max_event_age_hours", "72")}
                onChange={(v) => form.setValue("notifications.fanout.max_event_age_hours", v)}
                restartRequired={needsRestart("notifications.fanout.max_event_age_hours")}
              />
            </AdvancedCluster>
            <AdvancedCluster label={t("web.admin.settings.notifications.processing.retention")}>
              <SettingField
                label={t("web.admin.settings.notifications.processing.readNotifications")}
                unit={t("web.admin.settings.notifications.unit.days")}
                type="number"
                value={numberValue("notifications.retention.read_days", "90")}
                onChange={(v) => form.setValue("notifications.retention.read_days", v)}
                restartRequired={needsRestart("notifications.retention.read_days")}
              />
              <SettingField
                label={t("web.admin.settings.notifications.processing.unreadNotifications")}
                unit={t("web.admin.settings.notifications.unit.days")}
                type="number"
                value={numberValue("notifications.retention.unread_days", "180")}
                onChange={(v) => form.setValue("notifications.retention.unread_days", v)}
                restartRequired={needsRestart("notifications.retention.unread_days")}
              />
              <SettingField
                label={t("web.admin.settings.notifications.processing.sentHistory")}
                description={t(
                  "web.admin.settings.notifications.processing.sentHistoryDescription",
                )}
                unit={t("web.admin.settings.notifications.unit.days")}
                type="number"
                value={numberValue("notifications.retention.event_days", "30")}
                onChange={(v) => form.setValue("notifications.retention.event_days", v)}
                restartRequired={needsRestart("notifications.retention.event_days")}
              />
            </AdvancedCluster>
          </AdvancedSection>
        </FieldGroup>
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
