import { captureProfileRequestContext, isCapturedProfileAuthorityActive } from "@/api/client";
import {
  notificationScope,
  requireNotificationAuthority,
  captureNotificationAuthority,
} from "@/api/v2/notifications";
import { useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  BellRing,
  KeyRound,
  Loader2,
  MonitorSmartphone,
  Pencil,
  Plus,
  Send,
  Trash2,
  Webhook as WebhookIcon,
} from "lucide-react";
import { toast } from "sonner";
import { PageHeader } from "@/components/page/PageHeader";
import { invalidEmailMessage, isValidEmail } from "@/lib/email";
import type {
  NotificationChannelMode,
  NotificationEmailPreferences,
  NotificationPreferences,
  NotificationWebhook,
  NotificationWebhookInput,
  NotificationWebhookTestResult,
  WebPushSubscriptionView,
} from "@/api/types";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { SigningSecretDialog } from "@/components/SigningSecretDialog";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import {
  useClearEmailNotificationAddress,
  useDiscordLinkInit,
  useDiscordNotificationPreferences,
  useEmailNotificationPreferences,
  useNotificationPreferences,
  useRequestEmailNotificationAddress,
  useUnlinkDiscord,
  useUpdateDiscordNotificationPreferences,
  useUpdateEmailNotificationPreferences,
  useUpdateNotificationPreferences,
} from "@/hooks/queries/notifications";
import {
  useCreateNotificationWebhook,
  useDeleteNotificationWebhook,
  useDeleteWebPushSubscription,
  useNotificationCapability,
  useNotificationWebhooks,
  useRotateNotificationWebhookSecret,
  useTestNotificationWebhook,
  useUpdateNotificationWebhook,
  useWebPushSubscriptions,
} from "@/hooks/queries/notificationWebhooks";
import { notificationKeys } from "@/hooks/queries/keys";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useOptionalAuth } from "@/hooks/useAuth";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { formatRelativeTime } from "@/lib/date";
import { useT, type MessageKey, type Translate } from "@/i18n";
import {
  currentWebPushSubscription,
  disableWebPush,
  enableWebPush,
  webPushSupport,
} from "@/lib/webPush";

const REASON_FIELDS = [
  { key: "notify_favorites", labelKey: "web.settings.notifications.reason.favorites" },
  { key: "notify_watchlist", labelKey: "web.settings.notifications.reason.watchlist" },
  {
    key: "notify_continue_watching",
    labelKey: "web.settings.notifications.reason.continueWatching",
  },
  { key: "notify_next_up", labelKey: "web.settings.notifications.reason.nextUp" },
] as const satisfies readonly { key: string; labelKey: MessageKey }[];

// Webhooks additionally carry the request.fulfilled toggle; it is not an
// episode reason, so the profile preferences section keeps REASON_FIELDS.
const WEBHOOK_NOTIFY_FIELDS = [
  ...REASON_FIELDS,
  { key: "notify_requests", labelKey: "web.settings.notifications.reason.requests" },
] as const satisfies readonly { key: string; labelKey: MessageKey }[];

/** Example URL in the create dialog's URL field; a URL, so not translated. */
const DISCORD_WEBHOOK_URL_EXAMPLE = "https://discord.com/api/webhooks/…";

type WebhookNotifyKey = (typeof WEBHOOK_NOTIFY_FIELDS)[number]["key"];

function PreferencesSection() {
  const t = useT();
  const { data: prefs, isLoading } = useNotificationPreferences();
  const updatePrefs = useUpdateNotificationPreferences();
  const { data: capability } = useNotificationCapability();

  if (isLoading || !prefs) {
    return (
      <SettingsGroup title={t("web.settings.notifications.episodes.title")}>
        <Skeleton className="h-24 w-full" />
      </SettingsGroup>
    );
  }

  return (
    <SettingsGroup
      title={t("web.settings.notifications.episodes.title")}
      description={
        <>
          {t("web.settings.notifications.episodes.description")}
          {capability?.series?.available && (
            <>
              {" "}
              <Link
                to="/notifications?view=series"
                className="text-foreground underline underline-offset-4"
              >
                {t("web.settings.notifications.episodes.seriesLink")}
              </Link>
            </>
          )}
        </>
      }
    >
      <div className="flex items-center justify-between gap-3">
        <div>
          <div className="text-sm font-medium">
            {t("web.settings.notifications.episodes.enable")}
          </div>
          <div className="text-muted-foreground text-xs">
            {t("web.settings.notifications.episodes.enableHint")}
          </div>
        </div>
        <Switch
          checked={prefs.enabled}
          onCheckedChange={(checked) => updatePrefs.mutate({ enabled: checked })}
        />
      </div>
      {REASON_FIELDS.map((field) => (
        <div key={field.key} className="flex items-center justify-between gap-3">
          <div className="text-sm">{t(field.labelKey)}</div>
          <Switch
            checked={prefs[field.key as keyof NotificationPreferences] as boolean}
            disabled={!prefs.enabled}
            onCheckedChange={(checked) => updatePrefs.mutate({ [field.key]: checked })}
          />
        </div>
      ))}
    </SettingsGroup>
  );
}

