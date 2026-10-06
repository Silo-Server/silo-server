import { useMemo, useState } from "react";
import { Link } from "react-router";
import { ArrowUpRight, Trash2 } from "lucide-react";

import { ConfirmDialog } from "@/components/ConfirmDialog";
import { PlatformTile, classifyPlatform, platformLabel } from "@/components/admin/deviceOverrides";
import { RegistrySettingControl } from "@/components/settings/RegistrySettingControl";
import { Button } from "@/components/ui/button";
import {
  useDeleteAdminUserSetting,
  useDeleteAllAdminUserDeviceSettingsForDevice,
  useUpdateAdminUserSetting,
  type AdminUserSettingEntry,
} from "@/hooks/queries/admin/users";
import {
  formatSettingValue,
  getSettingDefinition,
  isStructuredSetting,
} from "@/lib/settingsDisplay";

import { formatLastSeen } from "../format";
import { DetailCard } from "../ui";
import { AddSettingPicker } from "./AddSettingPicker";
import {
  addableSettings,
  categoryTitle,
  identityOf,
  levelNoun,
  levelPlace,
  replacedValue,
  replacesText,
  type PreferenceLevel,
} from "./levels";
import { SettingJsonDialog } from "./SettingJsonDialog";

const SYNC_NOTE = "The change reaches the app the next time it syncs.";

function levelTitle(level: PreferenceLevel): string {
  return level.kind === "account" ? level.name : `${level.name} · ${level.profileName}`;
}

function levelDescription(level: PreferenceLevel): string {
  const profile = level.profileName;
  switch (level.kind) {
    case "account":
      return "These apply to every profile on the account.";
    case "profile":
      return `These apply on every device ${profile} uses.`;
    case "device": {
      const meta = [
        level.device?.platform ? platformLabel(level.device.platform) : null,
        level.device?.lastSeenAt ? `last seen ${formatLastSeen(level.device.lastSeenAt)}` : null,
      ].filter(Boolean);
      const lead = meta.length > 0 ? `${meta.join(" · ")}. ` : "";
      return `${lead}These replace ${profile}'s settings on this device only; everything else follows ${profile}.`;
    }
    case "client":
      return `These replace ${profile}'s settings in these apps only; everything else follows ${profile}.`;
    case "library":
      return `These replace ${profile}'s settings in this library only; everything else follows ${profile}.`;
    case "series":
      return `These replace ${profile}'s settings for this series only; everything else follows ${profile}.`;
  }
}

function emptyText(level: PreferenceLevel): { title: string; detail: string } {
  if (level.kind === "profile") {
    return {
      title: `Nothing changed for ${level.profileName}`,
      detail: "It uses the app defaults everywhere.",
    };
  }
  if (level.kind === "account") {
    return {
      title: "Nothing changed for the account",
      detail: "Every profile uses its own settings.",
    };
  }
  return {
    title: `Nothing changed on this ${levelNoun(level)}`,
    detail: `It uses ${level.profileName}'s settings everywhere.`,
  };
}

