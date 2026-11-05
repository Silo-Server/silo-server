import { useState } from "react";
import { AlertTriangle, BellPlus, Loader2, Pencil, Plus, Send, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { notificationScope } from "@/api/v2/notifications";
import type { NotificationPreferences } from "@/api/types";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { SchemaForm } from "@/components/admin/plugins/SchemaForm";
import {
  buildSchemaValues,
  parseFieldTypes,
  type BuildSchemaValuesOptions,
} from "@/components/admin/plugins/schemaFormUtils";
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
import { useNotificationPreferences } from "@/hooks/queries/notifications";
import {
  pluginDestinationErrorMessage,
  useCreateNotificationPluginDestination,
  useDeleteNotificationPluginDestination,
  useNotificationPluginChannels,
  useNotificationPluginDestinations,
  useTestNotificationPluginDestination,
  useUpdateNotificationPluginDestination,
  type NotificationPluginChannel,
  type NotificationPluginDestination,
} from "@/hooks/queries/notificationPluginDestinations";
import { useNotificationCapability } from "@/hooks/queries/notificationWebhooks";
import { formatRelativeTime } from "@/lib/date";
import { DESTINATION_NOTIFY_FIELDS, type DestinationNotifyKey } from "./notificationReasonFields";

const channelKey = (c: { plugin_id: string; capability_id: string }) =>
  `${c.plugin_id}/${c.capability_id}`;

function ReasonToggles({
  reasons,
  onChange,
  globalPrefs,
}: {
  reasons: Record<DestinationNotifyKey, boolean>;
  onChange: (next: Record<DestinationNotifyKey, boolean>) => void;
  globalPrefs: NotificationPreferences | undefined;
}) {
  return (
    <div className="space-y-2">
      <Label>Send notifications for</Label>
      {DESTINATION_NOTIFY_FIELDS.map((field) => {
        // Requests have no per-profile reason toggle; only the master switch
        // suppresses them.
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
              onCheckedChange={(checked) => onChange({ ...reasons, [field.key]: checked })}
            />
          </div>
        );
      })}
    </div>
  );
}