/**
 * Frequency picker shared by the account-level digest channels (email,
 * Discord). Per-episode stays listed-but-disabled when the admin allowance is
 * off and the account is still set to it, so the coercion is visible.
 */
function ChannelFrequencyRow({
  mode,
  allowPerEpisode,
  digestHour,
  isPending,
  perEpisodeHint,
  onChange,
}: {
  mode: NotificationChannelMode;
  allowPerEpisode: boolean;
  digestHour: string;
  isPending: boolean;
  perEpisodeHint: string;
  onChange: (mode: NotificationChannelMode) => void;
}) {
  const t = useT();
  const digestText = t("web.settings.notifications.frequency.digestHint", { hour: digestHour });
  return (
    <div className="flex items-center justify-between gap-3">
      <div>
        <div className="text-sm">{t("web.settings.notifications.frequency.label")}</div>
        <div className="text-muted-foreground text-xs">
          {mode === "per_episode"
            ? perEpisodeHint
            : mode === "per_episode_and_digest"
              ? t("web.settings.notifications.frequency.perEpisodeAndDigestHint", {
                  per_episode: perEpisodeHint,
                  hour: digestHour,
                })
              : digestText}
        </div>
      </div>
      <Select
        value={mode}
        disabled={isPending}
        onValueChange={(value) => onChange(value as NotificationChannelMode)}
      >
        <SelectTrigger className="w-[220px]">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="daily_digest">
            {t("web.settings.notifications.frequency.dailyDigest")}
          </SelectItem>
          {(allowPerEpisode || mode === "per_episode") && (
            <SelectItem value="per_episode" disabled={!allowPerEpisode}>
              {t("web.settings.notifications.frequency.perEpisode")}
            </SelectItem>
          )}
          {(allowPerEpisode || mode === "per_episode_and_digest") && (
            <SelectItem value="per_episode_and_digest" disabled={!allowPerEpisode}>
              {t("web.settings.notifications.frequency.perEpisodeAndDigest")}
            </SelectItem>
          )}
        </SelectContent>
      </Select>
    </div>
  );
}

/**
 * Destination address for this profile's emails. There is no account-email
 * fallback: the profile receives nothing until an address is verified here.
 * Changing it queues a verification request for the new address; the old address
 * keeps receiving mail until the link is clicked. Removing the address also
 * turns the channel off. Child profiles cannot set addresses.
 */
function EmailDestinationRow({ prefs }: { prefs: NotificationEmailPreferences }) {
  const t = useT();
  const [editing, setEditing] = useState(false);
  const [address, setAddress] = useState("");
  const requestAddress = useRequestEmailNotificationAddress();
  const clearAddress = useClearEmailNotificationAddress();

  const hasAddress = prefs.custom_email !== "";

  const submit = () => {
    const trimmed = address.trim();
    if (!trimmed || requestAddress.isPending) {
      return;
    }
    if (!isValidEmail(trimmed)) {
      toast.error(invalidEmailMessage());
      return;
    }
    requestAddress.mutate(trimmed, {
      onSuccess: () => {
        setEditing(false);
        setAddress("");
      },
    });
  };

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-3">
        <div>
          <div className="text-sm">{t("web.settings.notifications.email.deliverTo")}</div>
          <div className="text-muted-foreground text-xs">
            {hasAddress ? prefs.custom_email : t("web.settings.notifications.email.noAddress")}
          </div>
        </div>
        {prefs.can_edit_address && (
          <div className="flex items-center gap-2">
            {hasAddress && (
              <Button
                variant="ghost"
                size="sm"
                disabled={clearAddress.isPending || requestAddress.isPending}
                onClick={() => clearAddress.mutate()}
              >
                {t("web.settings.notifications.email.remove")}
              </Button>
            )}
            <Button
              variant="outline"
              size="sm"
              disabled={requestAddress.isPending}
              onClick={() => setEditing((value) => !value)}
            >
              {editing
                ? t("common.cancel")
                : hasAddress
                  ? t("web.settings.notifications.email.change")
                  : t("web.settings.notifications.email.addAddress")}
            </Button>
          </div>
        )}
      </div>
      {editing && (
        <div className="flex items-center gap-2">
          <Input
            type="email"
            disabled={requestAddress.isPending}
            placeholder={t("web.settings.notifications.email.addressPlaceholder")}
            value={address}
            onChange={(event) => setAddress(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                submit();
              }
            }}
            className="max-w-xs"
          />
          <Button size="sm" disabled={requestAddress.isPending || !address.trim()} onClick={submit}>
            {requestAddress.isPending && <Loader2 className="mr-1 h-3 w-3 animate-spin" />}
            {t("web.settings.notifications.email.requestVerification")}
          </Button>
        </div>
      )}
      {prefs.pending_email !== "" && (
        <div className="text-xs text-amber-500">
          {t("web.settings.notifications.email.pending", { email: prefs.pending_email })}
        </div>
      )}
      {!prefs.can_edit_address && (
        <div className="text-muted-foreground text-xs">
          {t("web.settings.notifications.email.childProfile")}
        </div>
      )}
    </div>
  );
}

