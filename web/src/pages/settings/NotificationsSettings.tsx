import { useEffect, useState } from "react";
import { useSearchParams } from "react-router";
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
import { formatRelativeTime } from "@/lib/date";
import {
  currentWebPushSubscription,
  disableWebPush,
  enableWebPush,
  webPushSupport,
} from "@/lib/webPush";

const REASON_FIELDS = [
  { key: "notify_favorites", label: "Favorites" },
  { key: "notify_watchlist", label: "Watchlist" },
  { key: "notify_continue_watching", label: "Continue Watching" },
  { key: "notify_next_up", label: "Next Up" },
] as const;

// Webhooks additionally carry the request.fulfilled toggle; it is not an
// episode reason, so the profile preferences section keeps REASON_FIELDS.
const WEBHOOK_NOTIFY_FIELDS = [
  ...REASON_FIELDS,
  { key: "notify_requests", label: "Requests" },
] as const;

type WebhookNotifyKey = (typeof WEBHOOK_NOTIFY_FIELDS)[number]["key"];

function PreferencesSection() {
  const { data: prefs, isLoading } = useNotificationPreferences();
  const updatePrefs = useUpdateNotificationPreferences();

  if (isLoading || !prefs) {
    return (
      <SettingsGroup title="New Episode Notifications">
        <Skeleton className="h-24 w-full" />
      </SettingsGroup>
    );
  }

  return (
    <SettingsGroup
      title="New Episode Notifications"
      description="Choose which series relationships notify this profile when a new episode arrives."
    >
      <div className="flex items-center justify-between gap-3">
        <div>
          <div className="text-sm font-medium">Enable notifications</div>
          <div className="text-muted-foreground text-xs">Master switch for this profile</div>
        </div>
        <Switch
          checked={prefs.enabled}
          onCheckedChange={(checked) => updatePrefs.mutate({ enabled: checked })}
        />
      </div>
      {REASON_FIELDS.map((field) => (
        <div key={field.key} className="flex items-center justify-between gap-3">
          <div className="text-sm">{field.label}</div>
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
  const digestText = `One summary per day, around ${digestHour}:00 server time`;
  return (
    <div className="flex items-center justify-between gap-3">
      <div>
        <div className="text-sm">Frequency</div>
        <div className="text-muted-foreground text-xs">
          {mode === "per_episode"
            ? perEpisodeHint
            : mode === "per_episode_and_digest"
              ? `${perEpisodeHint}, plus a daily summary around ${digestHour}:00 server time`
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
          <SelectItem value="daily_digest">Daily digest</SelectItem>
          {(allowPerEpisode || mode === "per_episode") && (
            <SelectItem value="per_episode" disabled={!allowPerEpisode}>
              Every episode
            </SelectItem>
          )}
          {(allowPerEpisode || mode === "per_episode_and_digest") && (
            <SelectItem value="per_episode_and_digest" disabled={!allowPerEpisode}>
              Every episode + daily digest
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
 * Changing it sends a verification link to the new address; the old address
 * keeps receiving mail until the link is clicked. Removing the address also
 * turns the channel off. Child profiles cannot set addresses.
 */
function EmailDestinationRow({ prefs }: { prefs: NotificationEmailPreferences }) {
  const [editing, setEditing] = useState(false);
  const [address, setAddress] = useState("");
  const requestAddress = useRequestEmailNotificationAddress();
  const clearAddress = useClearEmailNotificationAddress();

  const hasAddress = prefs.custom_email !== "";

  const submit = () => {
    const trimmed = address.trim();
    if (!trimmed) {
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
          <div className="text-sm">Deliver to</div>
          <div className="text-muted-foreground text-xs">
            {hasAddress ? prefs.custom_email : "No address set — verify one to receive emails"}
          </div>
        </div>
        {prefs.can_edit_address && (
          <div className="flex items-center gap-2">
            {hasAddress && (
              <Button
                variant="ghost"
                size="sm"
                disabled={clearAddress.isPending}
                onClick={() => clearAddress.mutate()}
              >
                Remove
              </Button>
            )}
            <Button variant="outline" size="sm" onClick={() => setEditing((value) => !value)}>
              {editing ? "Cancel" : hasAddress ? "Change" : "Add address"}
            </Button>
          </div>
        )}
      </div>
      {editing && (
        <div className="flex items-center gap-2">
          <Input
            type="email"
            placeholder="name@example.com"
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
            Send verification
          </Button>
        </div>
      )}
      {prefs.pending_email !== "" && (
        <div className="text-xs text-amber-500">
          Verification email sent to {prefs.pending_email} — it becomes active once the link in it
          is opened.
        </div>
      )}
      {!prefs.can_edit_address && (
        <div className="text-muted-foreground text-xs">
          Child profiles can't receive email notifications.
        </div>
      )}
    </div>
  );
}

function EmailSection() {
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
      <SettingsGroup title="Email Notifications">
        <Skeleton className="h-16 w-full" />
      </SettingsGroup>
    );
  }

  const hasAddress = prefs.custom_email !== "";

  return (
    <SettingsGroup
      title="Email Notifications"
      description="Per profile: each profile verifies its own address and picks its own frequency."
    >
      <div className="flex items-center justify-between gap-3">
        <div>
          <div className="text-sm font-medium">Email this profile's notifications</div>
          <div className="text-muted-foreground text-xs">
            Notifications you'd see in the inbox, delivered by email
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
          perEpisodeHint="An email as soon as each notification arrives"
          onChange={(value) => updatePrefs.mutate({ mode: value })}
        />
      )}
      {enabled && mode !== "daily_digest" && !allowPerEpisode && (
        <div className="text-xs text-amber-500">
          Per-episode email is disabled by the administrator; you'll receive the daily digest
          instead.
        </div>
      )}
    </SettingsGroup>
  );
}

const DISCORD_LINK_ERRORS: Record<string, string> = {
  denied: "Discord linking was cancelled",
  disabled: "Discord notifications were disabled by the administrator",
  invalid_callback: "Discord linking failed: invalid callback",
  state_invalid: "Discord linking expired — try again",
  exchange_failed: "Discord linking failed — try again",
};

function DiscordSection() {
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
      toast.success("Discord account linked");
      void queryClient.invalidateQueries({ queryKey: notificationKeys.discordPreferences() });
    } else if (error) {
      toast.error(DISCORD_LINK_ERRORS[error] ?? "Discord linking failed");
    }
    const next = new URLSearchParams(searchParams);
    next.delete("discord_linked");
    next.delete("discord_error");
    setSearchParams(next, { replace: true });
  }, [searchParams, setSearchParams, queryClient]);

  if (!available) {
    return null;
  }

  if (isLoading) {
    return (
      <SettingsGroup title="Discord Notifications">
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
        window.location.assign(init.url);
      },
    });
  };

  return (
    <SettingsGroup
      title="Discord Notifications"
      description="Account-wide: the Silo bot sends direct messages covering every profile on this account. You must share a Discord server with the bot."
    >
      {!linked ? (
        <div className="flex items-center justify-between gap-3">
          <div>
            <div className="text-sm font-medium">Link your Discord account</div>
            <div className="text-muted-foreground text-xs">
              Authorize on Discord so the bot knows who to message
            </div>
          </div>
          <Button size="sm" disabled={linkInit.isPending} onClick={startLink}>
            {linkInit.isPending && <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />}
            Link Discord
          </Button>
        </div>
      ) : (
        <>
          <div className="flex items-center justify-between gap-3">
            <div>
              <div className="text-sm font-medium">
                Linked as {prefs?.discord_username || "Discord user"}
              </div>
              <div className="text-muted-foreground text-xs">
                Notifications you'd see in the inbox, delivered as a DM
              </div>
            </div>
            <div className="flex items-center gap-2">
              <Switch
                checked={enabled}
                disabled={updatePrefs.isPending}
                onCheckedChange={(checked) =>
                  updatePrefs.mutate({ mode: checked ? "daily_digest" : "off" })
                }
              />
              <Button
                variant="ghost"
                size="sm"
                className="text-destructive"
                disabled={unlink.isPending}
                onClick={() => unlink.mutate()}
              >
                Unlink
              </Button>
            </div>
          </div>
          {enabled && (
            <ChannelFrequencyRow
              mode={mode}
              allowPerEpisode={allowPerEpisode}
              digestHour={digestHour}
              isPending={updatePrefs.isPending}
              perEpisodeHint="A DM as soon as each notification arrives"
              onChange={(value) => updatePrefs.mutate({ mode: value })}
            />
          )}
          {enabled && mode !== "daily_digest" && !allowPerEpisode && (
            <div className="text-xs text-amber-500">
              Per-episode DMs are disabled by the administrator; you'll receive the daily digest
              instead.
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
function webPushHealth(sub: WebPushSubscriptionView): { text: string; failing: boolean } {
  if (!sub.enabled) {
    return { text: "Disabled after repeated delivery failures", failing: true };
  }
  const failing =
    sub.last_failure_at != null &&
    (sub.last_success_at == null || sub.last_failure_at > sub.last_success_at);
  if (failing) {
    const when = formatRelativeTime(sub.last_failure_at);
    return { text: when ? `Last delivery failed ${when}` : "Last delivery failed", failing: true };
  }
  if (sub.last_success_at) {
    const when = formatRelativeTime(sub.last_success_at);
    return { text: when ? `Last delivered ${when}` : "Delivering", failing: false };
  }
  return { text: "No deliveries yet", failing: false };
}

function webPushSubtitle(sub: WebPushSubscriptionView): { text: string; failing: boolean } {
  const health = webPushHealth(sub);
  const added = formatRelativeTime(sub.created_at);
  return {
    text: added ? `Added ${added} · ${health.text}` : health.text,
    failing: health.failing,
  };
}

function WebPushSection() {
  const queryClient = useQueryClient();
  const capability = useNotificationCapability();
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
  const thisHealth = thisSub ? webPushHealth(thisSub) : null;

  const enable = async () => {
    if (!webPushCap?.public_key) {
      toast.error("Web push is not available on this server");
      return;
    }
    setBusy(true);
    try {
      await enableWebPush(webPushCap.public_key);
      const sub = await currentWebPushSubscription();
      setThisEndpoint(sub?.endpoint ?? null);
      toast.success("Browser notifications enabled");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Failed to enable notifications");
    } finally {
      setBusy(false);
      void queryClient.invalidateQueries({ queryKey: notificationKeys.webPushSubscriptions() });
    }
  };

  const disable = async () => {
    setBusy(true);
    try {
      await disableWebPush();
      setThisEndpoint(null);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Failed to disable notifications");
    } finally {
      setBusy(false);
      void queryClient.invalidateQueries({ queryKey: notificationKeys.webPushSubscriptions() });
    }
  };

  if (!available) {
    return null;
  }

  const otherSubscriptions = (subscriptions ?? []).filter((sub) => sub.endpoint !== thisEndpoint);

  return (
    <SettingsGroup
      title="Browser Notifications"
      description="Get notified even when Silo is closed. Notifications are encrypted end-to-end — the browser vendor's push service never sees their content."
    >
      {support === "unsupported" ? (
        <div className="text-muted-foreground text-sm">
          This browser does not support push notifications.
        </div>
      ) : support === "denied" && !subscribedHere ? (
        <div className="text-muted-foreground text-sm">
          Notifications are blocked for this site. Allow them in your browser's site settings, then
          return here.
        </div>
      ) : (
        <div className="flex items-center justify-between gap-3">
          <div className="flex items-center gap-2">
            <BellRing className="text-muted-foreground h-4 w-4" />
            <div>
              <div className="text-sm font-medium">This browser</div>
              <div
                className={
                  thisHealth?.failing ? "text-xs text-amber-500" : "text-muted-foreground text-xs"
                }
              >
                {!subscribedHere
                  ? "Not receiving notifications"
                  : thisHealth?.failing
                    ? thisHealth.text
                    : thisHealth && thisHealth.text !== "No deliveries yet"
                      ? `Receiving notifications · ${thisHealth.text}`
                      : "Receiving notifications"}
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
            {subscribedHere ? "Disable" : "Enable"}
          </Button>
        </div>
      )}

      {otherSubscriptions.length > 0 && (
        <div className="space-y-2">
          <div className="text-muted-foreground text-xs font-medium">
            Other devices ({otherSubscriptions.length})
          </div>
          {otherSubscriptions.map((sub) => {
            const subtitle = webPushSubtitle(sub);
            return (
              <div key={sub.id} className="flex items-center justify-between gap-3">
                <div className="flex min-w-0 items-center gap-2">
                  <MonitorSmartphone className="text-muted-foreground h-4 w-4 shrink-0" />
                  <div className="min-w-0">
                    <div className="truncate text-sm">{sub.device_name || "Unknown device"}</div>
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
                  Remove
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
        { id: webhook.id, ...input },
        {
          onSuccess: () => onOpenChange(false),
        },
      );
      return;
    }
    if (!input.url) {
      toast.error("A webhook URL is required");
      return;
    }
    create.mutate(input, {
      onSuccess: (created) => {
        onOpenChange(false);
        toast.success(`Webhook "${created.name}" created`);
        if (created.signing_secret) {
          onSecret(created.signing_secret);
        }
      },
      onError: (error) => {
        toast.error(error instanceof Error ? error.message : "Failed to create webhook");
      },
    });
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{editing ? `Edit "${webhook.name}"` : "Add webhook"}</DialogTitle>
          <DialogDescription>
            Discord webhook URLs render as native embeds. Any other HTTPS endpoint receives signed
            JSON.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="webhook-name">Name</Label>
            <Input
              id="webhook-name"
              value={name}
              maxLength={64}
              placeholder="Family Discord"
              onChange={(event) => setName(event.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="webhook-url">{editing ? "Replace URL (optional)" : "URL"}</Label>
            <Input
              id="webhook-url"
              value={url}
              placeholder={
                editing
                  ? `Currently pointing at ${webhook.url_host}`
                  : "https://discord.com/api/webhooks/…"
              }
              onChange={(event) => setUrl(event.target.value)}
            />
          </div>
          <div className="space-y-2">
            <Label>Send notifications for</Label>
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
                    {field.label}
                    {globallyDisabled && (
                      <span className="text-muted-foreground ml-2 text-xs">
                        disabled in profile preferences
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
            Cancel
          </Button>
          <Button onClick={submit} disabled={pending || !name.trim()}>
            {pending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
            {editing ? "Save" : "Create"}
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
  const update = useUpdateNotificationWebhook();
  const remove = useDeleteNotificationWebhook();
  const test = useTestNotificationWebhook();
  const rotate = useRotateNotificationWebhookSecret();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [testResult, setTestResult] = useState<NotificationWebhookTestResult | null>(null);

  const lastSuccess = formatRelativeTime(webhook.last_success_at);
  const lastFailure = formatRelativeTime(webhook.last_failure_at);
  const failing =
    webhook.last_failure_at != null &&
    (webhook.last_success_at == null || webhook.last_failure_at > webhook.last_success_at);
  const enabledReasons = WEBHOOK_NOTIFY_FIELDS.filter(
    (field) => webhook[field.key as keyof NotificationWebhook] as boolean,
  ).map((field) => field.label);

  return (
    <div className="border-border/60 space-y-2 rounded-xl border p-4">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-medium">{webhook.name}</span>
        <Badge variant="secondary">{webhook.type}</Badge>
        <span className="text-muted-foreground text-xs">{webhook.url_host}</span>
        <div className="ml-auto flex items-center gap-1.5">
          <span className="text-muted-foreground text-xs">
            {webhook.enabled ? "Enabled" : "Disabled"}
          </span>
          <Switch
            checked={webhook.enabled}
            onCheckedChange={(checked) => update.mutate({ id: webhook.id, enabled: checked })}
          />
        </div>
      </div>

      <div className="text-muted-foreground text-xs">
        {enabledReasons.length === WEBHOOK_NOTIFY_FIELDS.length
          ? "All reasons"
          : enabledReasons.length > 0
            ? enabledReasons.join(" · ")
            : "No reasons selected"}
      </div>

      {lastSuccess && !failing && (
        <div className="text-muted-foreground text-xs">Last success: {lastSuccess}</div>
      )}
      {failing && (
        <div className="flex items-start gap-1.5 text-xs text-amber-500">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          <span>
            {webhook.disabled_reason
              ? `Disabled: ${webhook.disabled_reason}`
              : `Last failure${lastFailure ? ` ${lastFailure}` : ""}: ${
                  webhook.last_failure_message || `HTTP ${webhook.last_failure_status ?? "error"}`
                }. Check the destination URL.`}
          </span>
        </div>
      )}
      {testResult && (
        <div className={`text-xs ${testResult.ok ? "text-emerald-500" : "text-amber-500"}`}>
          Test {testResult.ok ? "succeeded" : "failed"}
          {testResult.http_status ? ` (HTTP ${testResult.http_status}` : " ("}
          {`${testResult.duration_ms}ms)`}
          {testResult.message ? ` — ${testResult.message}` : ""}
        </div>
      )}

      <div className="flex flex-wrap gap-1.5 pt-1">
        <Button
          variant="outline"
          size="sm"
          disabled={test.isPending}
          onClick={() =>
            test.mutate(webhook.id, {
              onSuccess: setTestResult,
              onError: () => toast.error("Test request failed"),
            })
          }
        >
          {test.isPending ? (
            <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
          ) : (
            <Send className="mr-1.5 h-3.5 w-3.5" />
          )}
          Test
        </Button>
        <Button variant="outline" size="sm" onClick={onEdit}>
          <Pencil className="mr-1.5 h-3.5 w-3.5" />
          Edit
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
            Rotate secret
          </Button>
        )}
        <Button
          variant="outline"
          size="sm"
          className="text-destructive"
          onClick={() => setConfirmDelete(true)}
        >
          <Trash2 className="mr-1.5 h-3.5 w-3.5" />
          Delete
        </Button>
      </div>

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Delete "${webhook.name}"?`}
        description="Notifications will stop posting to this destination. This cannot be undone."
        confirmLabel="Delete"
        variant="destructive"
        isPending={remove.isPending}
        onConfirm={() => remove.mutate(webhook.id, { onSettled: () => setConfirmDelete(false) })}
      />
      {/* The edit dialog is hosted by the parent so state resets per webhook. */}
      {update.isPending && <span className="sr-only">Saving…</span>}
    </div>
  );
}

function WebhooksSection() {
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
        title="Webhooks"
        description="Send this profile's notifications to a webhook URL. Discord URLs render as native embeds; other URLs receive signed JSON."
      >
        {isLoading ? (
          <Skeleton className="h-24 w-full" />
        ) : (
          <>
            {(webhooks ?? []).map((webhook) => (
              <WebhookCard
                key={webhook.id}
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
                No webhooks yet.
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
                Add webhook
              </Button>
              {atLimit && (
                <span className="text-muted-foreground ml-2 text-xs">
                  Limit of {maxPerProfile} webhooks reached
                </span>
              )}
            </div>
          </>
        )}
      </SettingsGroup>

      {formOpen && (
        <WebhookFormDialog
          key={editing?.id ?? "new"}
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
  useDocumentTitle("Notification Settings");

  return (
    <div className="space-y-6">
      <PreferencesSection />

      <WebPushSection />

      <EmailSection />

      <DiscordSection />

      <WebhooksSection />
    </div>
  );
}