/** One setting the selected level stores, with what it replaces. */
function SettingRow({
  entry,
  replaces,
  busy,
  onChange,
  onEditJson,
  onRemove,
}: {
  entry: AdminUserSettingEntry;
  replaces: string;
  busy: boolean;
  onChange: (value: string) => void;
  onEditJson: () => void;
  onRemove: () => void;
}) {
  const definition = getSettingDefinition(entry.key);
  const label = definition?.label ?? entry.key;
  // A key this build does not know, an object-valued one, or one the manifest
  // edits through a panel has no inline control that renders its value
  // truthfully; those go through the raw JSON editor.
  const jsonOnly = !definition || isStructuredSetting(definition);
  return (
    <div
      data-setting-row={entry.key}
      className="border-border/50 grid grid-cols-[auto_minmax(0,1fr)] items-center gap-x-3 gap-y-2 border-t px-4 py-3 first:border-t-0 sm:px-5 md:grid-cols-[auto_minmax(0,1fr)_auto]"
    >
      <Button
        variant="ghost"
        size="icon"
        className="text-muted-foreground hover:text-destructive size-7"
        aria-label={`Remove ${label}`}
        disabled={busy}
        onClick={onRemove}
      >
        <Trash2 className="h-3.5 w-3.5" />
      </Button>
      <div className="min-w-0">
        <div className="text-muted-foreground text-[11px] font-medium">
          {definition ? categoryTitle(definition.category) : "Not in this version"}
        </div>
        <div className="text-sm font-medium break-words" title={definition?.description}>
          {label}
        </div>
        <div className="text-muted-foreground text-xs">{replaces}</div>
      </div>
      <div className="col-start-2 flex min-w-0 flex-wrap items-center gap-2 md:col-start-auto md:justify-end">
        {jsonOnly ? (
          <>
            <span className="text-muted-foreground max-w-[14rem] truncate font-mono text-xs">
              {formatSettingValue(entry.key, entry.value)}
            </span>
            {/* Without a definition the save can't keep the value's JSON type,
                so a key from a newer server is shown but not edited here. */}
            {definition ? (
              <Button variant="outline" size="sm" disabled={busy} onClick={onEditJson}>
                Edit JSON
              </Button>
            ) : (
              <span className="text-muted-foreground text-xs">View only</span>
            )}
          </>
        ) : (
          <RegistrySettingControl
            definition={definition}
            value={entry.value}
            disabled={busy}
            onChange={onChange}
          />
        )}
      </div>
    </div>
  );
}

/**
 * The selected level: only the settings it stores, each with what it
 * replaces, plus adding one, removing one, and clearing a device. Every write
 * addresses one row by its full identity.
 */