function EmailSection() {
  const t = useT();
  const capability = useNotificationCapability();
  const emailCap = capability.data?.email;
  const available = emailCap?.available ?? false;
  const { data: prefs, isLoading } = useEmailNotificationPreferences(available);
  const updatePrefs = useUpdateEmailNotificationPreferences();

  if (!available) {
    return null;
  }

  const mode = prefs?.mode ?? "off";
  const enabled = mode !== "off";
  const allowPerEpisode = emailCap?.modes.includes("per_episode") ?? false;
  const digestHour = String(emailCap?.digest_hour ?? 8).padStart(2, "0");

  if (isLoading || !prefs) {
    return (
      <SettingsGroup title={t("web.settings.notifications.email.title")}>
        <Skeleton className="h-16 w-full" />
      </SettingsGroup>
    );
  }

  const hasAddress = prefs.custom_email !== "";

  return (
    <SettingsGroup
      title={t("web.settings.notifications.email.title")}
      description={t("web.settings.notifications.email.description")}
    >
      <div className="flex items-center justify-between gap-3">
        <div>
          <div className="text-sm font-medium">{t("web.settings.notifications.email.enable")}</div>
          <div className="text-muted-foreground text-xs">
            {t("web.settings.notifications.email.enableHint")}
          </div>
        </div>
        <Switch
          checked={enabled}
          disabled={updatePrefs.isPending || (!enabled && !hasAddress)}
          onCheckedChange={(checked) =>
            updatePrefs.mutate({ mode: checked ? "daily_digest" : "off" })
          }
        />
      </div>
      <EmailDestinationRow prefs={prefs} />
      {enabled && (
        <ChannelFrequencyRow
          mode={mode}
          allowPerEpisode={allowPerEpisode}
          digestHour={digestHour}
          isPending={updatePrefs.isPending}
          perEpisodeHint={t("web.settings.notifications.email.perEpisodeHint")}
          onChange={(value) => updatePrefs.mutate({ mode: value })}
        />
      )}
      {enabled && mode !== "daily_digest" && !allowPerEpisode && (
        <div className="text-xs text-amber-500">
          {t("web.settings.notifications.email.perEpisodeDisabled")}
        </div>
      )}
    </SettingsGroup>
  );
}

const DISCORD_LINK_ERRORS: Record<string, MessageKey> = {
  denied: "web.settings.notifications.discord.linkError.denied",
  disabled: "web.settings.notifications.discord.linkError.disabled",
  invalid_callback: "web.settings.notifications.discord.linkError.invalidCallback",
  state_invalid: "web.settings.notifications.discord.linkError.stateInvalid",
  exchange_failed: "web.settings.notifications.discord.linkError.exchangeFailed",
};

