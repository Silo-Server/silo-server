import { captureProfileRequestContext, isCapturedProfileAuthorityActive } from "@/api/client";
import {
  notificationScope,
  requireNotificationAuthority,
  captureNotificationAuthority,
} from "@/api/v2/notifications";
import { useState } from "react";
import {
  AlertTriangle,
  KeyRound,
  Loader2,
  Megaphone,
  Pencil,
  Plus,
  Send,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import type {
  NotificationWebhookTestResult,
  ServerNotificationChannel,
  ServerNotificationChannelInput,
} from "@/api/types";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { SigningSecretDialog } from "@/components/SigningSecretDialog";
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
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import {
  useCreateServerNotificationChannel,
  useDeleteServerNotificationChannel,
  useRotateServerNotificationChannelSecret,
  useServerNotificationChannels,
  useTestServerNotificationChannel,
  useUpdateServerNotificationChannel,
} from "@/hooks/queries/admin/serverNotificationChannels";
import { useT, type MessageKey } from "@/i18n";
import { formatRelativeTime } from "@/lib/date";

type ChannelNotifyKey =
  | "notify_new_movies"
  | "notify_new_episodes"
  | "notify_new_audiobooks"
  | "notify_new_ebooks"
  | "notify_request_submitted"
  | "notify_request_approved"
  | "notify_request_declined"
  | "notify_request_fulfilled";

interface ChannelNotifyField {
  key: ChannelNotifyKey;
  label: MessageKey;
  defaultValue: boolean;
}

const EVENT_SECTIONS: { label: MessageKey; fields: ChannelNotifyField[] }[] = [
  {
    label: "web.admin.settings.notifications.serverChannels.events.newContent",
    fields: [
      {
        key: "notify_new_movies",
        label: "web.admin.settings.notifications.serverChannels.events.newMovies",
        defaultValue: true,
      },
      {
        key: "notify_new_episodes",
        label: "web.admin.settings.notifications.serverChannels.events.newEpisodes",
        defaultValue: true,
      },
      {
        key: "notify_new_audiobooks",
        label: "web.admin.settings.notifications.serverChannels.events.newAudiobooks",
        defaultValue: true,
      },
      {
        key: "notify_new_ebooks",
        label: "web.admin.settings.notifications.serverChannels.events.newEbooks",
        defaultValue: true,
      },
    ],
  },
  {
    label: "web.admin.settings.notifications.serverChannels.events.mediaRequests",
    fields: [
      {
        key: "notify_request_submitted",
        label: "web.admin.settings.notifications.serverChannels.events.requestSubmitted",
        defaultValue: false,
      },
      {
        key: "notify_request_approved",
        label: "web.admin.settings.notifications.serverChannels.events.requestApproved",
        defaultValue: false,
      },
      {
        key: "notify_request_declined",
        label: "web.admin.settings.notifications.serverChannels.events.requestDeclined",
        defaultValue: false,
      },
      {
        key: "notify_request_fulfilled",
        label: "web.admin.settings.notifications.serverChannels.events.requestFulfilled",
        defaultValue: false,
      },
    ],
  },
];

/** Example destination URL for a new channel; it reads the same in every language. */
const DISCORD_WEBHOOK_PLACEHOLDER = "https://discord.com/api/webhooks/…";

const CHANNEL_NOTIFY_FIELDS = EVENT_SECTIONS.flatMap((section) => section.fields);

function ChannelFormDialog({
  open,
  onOpenChange,
  channel,
  onSecret,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  channel: ServerNotificationChannel | null;
  onSecret: (secret: string) => void;
}) {
  const t = useT();
  const create = useCreateServerNotificationChannel();
  const update = useUpdateServerNotificationChannel();
  const [name, setName] = useState(channel?.name ?? "");
  const [url, setUrl] = useState("");
  const [events, setEvents] = useState<Record<ChannelNotifyKey, boolean>>(
    () =>
      Object.fromEntries(
        CHANNEL_NOTIFY_FIELDS.map((field) => [
          field.key,
          channel?.[field.key] ?? field.defaultValue,
        ]),
      ) as Record<ChannelNotifyKey, boolean>,
  );
  const pending = create.isPending || update.isPending;
  const editing = channel != null;

  const submit = () => {
    const input: ServerNotificationChannelInput = { name: name.trim(), ...events };
    if (url.trim()) {
      input.url = url.trim();
    }
    if (editing) {
      update.mutate(
        { id: channel.id, ...input },
        {
          onSuccess: () => onOpenChange(false),
        },
      );
      return;
    }
    if (!input.url) {
      toast.error(t("web.admin.settings.notifications.serverChannels.urlRequired"));
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
        toast.success(
          t("web.admin.settings.notifications.serverChannels.created", { name: created.name }),
        );
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
            : t("web.admin.settings.notifications.serverChannels.createFailed"),
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
              ? t("web.admin.settings.notifications.serverChannels.editTitle", {
                  name: channel.name,
                })
              : t("web.admin.settings.notifications.serverChannels.addTitle")}
          </DialogTitle>
          <DialogDescription>
            {t("web.admin.settings.notifications.serverChannels.dialogDescription")}
          </DialogDescription>
        </DialogHeader>
        <fieldset disabled={pending} className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="server-channel-name">
              {t("web.admin.settings.notifications.serverChannels.name")}
            </Label>
            <Input
              id="server-channel-name"
              value={name}
              maxLength={64}
              placeholder={t("web.admin.settings.notifications.serverChannels.namePlaceholder")}
              onChange={(event) => setName(event.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="server-channel-url">
              {editing
                ? t("web.admin.settings.notifications.serverChannels.replaceUrl")
                : t("web.admin.settings.notifications.serverChannels.url")}
            </Label>
            <Input
              id="server-channel-url"
              value={url}
              placeholder={
                editing
                  ? t("web.admin.settings.notifications.serverChannels.currentHost", {
                      host: channel.url_host,
                    })
                  : DISCORD_WEBHOOK_PLACEHOLDER
              }
              onChange={(event) => setUrl(event.target.value)}
            />
          </div>
          {EVENT_SECTIONS.map((section) => (
            <div key={section.label} className="space-y-2">
              <Label>{t(section.label)}</Label>
              {section.fields.map((field) => (
                <div key={field.key} className="flex items-center justify-between gap-3">
                  <div className="text-sm">{t(field.label)}</div>
                  <Switch
                    checked={events[field.key]}
                    onCheckedChange={(checked) =>
                      setEvents((current) => ({ ...current, [field.key]: checked }))
                    }
                  />
                </div>
              ))}
            </div>
          ))}
        </fieldset>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button onClick={submit} disabled={pending || !name.trim()}>
            {pending && <Loader2 className="me-2 h-4 w-4 animate-spin" />}
            {editing
              ? t("common.save")
              : t("web.admin.settings.notifications.serverChannels.create")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/**
 * Values for a test post's result line. The status runs straight into the
 * duration ("HTTP 200123ms"), as the line always has.
 */
function testResultValues(result: NotificationWebhookTestResult) {
  return {
    outcome: result.ok ? "ok" : "failed",
    status: result.http_status ? String(result.http_status) : "none",
    duration_ms: String(result.duration_ms),
  };
}

function ChannelCard({
  channel,
  onEdit,
  onSecret,
}: {
  channel: ServerNotificationChannel;
  onEdit: () => void;
  onSecret: (secret: string) => void;
}) {
  const t = useT();
  const update = useUpdateServerNotificationChannel();
  const remove = useDeleteServerNotificationChannel();
  const authority = captureProfileRequestContext();
  const test = useTestServerNotificationChannel();
  const rotate = useRotateServerNotificationChannelSecret();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [testResult, setTestResult] = useState<NotificationWebhookTestResult | null>(null);

  const lastSuccess = formatRelativeTime(channel.last_success_at);
  const lastFailure = formatRelativeTime(channel.last_failure_at);
  const failing =
    channel.last_failure_at != null &&
    (channel.last_success_at == null || channel.last_failure_at > channel.last_success_at);
  const enabledEvents = CHANNEL_NOTIFY_FIELDS.filter((field) => channel[field.key]).map((field) =>
    t(field.label),
  );
  const failureDetail =
    channel.last_failure_message ||
    (channel.last_failure_status != null
      ? t("web.admin.settings.notifications.serverChannels.httpStatus", {
          status: String(channel.last_failure_status),
        })
      : t("web.admin.settings.notifications.serverChannels.httpError"));

  return (
    <div className="border-border/60 space-y-2 rounded-xl border p-4">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-medium">{channel.name}</span>
        <Badge variant="secondary">{channel.type}</Badge>
        <span className="text-muted-foreground text-xs">{channel.url_host}</span>
        <div className="ms-auto flex items-center gap-1.5">
          <span className="text-muted-foreground text-xs">
            {channel.enabled
              ? t("web.admin.settings.notifications.serverChannels.enabled")
              : t("web.admin.settings.notifications.serverChannels.disabled")}
          </span>
          <Switch
            checked={channel.enabled}
            onCheckedChange={(checked) => update.mutate({ id: channel.id, enabled: checked })}
          />
        </div>
      </div>

      <div className="text-muted-foreground text-xs">
        {enabledEvents.length > 0
          ? enabledEvents.join(" · ")
          : t("web.admin.settings.notifications.serverChannels.noEvents")}
      </div>

      {lastSuccess && !failing && (
        <div className="text-muted-foreground text-xs">
          {t("web.admin.settings.notifications.serverChannels.lastPost", { when: lastSuccess })}
        </div>
      )}
      {failing && (
        <div className="flex items-start gap-1.5 text-xs text-amber-500">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          <span>
            {channel.disabled_reason
              ? t("web.admin.settings.notifications.serverChannels.disabledReason", {
                  reason: channel.disabled_reason,
                })
              : lastFailure
                ? t("web.admin.settings.notifications.serverChannels.lastFailureWhen", {
                    when: lastFailure,
                    detail: failureDetail,
                  })
                : t("web.admin.settings.notifications.serverChannels.lastFailure", {
                    detail: failureDetail,
                  })}
          </span>
        </div>
      )}
      {testResult && (
        <div className={`text-xs ${testResult.ok ? "text-emerald-500" : "text-amber-500"}`}>
          {testResult.message
            ? t("web.admin.settings.notifications.serverChannels.testResultWithMessage", {
                ...testResultValues(testResult),
                message: testResult.message,
              })
            : t(
                "web.admin.settings.notifications.serverChannels.testResult",
                testResultValues(testResult),
              )}
        </div>
      )}

      <div className="flex flex-wrap gap-1.5 pt-1">
        <Button
          variant="outline"
          size="sm"
          disabled={test.isPending}
          onClick={() =>
            test.mutate(channel.id, {
              onSuccess: (result) => {
                if (authority && isCapturedProfileAuthorityActive(authority)) setTestResult(result);
              },
              onError: () => {
                if (authority && isCapturedProfileAuthorityActive(authority))
                  toast.error(t("web.admin.settings.notifications.serverChannels.testFailed"));
              },
            })
          }
        >
          {test.isPending ? (
            <Loader2 className="me-1.5 h-3.5 w-3.5 animate-spin" />
          ) : (
            <Send className="me-1.5 h-3.5 w-3.5" />
          )}
          {t("web.admin.settings.notifications.serverChannels.test")}
        </Button>
        <Button variant="outline" size="sm" onClick={onEdit}>
          <Pencil className="me-1.5 h-3.5 w-3.5" />
          {t("web.admin.settings.notifications.serverChannels.edit")}
        </Button>
        {channel.type === "generic" && (
          <Button
            variant="outline"
            size="sm"
            disabled={rotate.isPending}
            onClick={() =>
              rotate.mutate(channel.id, {
                onSuccess: (result) => onSecret(result.signing_secret),
              })
            }
          >
            <KeyRound className="me-1.5 h-3.5 w-3.5" />
            {t("web.admin.settings.notifications.serverChannels.rotateSecret")}
          </Button>
        )}
        <Button
          variant="outline"
          size="sm"
          className="text-destructive"
          onClick={() => setConfirmDelete(true)}
        >
          <Trash2 className="me-1.5 h-3.5 w-3.5" />
          {t("web.admin.settings.notifications.serverChannels.delete")}
        </Button>
      </div>

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={t("web.admin.settings.notifications.serverChannels.deleteTitle", {
          name: channel.name,
        })}
        description={t("web.admin.settings.notifications.serverChannels.deleteDescription")}
        confirmLabel={t("web.admin.settings.notifications.serverChannels.deleteConfirm")}
        variant="destructive"
        isPending={remove.isPending}
        onConfirm={() => remove.mutate(channel.id, { onSettled: () => setConfirmDelete(false) })}
      />
      {update.isPending && (
        <span className="sr-only">
          {t("web.admin.settings.notifications.serverChannels.saving")}
        </span>
      )}
    </div>
  );
}

/**
 * Admin CRUD for server notification channels: broadcast webhook destinations
 * that announce newly added content and media request lifecycle events to a
 * shared audience (e.g. a community Discord channel).
 */
export default function ServerNotificationChannels() {
  const t = useT();
  const { data: channels, isLoading } = useServerNotificationChannels();
  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<ServerNotificationChannel | null>(null);
  const [secret, setSecret] = useState<string | null>(null);

  return (
    <div className="space-y-3">
      {isLoading ? (
        <Skeleton className="h-24 w-full" />
      ) : (
        <>
          {(channels ?? []).map((channel) => (
            <ChannelCard
              key={`${notificationScope()}:${channel.id}`}
              channel={channel}
              onSecret={setSecret}
              onEdit={() => {
                setEditing(channel);
                setFormOpen(true);
              }}
            />
          ))}
          {(channels ?? []).length === 0 && (
            <div className="text-muted-foreground flex items-center gap-2 py-1 text-sm">
              <Megaphone className="h-4 w-4" />
              {t("web.admin.settings.notifications.serverChannels.empty")}
            </div>
          )}
          <div>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setEditing(null);
                setFormOpen(true);
              }}
            >
              <Plus className="me-1.5 h-4 w-4" />
              {t("web.admin.settings.notifications.serverChannels.add")}
            </Button>
          </div>
        </>
      )}

      {formOpen && (
        <ChannelFormDialog
          key={`${notificationScope()}:${editing?.id ?? "new"}`}
          open={formOpen}
          onOpenChange={(open) => {
            setFormOpen(open);
            if (!open) {
              setEditing(null);
            }
          }}
          channel={editing}
          onSecret={setSecret}
        />
      )}
      <SigningSecretDialog secret={secret} onClose={() => setSecret(null)} />
    </div>
  );
}
