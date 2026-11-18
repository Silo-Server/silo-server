import { useState } from "react";
import { AlertTriangle, BellPlus, Loader2, Pencil, Plus, Send, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { notificationScope } from "@/api/v2/notifications";
import type { NotificationPreferences, PluginAdminForm } from "@/api/types";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { SchemaForm } from "@/components/admin/plugins/SchemaForm";
import {
  buildSchemaValues,
  type BuildSchemaValuesOptions,
  type FieldType,
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
  pluginSettingsRejection,
  useCreateNotificationPluginDestination,
  useDeleteNotificationPluginDestination,
  useNotificationPluginChannels,
  useNotificationPluginDestinations,
  useTestNotificationPluginDestination,
  useUpdateNotificationPluginDestination,
  type NotificationPluginChannel,
  type NotificationPluginDestination,
  type NotificationPluginSettingsField,
} from "@/hooks/queries/notificationPluginDestinations";
import { useNotificationCapability } from "@/hooks/queries/notificationWebhooks";
import { useT } from "@/i18n";
import { formatRelativeTime } from "@/lib/date";
import { DESTINATION_NOTIFY_FIELDS, type DestinationNotifyKey } from "./notificationReasonFields";

const channelKey = (c: { plugin_id: string; capability_id: string }) =>
  `${c.plugin_id}/${c.capability_id}`;

/**
 * The destination's flat settings form as the shared SchemaForm renders it.
 * A setting the server lists in secretsSet is write-only even when the
 * current form no longer marks it secret: it renders as a password left
 * blank, and blank keeps the stored value, so it isn't required either.
 */
function destinationForm(
  fields: NotificationPluginSettingsField[],
  secretsSet: string[],
): PluginAdminForm {
  return {
    fields: fields.map((field) => {
      const stored = secretsSet.includes(field.key);
      const secret = field.secret || stored;
      return {
        key: field.key,
        label: field.label,
        description: field.description || undefined,
        placeholder: field.placeholder || undefined,
        control: secret
          ? "PASSWORD"
          : (field.control.toUpperCase() as Uppercase<typeof field.control>),
        required: field.required && !stored,
        secret,
        multiline: false,
        default_value: field.default_value,
        options: field.options,
        validation: {
          has_min: field.minimum != null,
          min: field.minimum ?? undefined,
          has_max: field.maximum != null,
          max: field.maximum ?? undefined,
          pattern: field.pattern || undefined,
          min_length: field.min_length ?? undefined,
          max_length: field.max_length ?? undefined,
        },
      };
    }),
  };
}

const destinationFieldTypes = (fields: NotificationPluginSettingsField[]) =>
  Object.fromEntries(fields.map((field) => [field.key, field.type])) as Record<string, FieldType>;

function ReasonToggles({
  reasons,
  onChange,
  globalPrefs,
}: {
  reasons: Record<DestinationNotifyKey, boolean>;
  onChange: (next: Record<DestinationNotifyKey, boolean>) => void;
  globalPrefs: NotificationPreferences | undefined;
}) {
  const t = useT();
  return (
    <div className="space-y-2">
      <Label>{t("web.settings.notifications.webhooks.sendFor")}</Label>
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
              {t(field.labelKey)}
              {globallyDisabled && (
                <span className="text-muted-foreground ms-2 text-xs">
                  {t("web.settings.notifications.webhooks.disabledInProfile")}
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
  const t = useT();
  const create = useCreateNotificationPluginDestination();
  const update = useUpdateNotificationPluginDestination();
  const editing = destination != null;
  const [kind, setKind] = useState(
    destination ? channelKey(destination) : channels[0] ? channelKey(channels[0]) : "",
  );
  const channel = channels.find((c) => channelKey(c) === kind);
  const [name, setName] = useState(destination?.name ?? channel?.display_name ?? "");
  const [settings, setSettings] = useState<Record<string, unknown>>(destination?.settings ?? {});
  // The plugin's reason for refusing the last submitted settings, shown under
  // the fields until they change.
  const [settingsRejection, setSettingsRejection] = useState<string | null>(null);
  const [settingsValid, setSettingsValid] = useState(true);
  const [reasons, setReasons] = useState<Record<DestinationNotifyKey, boolean>>({
    notify_next_up: destination?.notify_next_up ?? true,
    notify_requests: destination?.notify_requests ?? true,
  });
  const pending = create.isPending || update.isPending;
  // The server names every secret with a stored value here, however the
  // form marks it. Left blank, such a secret stays as saved.
  const secretsSet = destination?.secret_settings_set ?? [];
  const fields = channel?.settings_form.fields ?? [];
  const form = fields.length > 0 ? destinationForm(fields, secretsSet) : undefined;

  // The form keeps number inputs as text and shows defaults it never
  // stores, so settings go out typed as each field declares.
  const settingsToSend = (options?: BuildSchemaValuesOptions) =>
    form ? buildSchemaValues(form, settings, destinationFieldTypes(fields), options) : settings;

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
    setSettingsRejection(null);
    const onError = (error: unknown) => {
      // The rejection shows under the settings fields; a channel without a
      // form has none, so it falls back to a toast.
      const rejection = pluginSettingsRejection(error);
      if (rejection && form) setSettingsRejection(rejection);
      else
        toast.error(
          pluginDestinationErrorMessage(
            error,
            t("web.settings.notifications.destinations.saveFailed"),
          ),
        );
    };
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
          toast.success(t("web.settings.notifications.destinations.added", { name: created.name }));
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
            {editing
              ? t("web.settings.notifications.webhooks.editTitle", { name: destination.name })
              : t("web.settings.notifications.destinations.addTitle")}
          </DialogTitle>
          <DialogDescription>
            {channel?.description || t("web.settings.notifications.destinations.dialogDescription")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          {!editing && channels.length > 1 && (
            <div className="space-y-1.5">
              <Label htmlFor="plugin-destination-kind">
                {t("web.settings.notifications.destinations.kind")}
              </Label>
              <Select
                value={kind}
                onValueChange={(next) => {
                  setKind(next);
                  setSettings({});
                  setSettingsRejection(null);
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
            <Label htmlFor="plugin-destination-name">
              {t("web.settings.notifications.webhooks.name")}
            </Label>
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
                onChange={(next) => {
                  setSettings(next);
                  setSettingsRejection(null);
                }}
                idPrefix={`plugin-destination-${kind}`}
                onValidityChange={setSettingsValid}
              />
              {settingsRejection && (
                <p role="alert" className="text-destructive text-sm">
                  {settingsRejection}
                </p>
              )}
              {secretsSet.length > 0 && (
                <p className="text-muted-foreground text-xs">
                  {t("web.settings.notifications.destinations.secretsKept")}
                </p>
              )}
            </div>
          )}
          <ReasonToggles reasons={reasons} onChange={setReasons} globalPrefs={globalPrefs} />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button onClick={submit} disabled={pending || !name.trim() || !channel || !settingsValid}>
            {pending && <Loader2 className="me-2 h-4 w-4 animate-spin" />}
            {editing ? t("common.save") : t("web.settings.notifications.destinations.addSubmit")}
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
  const t = useT();
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
  ).map((field) => t(field.labelKey));
  const failureMessage = destination.last_failure_message;
  const failureText = failureMessage
    ? lastFailure
      ? t("web.settings.notifications.destinations.lastFailureAt", {
          when: lastFailure,
          message: failureMessage,
        })
      : t("web.settings.notifications.destinations.lastFailure", { message: failureMessage })
    : lastFailure
      ? t("web.settings.notifications.destinations.lastFailureAtUnknown", { when: lastFailure })
      : t("web.settings.notifications.destinations.lastFailureUnknown");

  return (
    <div className="border-border/60 space-y-2 rounded-xl border p-4">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-medium">{destination.name}</span>
        <Badge variant="secondary">
          {destination.available
            ? destination.channel_name
            : t("web.settings.notifications.destinations.unavailable")}
        </Badge>
        <div className="ms-auto flex items-center gap-1.5">
          <span className="text-muted-foreground text-xs">
            {destination.enabled
              ? t("web.settings.value.enabled")
              : t("web.settings.value.disabled")}
          </span>
          <Switch
            checked={destination.enabled}
            onCheckedChange={(checked) =>
              update.mutate(
                { id: destination.id, etag: destination.etag, enabled: checked },
                {
                  onError: (error) =>
                    toast.error(
                      pluginDestinationErrorMessage(
                        error,
                        t("web.settings.notifications.destinations.updateFailed"),
                      ),
                    ),
                },
              )
            }
          />
        </div>
      </div>

      {!destination.available && (
        <div className="text-muted-foreground text-xs">
          {t("web.settings.notifications.destinations.unavailableNote")}
        </div>
      )}
      <div className="text-muted-foreground text-xs">
        {enabledReasons.length === DESTINATION_NOTIFY_FIELDS.length
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
      {(failing || destination.disabled_reason) && (
        <div className="flex items-start gap-1.5 text-xs text-amber-500">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          <span>
            {destination.disabled_reason
              ? t("web.settings.notifications.webhooks.disabledReason", {
                  reason: destination.disabled_reason,
                })
              : failureText}
          </span>
        </div>
      )}
      {testResult && (
        <div className={`text-xs ${testResult.ok ? "text-emerald-500" : "text-amber-500"}`}>
          {testResult.message
            ? t("web.settings.notifications.destinations.testResultDetail", {
                outcome: testResult.ok ? "ok" : "failed",
                message: testResult.message,
              })
            : t("web.settings.notifications.destinations.testResult", {
                outcome: testResult.ok ? "ok" : "failed",
              })}
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
                toast.error(
                  pluginDestinationErrorMessage(
                    error,
                    t("web.settings.notifications.webhooks.testFailed"),
                  ),
                ),
            })
          }
        >
          {test.isPending ? (
            <Loader2 className="me-1.5 h-3.5 w-3.5 animate-spin" />
          ) : (
            <Send className="me-1.5 h-3.5 w-3.5" />
          )}
          {t("web.settings.notifications.webhooks.test")}
        </Button>
        <Button variant="outline" size="sm" onClick={onEdit} disabled={!destination.available}>
          <Pencil className="me-1.5 h-3.5 w-3.5" />
          {t("web.settings.notifications.webhooks.edit")}
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
          <Trash2 className="me-1.5 h-3.5 w-3.5" />
          {t("web.settings.notifications.webhooks.delete")}
        </Button>
      </div>

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={t("web.settings.notifications.webhooks.deleteTitle", { name: destination.name })}
        description={t("web.settings.notifications.destinations.deleteDescription")}
        confirmLabel={t("web.settings.notifications.webhooks.delete")}
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
  const t = useT();
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
        title={t("web.settings.notifications.destinations.title")}
        description={t("web.settings.notifications.destinations.description")}
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
                {t("web.settings.notifications.destinations.empty")}
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
                  <Plus className="me-1.5 h-4 w-4" />
                  {t("web.settings.notifications.destinations.add")}
                </Button>
                {atLimit && (
                  <span className="text-muted-foreground ms-2 text-xs">
                    {t("web.settings.notifications.destinations.limit", {
                      count: maxPerProfile,
                      count_text: String(maxPerProfile),
                    })}
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