function DiscordSection() {
  const t = useT();
  const { profile } = useCurrentProfile();
  const canManage = profile?.is_primary === true;
  const authority = captureProfileRequestContext();
  const queryClient = useQueryClient();
  const [searchParams, setSearchParams] = useSearchParams();
  const capability = useNotificationCapability();
  const discordCap = capability.data?.discord;
  const available = discordCap?.available ?? false;
  const { data: prefs, isLoading } = useDiscordNotificationPreferences(available);
  const updatePrefs = useUpdateDiscordNotificationPreferences();
  const linkInit = useDiscordLinkInit();
  const unlink = useUnlinkDiscord();

  // Surface the OAuth callback result (the link flow round-trips through
  // Discord and lands back here with a query param), then clean the URL.
  useEffect(() => {
    const linked = searchParams.get("discord_linked");
    const error = searchParams.get("discord_error");
    if (!linked && !error) {
      return;
    }
    if (linked) {
      toast.success(t("web.settings.notifications.discord.linked"));
      void queryClient.invalidateQueries({ queryKey: notificationKeys.discordPreferences() });
    } else if (error) {
      toast.error(
        t(DISCORD_LINK_ERRORS[error] ?? "web.settings.notifications.discord.linkError.generic"),
      );
    }
    const next = new URLSearchParams(searchParams);
    next.delete("discord_linked");
    next.delete("discord_error");
    setSearchParams(next, { replace: true });
  }, [searchParams, setSearchParams, queryClient, t]);

  if (!available) {
    return null;
  }

  if (isLoading) {
    return (
      <SettingsGroup title={t("web.settings.notifications.discord.title")}>
        <Skeleton className="h-16 w-full" />
      </SettingsGroup>
    );
  }

  const linked = prefs?.linked ?? false;
  const mode = prefs?.mode ?? "off";
  const enabled = mode !== "off";
  const allowPerEpisode = discordCap?.modes.includes("per_episode") ?? false;
  const digestHour = String(discordCap?.digest_hour ?? 8).padStart(2, "0");

  const startLink = () => {
    linkInit.mutate(undefined, {
      onSuccess: (init) => {
        if (authority && isCapturedProfileAuthorityActive(authority))
          window.location.assign(init.url);
      },
    });
  };

  return (
    <SettingsGroup
      title={t("web.settings.notifications.discord.title")}
      description={t("web.settings.notifications.discord.description")}
    >
      {!canManage && (
        <p className="text-muted-foreground text-sm">
          {t("web.settings.notifications.discord.primaryOnly")}
        </p>
      )}
      {!linked ? (
        <div className="flex items-center justify-between gap-3">
          <div>
            <div className="text-sm font-medium">
              {t("web.settings.notifications.discord.link")}
            </div>
            <div className="text-muted-foreground text-xs">
              {t("web.settings.notifications.discord.linkHint")}
            </div>
          </div>
          <Button size="sm" disabled={!canManage || linkInit.isPending} onClick={startLink}>
            {linkInit.isPending && <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />}
            {t("web.settings.notifications.discord.linkButton")}
          </Button>
        </div>
      ) : (
        <>
          <div className="flex items-center justify-between gap-3">
            <div>
              <div className="text-sm font-medium">
                {t("web.settings.notifications.discord.linkedAs", {
                  name:
                    prefs?.discord_username || t("web.settings.notifications.discord.unknownUser"),
                })}
              </div>
              <div className="text-muted-foreground text-xs">
                {t("web.settings.notifications.discord.enableHint")}
              </div>
            </div>
            <div className="flex items-center gap-2">
              <Switch
                checked={enabled}
                disabled={!canManage || updatePrefs.isPending}
                onCheckedChange={(checked) =>
                  updatePrefs.mutate({ mode: checked ? "daily_digest" : "off" })
                }
              />
              <Button
                variant="ghost"
                size="sm"
                className="text-destructive"
                disabled={!canManage || unlink.isPending}
                onClick={() => unlink.mutate()}
              >
                {t("web.settings.notifications.discord.unlink")}
              </Button>
            </div>
          </div>
          {enabled && (
            <ChannelFrequencyRow
              mode={mode}
              allowPerEpisode={allowPerEpisode}
              digestHour={digestHour}
              isPending={!canManage || updatePrefs.isPending}
              perEpisodeHint={t("web.settings.notifications.discord.perEpisodeHint")}
              onChange={(value) => updatePrefs.mutate({ mode: value })}
            />
          )}
          {enabled && mode !== "daily_digest" && !allowPerEpisode && (
            <div className="text-xs text-amber-500">
              {t("web.settings.notifications.discord.perEpisodeDisabled")}
            </div>
          )}
          {enabled && prefs?.link_failure && (
            <div className="flex items-start gap-1.5 text-xs text-amber-500">
              <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              <span>{prefs.link_failure}</span>
            </div>
          )}
        </>
      )}
    </SettingsGroup>
  );
}

/**
 * Delivery health for one push subscription, derived the same way as webhook
 * health: a failure newer than the last success means the device is failing.
 */
function webPushHealth(
  t: Translate,
  sub: WebPushSubscriptionView,
): { text: string; failing: boolean; delivered?: false } {
  if (!sub.enabled) {
    return { text: t("web.settings.notifications.push.disabledAfterFailures"), failing: true };
  }
  const failing =
    sub.last_failure_at != null &&
    (sub.last_success_at == null || sub.last_failure_at > sub.last_success_at);
  if (failing) {
    const when = formatRelativeTime(sub.last_failure_at);
    return {
      text: when
        ? t("web.settings.notifications.push.lastFailedAt", { when })
        : t("web.settings.notifications.push.lastFailed"),
      failing: true,
    };
  }
  if (sub.last_success_at) {
    const when = formatRelativeTime(sub.last_success_at);
    return {
      text: when
        ? t("web.settings.notifications.push.lastDeliveredAt", { when })
        : t("web.settings.notifications.push.delivering"),
      failing: false,
    };
  }
  return {
    text: t("web.settings.notifications.push.noDeliveries"),
    failing: false,
    delivered: false,
  };
}

function webPushSubtitle(
  t: Translate,
  sub: WebPushSubscriptionView,
): { text: string; failing: boolean; delivered?: false } {
  const health = webPushHealth(t, sub);
  const added = formatRelativeTime(sub.created_at);
  return {
    text: added
      ? t("web.settings.notifications.push.addedWithStatus", { added, status: health.text })
      : health.text,
    failing: health.failing,
  };
}