export function LevelSettings({
  userId,
  level,
  profileEntries,
  profileAllEntries,
}: {
  userId: number;
  level: PreferenceLevel;
  /** The owning profile's own (All devices) settings, for "Replaces Main: …". */
  profileEntries: readonly AdminUserSettingEntry[];
  /** Every scope the profile stores; see replacedValue. */
  profileAllEntries: readonly AdminUserSettingEntry[];
}) {
  const updateSetting = useUpdateAdminUserSetting();
  const deleteSetting = useDeleteAdminUserSetting();
  const clearDevice = useDeleteAllAdminUserDeviceSettingsForDevice();
  const [removing, setRemoving] = useState<AdminUserSettingEntry | null>(null);
  const [clearing, setClearing] = useState(false);
  const [jsonEditor, setJsonEditor] = useState<AdminUserSettingEntry | null>(null);
  const [jsonValue, setJsonValue] = useState("");
  const closeJsonEditor = () => {
    setJsonEditor(null);
    setJsonValue("");
  };

  const busy = updateSetting.isPending || deleteSetting.isPending || clearDevice.isPending;
  const addable = useMemo(
    () => addableSettings(level, profileEntries, profileAllEntries),
    [level, profileEntries, profileAllEntries],
  );
  const canAdd = addable.length > 0;
  const addLabel = `Add a setting for this ${levelNoun(level)}`;
  const replacedFor = (key: string) => replacedValue(level, key, profileEntries, profileAllEntries);

  const add = async (keys: string[]) => {
    // A retry after a partial failure must not overwrite what already landed.
    const stored = new Set(level.entries.map((entry) => entry.key));
    for (const key of keys.filter((k) => !stored.has(k))) {
      // Start from the value it replaces, so nothing changes until edited.
      await updateSetting.mutateAsync({
        userId,
        key,
        identity: level.identity,
        value: replacedFor(key).raw,
      });
    }
  };

  const picker = canAdd ? (
    <AddSettingPicker label={addLabel} categories={addable} disabled={busy} onAdd={add} />
  ) : null;

  const removal = removing ? replacedFor(removing.key) : null;
  const removingLabel = removing ? (getSettingDefinition(removing.key)?.label ?? removing.key) : "";
  const fromProfile = removal?.profileName != null;
  const removalTitle = fromProfile
    ? `Use ${level.profileName}'s value ${levelPlace(level)}?`
    : "Go back to the app default?";
  const removalValue = removal?.display ? `, ${removal.display},` : "";
  const removalDescription = fromProfile
    ? `${removingLabel} goes back to ${level.profileName}'s setting${removalValue} ${levelPlace(level)}. ${SYNC_NOTE}`
    : `${removingLabel} goes back to the app default${removalValue} ${levelPlace(level)}. ${SYNC_NOTE}`;

  const device = level.kind === "device" ? level.device : undefined;
  const empty = emptyText(level);

  return (
    <>
      <DetailCard
        title={
          device ? (
            <span className="flex items-center gap-3">
              <PlatformTile kind={classifyPlatform(device.platform)} size="sm" />
              {levelTitle(level)}
            </span>
          ) : (
            levelTitle(level)
          )
        }
        description={levelDescription(level)}
        actions={
          device ? (
            <>
              <Button variant="ghost" size="sm" asChild>
                <Link to={`/admin/devices/${userId}/${encodeURIComponent(device.id)}`}>
                  Open device
                  <ArrowUpRight className="h-3 w-3" />
                </Link>
              </Button>
              <Button
                variant="outline"
                size="sm"
                disabled={busy || level.entries.length === 0}
                onClick={() => setClearing(true)}
              >
                Clear device settings
              </Button>
            </>
          ) : undefined
        }
        footer={level.entries.length > 0 ? picker : undefined}
      >
        {level.entries.length === 0 ? (
          <div className="px-5 py-10 text-center">
            <p className="text-sm font-medium">{empty.title}</p>
            <p className="text-muted-foreground mt-1 text-[13px]">{empty.detail}</p>
            {picker ? <div className="mt-3 flex justify-center">{picker}</div> : null}
          </div>
        ) : (
          level.entries.map((entry) => (
            <SettingRow
              key={`${level.id}:${entry.key}`}
              entry={entry}
              replaces={replacesText(replacedFor(entry.key))}
              busy={busy}
              onChange={(value) =>
                updateSetting.mutate({ userId, key: entry.key, identity: identityOf(entry), value })
              }
              onEditJson={() => {
                setJsonEditor(entry);
                setJsonValue(entry.value);
              }}
              onRemove={() => setRemoving(entry)}
            />
          ))
        )}
      </DetailCard>

      <ConfirmDialog
        open={removing !== null}
        onOpenChange={(open) => {
          if (!open) setRemoving(null);
        }}
        title={removalTitle}
        description={removalDescription}
        confirmLabel={fromProfile ? `Use ${level.profileName}'s` : "Use app default"}
        onConfirm={() => {
          if (removing) {
            deleteSetting.mutate({ userId, key: removing.key, identity: identityOf(removing) });
          }
          setRemoving(null);
        }}
      />
      {device && level.profileId !== undefined ? (
        <ConfirmDialog
          open={clearing}
          onOpenChange={setClearing}
          title={`Clear ${level.profileName}'s settings on ${level.name}?`}
          description={`Every setting ${level.profileName} changed on this device is removed, so it uses ${level.profileName}'s settings everywhere. ${SYNC_NOTE}`}
          confirmLabel="Clear settings"
          variant="destructive"
          onConfirm={() => {
            clearDevice.mutate({
              userId,
              profileId: level.profileId ?? "",
              deviceId: device.id,
              keys: level.entries.map((entry) => entry.key),
            });
            setClearing(false);
          }}
        />
      ) : null}
      <SettingJsonDialog
        settingKey={jsonEditor?.key ?? null}
        value={jsonValue}
        description="Edit the raw value. This setting has no inline control, so saving replaces the stored value wholesale. The trash button removes it."
        saveLabel="Save value"
        onValueChange={setJsonValue}
        onCancel={closeJsonEditor}
        onSave={() => {
          if (!jsonEditor) return;
          updateSetting.mutate(
            { userId, key: jsonEditor.key, identity: identityOf(jsonEditor), value: jsonValue },
            { onSuccess: closeJsonEditor },
          );
        }}
      />
    </>
  );
}
