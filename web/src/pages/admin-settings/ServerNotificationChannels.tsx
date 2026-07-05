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
  label: string;
  defaultValue: boolean;
}

const EVENT_SECTIONS: { label: string; fields: ChannelNotifyField[] }[] = [
  {
    label: "New content",
    fields: [
      { key: "notify_new_movies", label: "New movies", defaultValue: true },
      { key: "notify_new_episodes", label: "New episodes", defaultValue: true },
      { key: "notify_new_audiobooks", label: "New audiobooks", defaultValue: true },
      { key: "notify_new_ebooks", label: "New ebooks", defaultValue: true },
    ],
  },
  {
    label: "Media requests",
    fields: [
      { key: "notify_request_submitted", label: "Request submitted", defaultValue: false },
      { key: "notify_request_approved", label: "Request approved", defaultValue: false },
      { key: "notify_request_declined", label: "Request declined", defaultValue: false },
      { key: "notify_request_fulfilled", label: "Request fulfilled", defaultValue: false },
    ],
  },
];

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
      toast.error("A webhook URL is required");
      return;
    }
    create.mutate(input, {
      onSuccess: (created) => {
        onOpenChange(false);
        toast.success(`Channel "${created.name}" created`);
        if (created.signing_secret) {
          onSecret(created.signing_secret);
        }
      },
      onError: (error) => {
        toast.error(error instanceof Error ? error.message : "Failed to create channel");
      },
    });
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{editing ? `Edit "${channel.name}"` : "Add server channel"}</DialogTitle>
          <DialogDescription>
            Server channels broadcast server-wide events — every profile sees the same posts.
            Discord webhook URLs render as native embeds; any other HTTPS endpoint receives signed
            JSON.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="server-channel-name">Name</Label>
            <Input
              id="server-channel-name"
              value={name}
              maxLength={64}
              placeholder="Community #new-content"
              onChange={(event) => setName(event.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="server-channel-url">{editing ? "Replace URL (optional)" : "URL"}</Label>
            <Input
              id="server-channel-url"
              value={url}
              placeholder={
                editing
                  ? `Currently pointing at ${channel.url_host}`
                  : "https://discord.com/api/webhooks/…"
              }
              onChange={(event) => setUrl(event.target.value)}
            />
          </div>
          {EVENT_SECTIONS.map((section) => (
            <div key={section.label} className="space-y-2">
              <Label>{section.label}</Label>
              {section.fields.map((field) => (
                <div key={field.key} className="flex items-center justify-between gap-3">
                  <div className="text-sm">{field.label}</div>
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

function ChannelCard({
  channel,
  onEdit,
  onSecret,
}: {
  channel: ServerNotificationChannel;
  onEdit: () => void;
  onSecret: (secret: string) => void;
}) {
  const update = useUpdateServerNotificationChannel();
  const remove = useDeleteServerNotificationChannel();
  const test = useTestServerNotificationChannel();
  const rotate = useRotateServerNotificationChannelSecret();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [testResult, setTestResult] = useState<NotificationWebhookTestResult | null>(null);

  const lastSuccess = formatRelativeTime(channel.last_success_at);
  const lastFailure = formatRelativeTime(channel.last_failure_at);
  const failing =
    channel.last_failure_at != null &&
    (channel.last_success_at == null || channel.last_failure_at > channel.last_success_at);
  const enabledEvents = CHANNEL_NOTIFY_FIELDS.filter((field) => channel[field.key]).map(
    (field) => field.label,
  );

  return (
    <div className="border-border/60 space-y-2 rounded-xl border p-4">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-medium">{channel.name}</span>
        <Badge variant="secondary">{channel.type}</Badge>
        <span className="text-muted-foreground text-xs">{channel.url_host}</span>
        <div className="ml-auto flex items-center gap-1.5">
          <span className="text-muted-foreground text-xs">
            {channel.enabled ? "Enabled" : "Disabled"}
          </span>
          <Switch
            checked={channel.enabled}
            onCheckedChange={(checked) => update.mutate({ id: channel.id, enabled: checked })}
          />
        </div>
      </div>

      <div className="text-muted-foreground text-xs">
        {enabledEvents.length > 0 ? enabledEvents.join(" · ") : "No events selected"}
      </div>

      {lastSuccess && !failing && (
        <div className="text-muted-foreground text-xs">Last post: {lastSuccess}</div>
      )}
      {failing && (
        <div className="flex items-start gap-1.5 text-xs text-amber-500">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          <span>
            {channel.disabled_reason
              ? `Disabled: ${channel.disabled_reason} Re-enable the channel to resume from now.`
              : `Last failure${lastFailure ? ` ${lastFailure}` : ""}: ${
                  channel.last_failure_message || `HTTP ${channel.last_failure_status ?? "error"}`
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
            test.mutate(channel.id, {
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
        title={`Delete "${channel.name}"?`}
        description="Server events will stop posting to this destination. This cannot be undone."
        confirmLabel="Delete"
        variant="destructive"
        isPending={remove.isPending}
        onConfirm={() => remove.mutate(channel.id, { onSettled: () => setConfirmDelete(false) })}
      />
      {update.isPending && <span className="sr-only">Saving…</span>}
    </div>
  );
}

/**
 * Admin CRUD for server notification channels: broadcast webhook destinations
 * that announce newly added content and media request lifecycle events to a
 * shared audience (e.g. a community Discord channel).
 */
export default function ServerNotificationChannels() {
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
              key={channel.id}
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
              No server channels yet. Create one to broadcast new content and request activity.
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
              <Plus className="mr-1.5 h-4 w-4" />
              Add server channel
            </Button>
          </div>
        </>
      )}

      {formOpen && (
        <ChannelFormDialog
          key={editing?.id ?? "new"}
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