function WebPushSection() {
  const t = useT();
  const queryClient = useQueryClient();
  const capability = useNotificationCapability();
  const userId = useOptionalAuth()?.user?.id;
  const webPushCap = capability.data?.web_push;
  const available = webPushCap?.available ?? false;
  const { data: subscriptions } = useWebPushSubscriptions(available);
  const removeSubscription = useDeleteWebPushSubscription();
  const support = webPushSupport();
  const [thisEndpoint, setThisEndpoint] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    void currentWebPushSubscription().then((sub) => setThisEndpoint(sub?.endpoint ?? null));
  }, []);

  const thisSub =
    thisEndpoint != null
      ? (subscriptions ?? []).find((sub) => sub.endpoint === thisEndpoint)
      : undefined;
  const subscribedHere = thisSub != null;
  const thisHealth = thisSub ? webPushHealth(t, thisSub) : null;

  const enable = async () => {
    if (!webPushCap?.public_key || userId == null) {
      toast.error(t("web.settings.notifications.push.unavailable"));
      return;
    }
    const authority = captureNotificationAuthority();
    setBusy(true);
    try {
      await enableWebPush(webPushCap.public_key, userId, authority);
      requireNotificationAuthority(authority);
      const sub = await currentWebPushSubscription();
      requireNotificationAuthority(authority);
      setThisEndpoint(sub?.endpoint ?? null);
      toast.success(t("web.settings.notifications.push.enabled"));
    } catch (error) {
      if (isCapturedProfileAuthorityActive(authority))
        toast.error(
          error instanceof Error
            ? error.message
            : t("web.settings.notifications.push.enableFailed"),
        );
    } finally {
      setBusy(false);
      if (isCapturedProfileAuthorityActive(authority))
        void queryClient.invalidateQueries({
          queryKey: [...notificationKeys.webPushSubscriptions(), notificationScope(authority)],
          exact: true,
        });
    }
  };

  const disable = async () => {
    const authority = captureNotificationAuthority();
    setBusy(true);
    try {
      await disableWebPush(authority);
      requireNotificationAuthority(authority);
      setThisEndpoint(null);
    } catch (error) {
      if (isCapturedProfileAuthorityActive(authority))
        toast.error(
          error instanceof Error
            ? error.message
            : t("web.settings.notifications.push.disableFailed"),
        );
    } finally {
      setBusy(false);
      if (isCapturedProfileAuthorityActive(authority))
        void queryClient.invalidateQueries({
          queryKey: [...notificationKeys.webPushSubscriptions(), notificationScope(authority)],
          exact: true,
        });
    }
  };

  if (!available) {
    return null;
  }

  const otherSubscriptions = (subscriptions ?? []).filter((sub) => sub.endpoint !== thisEndpoint);

  return (
    <SettingsGroup
      title={t("web.settings.notifications.push.title")}
      description={t("web.settings.notifications.push.description")}
    >
      {support === "unsupported" ? (
        <div className="text-muted-foreground text-sm">
          {t("web.settings.notifications.push.unsupported")}
        </div>
      ) : support === "denied" && !subscribedHere ? (
        <div className="text-muted-foreground text-sm">
          {t("web.settings.notifications.push.blocked")}
        </div>
      ) : (
        <div className="flex items-center justify-between gap-3">
          <div className="flex items-center gap-2">
            <BellRing className="text-muted-foreground h-4 w-4" />
            <div>
              <div className="text-sm font-medium">
                {t("web.settings.notifications.push.thisBrowser")}
              </div>
              <div
                className={
                  thisHealth?.failing ? "text-xs text-amber-500" : "text-muted-foreground text-xs"
                }
              >
                {!subscribedHere
                  ? t("web.settings.notifications.push.notReceiving")
                  : thisHealth?.failing
                    ? thisHealth.text
                    : thisHealth && thisHealth.delivered !== false
                      ? t("web.settings.notifications.push.receivingWithStatus", {
                          status: thisHealth.text,
                        })
                      : t("web.settings.notifications.push.receiving")}
              </div>
            </div>
          </div>
          <Button
            variant={subscribedHere ? "outline" : "default"}
            size="sm"
            disabled={busy}
            onClick={() => void (subscribedHere ? disable() : enable())}
          >
            {busy && <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />}
            {subscribedHere
              ? t("web.settings.notifications.push.disable")
              : t("web.settings.notifications.push.enable")}
          </Button>
        </div>
      )}

      {otherSubscriptions.length > 0 && (
        <div className="space-y-2">
          <div className="text-muted-foreground text-xs font-medium">
            {t("web.settings.notifications.push.otherDevices", {
              count: String(otherSubscriptions.length),
            })}
          </div>
          {otherSubscriptions.map((sub) => {
            const subtitle = webPushSubtitle(t, sub);
            return (
              <div key={sub.id} className="flex items-center justify-between gap-3">
                <div className="flex min-w-0 items-center gap-2">
                  <MonitorSmartphone className="text-muted-foreground h-4 w-4 shrink-0" />
                  <div className="min-w-0">
                    <div className="truncate text-sm">
                      {sub.device_name || t("web.settings.notifications.push.unknownDevice")}
                    </div>
                    <div
                      className={
                        subtitle.failing
                          ? "truncate text-xs text-amber-500"
                          : "text-muted-foreground truncate text-xs"
                      }
                    >
                      {subtitle.text}
                    </div>
                  </div>
                </div>
                <Button
                  variant="ghost"
                  size="sm"
                  className="text-destructive"
                  disabled={removeSubscription.isPending}
                  onClick={() => removeSubscription.mutate(sub.id)}
                >
                  <Trash2 className="mr-1.5 h-3.5 w-3.5" />
                  {t("web.settings.notifications.push.remove")}
                </Button>
              </div>
            );
          })}
        </div>
      )}
    </SettingsGroup>
  );
}