function DestinationFormDialog({
  open,
  onOpenChange,
  channels,
  destination,
  globalPrefs,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  channels: NotificationPluginChannel[];
  destination: NotificationPluginDestination | null;
  globalPrefs: NotificationPreferences | undefined;
}) {
  const create = useCreateNotificationPluginDestination();
  const update = useUpdateNotificationPluginDestination();
  const editing = destination != null;
  const [kind, setKind] = useState(
    destination ? channelKey(destination) : channels[0] ? channelKey(channels[0]) : "",
  );
  const channel = channels.find((c) => channelKey(c) === kind);
  const [name, setName] = useState(destination?.name ?? channel?.display_name ?? "");
  const [settings, setSettings] = useState<Record<string, unknown>>(destination?.settings ?? {});
  const [settingsValid, setSettingsValid] = useState(true);
  const [reasons, setReasons] = useState<Record<DestinationNotifyKey, boolean>>({
    notify_favorites: destination?.notify_favorites ?? true,
    notify_watchlist: destination?.notify_watchlist ?? true,
    notify_continue_watching: destination?.notify_continue_watching ?? true,
    notify_next_up: destination?.notify_next_up ?? true,
    notify_requests: destination?.notify_requests ?? true,
  });
  const pending = create.isPending || update.isPending;
  // The server names every secret with a stored value here, however the
  // schema marks it. Left blank, such a secret stays as saved, so it isn't a
  // missing required field.
  const secretsSet = destination?.secret_settings_set ?? [];
  const declared = channel?.settings_form.admin_form;
  const form = declared && {
    ...declared,
    fields: declared.fields.map((field) =>
      secretsSet.includes(field.key) ? { ...field, required: false } : field,
    ),
  };

  // The form keeps number inputs as text and shows defaults it never
  // stores, so settings go out typed as the schema declares them.
  const settingsToSend = (options?: BuildSchemaValuesOptions) =>
    form
      ? buildSchemaValues(
          form,
          settings,
          parseFieldTypes(channel?.settings_form.json_schema),
          options,
        )
      : settings;

  // An update merges over the stored settings. A stored secret the user
  // didn't type is left out, so neither a blank nor the field's form default
  // replaces it. Any other emptied field goes out as null, which removes it
  // so the plugin falls back to its default; a "" would be checked against
  // the field's constraints instead.
  const updatedSettings = () =>
    Object.fromEntries(
      Object.entries(settingsToSend({ explicitClears: true }))
        .filter(([key]) => !secretsSet.includes(key) || (settings[key] ?? "") !== "")
        .map(([key, value]) => [key, value === "" ? null : value]),
    );

  const submit = () => {
    const onError = (error: unknown) =>
      toast.error(pluginDestinationErrorMessage(error, "The destination couldn't be saved"));
    if (editing) {
      update.mutate(
        {
          id: destination.id,
          etag: destination.etag,
          name: name.trim(),
          settings: updatedSettings(),
          ...reasons,
        },
        { onSuccess: () => onOpenChange(false), onError },
      );
      return;
    }
    if (!channel) return;
    create.mutate(
      {
        plugin_id: channel.plugin_id,
        capability_id: channel.capability_id,
        name: name.trim(),
        settings: settingsToSend(),
        ...reasons,
      },
      {
        onSuccess: (created) => {
          onOpenChange(false);
          toast.success(`"${created.name}" added`);
        },
        onError,
      },
    );
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {editing ? `Edit "${destination.name}"` : "Add a notification destination"}
          </DialogTitle>
          <DialogDescription>
            {channel?.description || "Choose where this profile's notifications go."}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          {!editing && channels.length > 1 && (
            <div className="space-y-1.5">
              <Label htmlFor="plugin-destination-kind">Kind</Label>
              <Select
                value={kind}
                onValueChange={(next) => {
                  setKind(next);
                  setSettings({});
                  const picked = channels.find((c) => channelKey(c) === next);
                  if (picked && !name.trim()) setName(picked.display_name);
                }}
              >
                <SelectTrigger id="plugin-destination-kind">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {channels.map((c) => (
                    <SelectItem key={channelKey(c)} value={channelKey(c)}>
                      {c.display_name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}
          <div className="space-y-1.5">
            <Label htmlFor="plugin-destination-name">Name</Label>
            <Input
              id="plugin-destination-name"
              value={name}
              maxLength={64}
              onChange={(event) => setName(event.target.value)}
            />
          </div>
          {form && (
            <div className="space-y-1.5">
              <SchemaForm
                descriptor={form}
                values={settings}
                onChange={setSettings}
                idPrefix={`plugin-destination-${kind}`}
                onValidityChange={setSettingsValid}
              />
              {secretsSet.length > 0 && (
                <p className="text-muted-foreground text-xs">
                  Secret fields you leave blank keep their saved values.
                </p>
              )}
            </div>
          )}
          <ReasonToggles reasons={reasons} onChange={setReasons} globalPrefs={globalPrefs} />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button onClick={submit} disabled={pending || !name.trim() || !channel || !settingsValid}>
            {pending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
            {editing ? "Save" : "Add"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function DestinationCard({
  destination,
  onEdit,
}: {
  destination: NotificationPluginDestination;
  onEdit: () => void;
}) {
  const update = useUpdateNotificationPluginDestination();
  const remove = useDeleteNotificationPluginDestination();
  const test = useTestNotificationPluginDestination();
  const [confirmDelete, setConfirmDelete] = useState(false);
  // The validator the user saw when they chose to delete; a refetch while
  // the dialog is open must not swap in a newer one.
  const [deleteIntent, setDeleteIntent] = useState<{ id: string; etag: string } | null>(null);
  const [testResult, setTestResult] = useState<{ ok: boolean; message?: string } | null>(null);

  const lastSuccess = formatRelativeTime(destination.last_success_at);
  const lastFailure = formatRelativeTime(destination.last_failure_at);
  const failing =
    destination.last_failure_at != null &&
    (destination.last_success_at == null ||
      destination.last_failure_at > destination.last_success_at);
  const enabledReasons = DESTINATION_NOTIFY_FIELDS.filter(
    (field) => destination[field.key as keyof NotificationPluginDestination] as boolean,
  ).map((field) => field.label);

  return (
    <div className="border-border/60 space-y-2 rounded-xl border p-4">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-medium">{destination.name}</span>
        <Badge variant="secondary">
          {destination.available ? destination.channel_name : "Unavailable"}
        </Badge>
        <div className="ml-auto flex items-center gap-1.5">
          <span className="text-muted-foreground text-xs">
            {destination.enabled ? "Enabled" : "Disabled"}
          </span>
          <Switch
            checked={destination.enabled}
            onCheckedChange={(checked) =>
              update.mutate(
                { id: destination.id, etag: destination.etag, enabled: checked },
                {
                  onError: (error) =>
                    toast.error(pluginDestinationErrorMessage(error, "Failed to update")),
                },
              )
            }
          />
        </div>
      </div>

      {!destination.available && (
        <div className="text-muted-foreground text-xs">
          The plugin that provides this destination is turned off or removed, so nothing is sent.
        </div>
      )}
      <div className="text-muted-foreground text-xs">
        {enabledReasons.length === DESTINATION_NOTIFY_FIELDS.length
          ? "All reasons"
          : enabledReasons.length > 0
            ? enabledReasons.join(" · ")
            : "No reasons selected"}
      </div>
      {lastSuccess && !failing && (
        <div className="text-muted-foreground text-xs">Last success: {lastSuccess}</div>
      )}
      {(failing || destination.disabled_reason) && (
        <div className="flex items-start gap-1.5 text-xs text-amber-500">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          <span>
            {destination.disabled_reason
              ? `Disabled: ${destination.disabled_reason}`
              : `Last failure${lastFailure ? ` ${lastFailure}` : ""}: ${
                  destination.last_failure_message || "the plugin couldn't send"
                }`}
          </span>
        </div>
      )}
      {testResult && (
        <div className={`text-xs ${testResult.ok ? "text-emerald-500" : "text-amber-500"}`}>
          Test {testResult.ok ? "sent" : "failed"}
          {testResult.message ? `: ${testResult.message}` : ""}
        </div>
      )}

      <div className="flex flex-wrap gap-1.5 pt-1">
        <Button
          variant="outline"
          size="sm"
          disabled={test.isPending || !destination.available}
          onClick={() =>
            test.mutate(destination.id, {
              onSuccess: (result) => setTestResult(result),
              onError: (error) =>
                toast.error(pluginDestinationErrorMessage(error, "Test request failed")),
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
        <Button variant="outline" size="sm" onClick={onEdit} disabled={!destination.available}>
          <Pencil className="mr-1.5 h-3.5 w-3.5" />
          Edit
        </Button>
        <Button
          variant="outline"
          size="sm"
          className="text-destructive"
          onClick={() => {
            setDeleteIntent({ id: destination.id, etag: destination.etag });
            setConfirmDelete(true);
          }}
        >
          <Trash2 className="mr-1.5 h-3.5 w-3.5" />
          Delete
        </Button>
      </div>

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Delete "${destination.name}"?`}
        description="Notifications will stop going to this destination. This cannot be undone."
        confirmLabel="Delete"
        variant="destructive"
        isPending={remove.isPending}
        onConfirm={() => {
          if (deleteIntent)
            remove.mutate(deleteIntent, { onSettled: () => setConfirmDelete(false) });
        }}
      />
    </div>
  );
}

/**
 * Destinations that plugins provide, such as ntfy or Pushover. Shown while
 * an enabled plugin offers a kind, or the profile still has destinations
 * from one that was removed.
 */
export function PluginDestinationsSection() {
  const capability = useNotificationCapability();
  const available = capability.data?.plugin_destinations?.available ?? false;
  const channels = useNotificationPluginChannels(available);
  const destinations = useNotificationPluginDestinations(capability.isSuccess);
  const { data: globalPrefs } = useNotificationPreferences();
  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<NotificationPluginDestination | null>(null);

  const rows = destinations.data ?? [];
  if (!available && rows.length === 0) return null;
  const maxPerProfile = capability.data?.plugin_destinations?.max_per_profile ?? 10;
  const atLimit = rows.length >= maxPerProfile;
  const kinds = channels.data ?? [];

  return (
    <>
      <SettingsGroup
        title="Other destinations"
        description="Send this profile's notifications to services your server's plugins support."
      >
        {destinations.isLoading ? (
          <Skeleton className="h-24 w-full" />
        ) : (
          <>
            {rows.map((destination) => (
              <DestinationCard
                key={`${notificationScope()}:${destination.id}`}
                destination={destination}
                onEdit={() => {
                  setEditing(destination);
                  setFormOpen(true);
                }}
              />
            ))}
            {rows.length === 0 && (
              <div className="text-muted-foreground flex items-center gap-2 text-sm">
                <BellPlus className="h-4 w-4" />
                No destinations yet.
              </div>
            )}
            {kinds.length > 0 && (
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
                  Add destination
                </Button>
                {atLimit && (
                  <span className="text-muted-foreground ml-2 text-xs">
                    Limit of {maxPerProfile} destinations reached
                  </span>
                )}
              </div>
            )}
          </>
        )}
      </SettingsGroup>

      {formOpen && (
        <DestinationFormDialog
          key={`${notificationScope()}:${editing?.id ?? "new"}`}
          open={formOpen}
          onOpenChange={(open) => {
            setFormOpen(open);
            if (!open) setEditing(null);
          }}
          channels={kinds}
          destination={editing}
          globalPrefs={globalPrefs}
        />
      )}
    </>
  );
}