function WebhookFormDialog({
  open,
  onOpenChange,
  webhook,
  globalPrefs,
  onSecret,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  webhook: NotificationWebhook | null;
  globalPrefs: NotificationPreferences | undefined;
  onSecret: (secret: string) => void;
}) {
  const t = useT();
  const create = useCreateNotificationWebhook();
  const update = useUpdateNotificationWebhook();
  const [name, setName] = useState(webhook?.name ?? "");
  const [url, setUrl] = useState("");
  const [reasons, setReasons] = useState<Record<WebhookNotifyKey, boolean>>({
    notify_favorites: webhook?.notify_favorites ?? true,
    notify_watchlist: webhook?.notify_watchlist ?? true,
    notify_continue_watching: webhook?.notify_continue_watching ?? true,
    notify_next_up: webhook?.notify_next_up ?? true,
    notify_requests: webhook?.notify_requests ?? true,
  });
  const pending = create.isPending || update.isPending;
  const editing = webhook != null;

  const submit = () => {
    const input: NotificationWebhookInput = { name: name.trim(), ...reasons };
    if (url.trim()) {
      input.url = url.trim();
    }
    if (editing) {
      update.mutate(
        { id: webhook.id, etag: webhook.etag ?? "", ...input },
        {
          onSuccess: () => onOpenChange(false),
        },
      );
      return;
    }
    if (!input.url) {
      toast.error(t("web.settings.notifications.webhooks.urlRequired"));
      return;
    }
    const authority = captureNotificationAuthority();
    create.mutate(input, {
      onSuccess: (created) => {
        try {
          requireNotificationAuthority(authority);
        } catch {
          return;
        }
        onOpenChange(false);
        toast.success(t("web.settings.notifications.webhooks.created", { name: created.name }));
        if (created.signing_secret) {
          onSecret(created.signing_secret);
        }
      },
      onError: (error) => {
        try {
          requireNotificationAuthority(authority);
        } catch {
          return;
        }
        toast.error(
          error instanceof Error
            ? error.message
            : t("web.settings.notifications.webhooks.createFailed"),
        );
      },
    });
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {editing
              ? t("web.settings.notifications.webhooks.editTitle", { name: webhook.name })
              : t("web.settings.notifications.webhooks.add")}
          </DialogTitle>
          <DialogDescription>
            {t("web.settings.notifications.webhooks.dialogDescription")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="webhook-name">{t("web.settings.notifications.webhooks.name")}</Label>
            <Input
              id="webhook-name"
              value={name}
              maxLength={64}
              placeholder={t("web.settings.notifications.webhooks.namePlaceholder")}
              onChange={(event) => setName(event.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="webhook-url">
              {editing
                ? t("web.settings.notifications.webhooks.replaceUrl")
                : t("web.settings.notifications.webhooks.url")}
            </Label>
            <Input
              id="webhook-url"
              value={url}
              placeholder={
                editing
                  ? t("web.settings.notifications.webhooks.currentUrl", { host: webhook.url_host })
                  : DISCORD_WEBHOOK_URL_EXAMPLE
              }
              onChange={(event) => setUrl(event.target.value)}
            />
          </div>
          <div className="space-y-2">
            <Label>{t("web.settings.notifications.webhooks.sendFor")}</Label>
            {WEBHOOK_NOTIFY_FIELDS.map((field) => {
              // Requests have no per-profile reason toggle; only the master
              // switch suppresses them.
              const globallyDisabled =
                globalPrefs != null &&
                (!globalPrefs.enabled ||
                  (field.key !== "notify_requests" &&
                    !(globalPrefs[field.key as keyof NotificationPreferences] as boolean)));
              return (
                <div key={field.key} className="flex items-center justify-between gap-3">
                  <div className="text-sm">
                    {t(field.labelKey)}
                    {globallyDisabled && (
                      <span className="text-muted-foreground ml-2 text-xs">
                        {t("web.settings.notifications.webhooks.disabledInProfile")}
                      </span>
                    )}
                  </div>
                  <Switch
                    checked={reasons[field.key]}
                    disabled={globallyDisabled}
                    onCheckedChange={(checked) =>
                      setReasons((current) => ({ ...current, [field.key]: checked }))
                    }
                  />
                </div>
              );
            })}
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button onClick={submit} disabled={pending || !name.trim()}>
            {pending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
            {editing ? t("common.save") : t("web.settings.notifications.webhooks.create")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function WebhookCard({
  webhook,
  onEdit,
  onSecret,
}: {
  webhook: NotificationWebhook;
  onEdit: () => void;
  onSecret: (secret: string) => void;
}) {
  const t = useT();
  const update = useUpdateNotificationWebhook();
  const remove = useDeleteNotificationWebhook();
  const authority = captureProfileRequestContext();
  const test = useTestNotificationWebhook();
  const rotate = useRotateNotificationWebhookSecret();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [deleteIntent, setDeleteIntent] = useState<{ id: string; etag: string } | null>(null);
  const [testResult, setTestResult] = useState<NotificationWebhookTestResult | null>(null);

  const lastSuccess = formatRelativeTime(webhook.last_success_at);
  const lastFailure = formatRelativeTime(webhook.last_failure_at);
  const failing =
    webhook.last_failure_at != null &&
    (webhook.last_success_at == null || webhook.last_failure_at > webhook.last_success_at);
  const enabledReasons = WEBHOOK_NOTIFY_FIELDS.filter(
    (field) => webhook[field.key as keyof NotificationWebhook] as boolean,
  ).map((field) => t(field.labelKey));
  const failureMessage =
    webhook.last_failure_message ||
    (webhook.last_failure_status != null
      ? t("web.settings.notifications.webhooks.httpStatus", {
          status: String(webhook.last_failure_status),
        })
      : t("web.settings.notifications.webhooks.httpError"));
  const testResultText = testResult
    ? t(
        testResult.http_status
          ? "web.settings.notifications.webhooks.testResultHttp"
          : "web.settings.notifications.webhooks.testResult",
        {
          outcome: testResult.ok ? "ok" : "failed",
          status: String(testResult.http_status),
          duration: String(testResult.duration_ms),
        },
      )
    : "";

  return (
    <div className="border-border/60 space-y-2 rounded-xl border p-4">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-medium">{webhook.name}</span>
        <Badge variant="secondary">{webhook.type}</Badge>
        <span className="text-muted-foreground text-xs">{webhook.url_host}</span>
        <div className="ml-auto flex items-center gap-1.5">
          <span className="text-muted-foreground text-xs">
            {webhook.enabled ? t("web.settings.value.enabled") : t("web.settings.value.disabled")}
          </span>
          <Switch
            checked={webhook.enabled}
            onCheckedChange={(checked) =>
              update.mutate({ id: webhook.id, etag: webhook.etag ?? "", enabled: checked })
            }
          />
        </div>
      </div>

      <div className="text-muted-foreground text-xs">
        {enabledReasons.length === WEBHOOK_NOTIFY_FIELDS.length
          ? t("web.settings.notifications.webhooks.allReasons")
          : enabledReasons.length > 0
            ? enabledReasons.join(" · ")
            : t("web.settings.notifications.webhooks.noReasons")}
      </div>

      {lastSuccess && !failing && (
        <div className="text-muted-foreground text-xs">
          {t("web.settings.notifications.webhooks.lastSuccess", { when: lastSuccess })}
        </div>
      )}
      {failing && (
        <div className="flex items-start gap-1.5 text-xs text-amber-500">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          <span>
            {webhook.disabled_reason
              ? t("web.settings.notifications.webhooks.disabledReason", {
                  reason: webhook.disabled_reason,
                })
              : lastFailure
                ? t("web.settings.notifications.webhooks.lastFailureAt", {
                    when: lastFailure,
                    message: failureMessage,
                  })
                : t("web.settings.notifications.webhooks.lastFailure", { message: failureMessage })}
          </span>
        </div>
      )}
      {testResult && (
        <div className={`text-xs ${testResult.ok ? "text-emerald-500" : "text-amber-500"}`}>
          {testResult.message
            ? t("web.settings.notifications.webhooks.testResultDetail", {
                result: testResultText,
                message: testResult.message,
              })
            : testResultText}
        </div>
      )}

      <div className="flex flex-wrap gap-1.5 pt-1">
        <Button
          variant="outline"
          size="sm"
          disabled={test.isPending}
          onClick={() =>
            test.mutate(webhook.id, {
              onSuccess: (result) => {
                if (authority && isCapturedProfileAuthorityActive(authority)) setTestResult(result);
              },
              onError: () => {
                if (authority && isCapturedProfileAuthorityActive(authority))
                  toast.error(t("web.settings.notifications.webhooks.testFailed"));
              },
            })
          }
        >
          {test.isPending ? (
            <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
          ) : (
            <Send className="mr-1.5 h-3.5 w-3.5" />
          )}
          {t("web.settings.notifications.webhooks.test")}
        </Button>
        <Button variant="outline" size="sm" onClick={onEdit}>
          <Pencil className="mr-1.5 h-3.5 w-3.5" />
          {t("web.settings.notifications.webhooks.edit")}
        </Button>
        {webhook.type === "generic" && (
          <Button
            variant="outline"
            size="sm"
            disabled={rotate.isPending}
            onClick={() =>
              rotate.mutate(webhook.id, {
                onSuccess: (result) => onSecret(result.signing_secret),
              })
            }
          >
            <KeyRound className="mr-1.5 h-3.5 w-3.5" />
            {t("web.settings.notifications.webhooks.rotateSecret")}
          </Button>
        )}
        <Button
          variant="outline"
          size="sm"
          className="text-destructive"
          onClick={() => {
            setDeleteIntent({ id: webhook.id, etag: webhook.etag ?? "" });
            setConfirmDelete(true);
          }}
        >
          <Trash2 className="mr-1.5 h-3.5 w-3.5" />
          {t("web.settings.notifications.webhooks.delete")}
        </Button>
      </div>

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={t("web.settings.notifications.webhooks.deleteTitle", { name: webhook.name })}
        description={t("web.settings.notifications.webhooks.deleteDescription")}
        confirmLabel={t("web.settings.notifications.webhooks.delete")}
        variant="destructive"
        isPending={remove.isPending}
        onConfirm={() => {
          if (deleteIntent)
            remove.mutate(deleteIntent, { onSettled: () => setConfirmDelete(false) });
        }}
      />
      {/* The edit dialog is hosted by the parent so state resets per webhook. */}
      {update.isPending && (
        <span className="sr-only">{t("web.settings.notifications.webhooks.saving")}</span>
      )}
    </div>
  );
}

function WebhooksSection() {
  const t = useT();
  const capability = useNotificationCapability();
  const webhooksAvailable = capability.data?.webhooks.available ?? false;
  const { data: webhooks, isLoading } = useNotificationWebhooks(webhooksAvailable);
  const { data: globalPrefs } = useNotificationPreferences();
  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<NotificationWebhook | null>(null);
  const [secret, setSecret] = useState<string | null>(null);

  const maxPerProfile = capability.data?.webhooks.max_per_profile ?? 10;
  const atLimit = (webhooks?.length ?? 0) >= maxPerProfile;

  if (!webhooksAvailable) {
    return null;
  }

  return (
    <>
      <SettingsGroup
        title={t("web.settings.notifications.webhooks.title")}
        description={t("web.settings.notifications.webhooks.description")}
      >
        {isLoading ? (
          <Skeleton className="h-24 w-full" />
        ) : (
          <>
            {(webhooks ?? []).map((webhook) => (
              <WebhookCard
                key={`${notificationScope()}:${webhook.id}`}
                webhook={webhook}
                onSecret={setSecret}
                onEdit={() => {
                  setEditing(webhook);
                  setFormOpen(true);
                }}
              />
            ))}
            {(webhooks ?? []).length === 0 && (
              <div className="text-muted-foreground flex items-center gap-2 text-sm">
                <WebhookIcon className="h-4 w-4" />
                {t("web.settings.notifications.webhooks.empty")}
              </div>
            )}
            <div>
              <Button
                variant="outline"
                size="sm"
                disabled={atLimit}
                onClick={() => {
                  setEditing(null);
                  setFormOpen(true);
                }}
              >
                <Plus className="mr-1.5 h-4 w-4" />
                {t("web.settings.notifications.webhooks.add")}
              </Button>
              {atLimit && (
                <span className="text-muted-foreground ml-2 text-xs">
                  {t("web.settings.notifications.webhooks.limit", {
                    count: maxPerProfile,
                    count_text: String(maxPerProfile),
                  })}
                </span>
              )}
            </div>
          </>
        )}
      </SettingsGroup>

      {formOpen && (
        <WebhookFormDialog
          key={`${notificationScope()}:${editing?.id ?? "new"}`}
          open={formOpen}
          onOpenChange={(open) => {
            setFormOpen(open);
            if (!open) {
              setEditing(null);
            }
          }}
          webhook={editing}
          globalPrefs={globalPrefs}
          onSecret={setSecret}
        />
      )}
      <SigningSecretDialog secret={secret} onClose={() => setSecret(null)} />
    </>
  );
}

export default function NotificationsSettings() {
  const t = useT();
  useDocumentTitle(t("web.settings.notifications.documentTitle"));

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("web.settings.notifications.title")}
        subtitle={t("web.settings.notifications.subtitle")}
      />

      <PreferencesSection />

      <WebPushSection />

      <EmailSection />

      <DiscordSection key={notificationScope()} />

      <WebhooksSection />
    </div>
  );
}
